package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Body    string    `json:"body"`
	Assets  []ghAsset `json:"assets"`
}

// GitHubSource resolves releases from a GitHub repository.
//
// Asset names are matched against AssetPattern when set, otherwise the default
// "<repo>_<os>_<arch>" is used: an exact name wins, then a "contains" match so
// names like mydaemon_linux_amd64.tar.gz still resolve.
type GitHubSource struct {
	Owner string
	Repo  string
	// AssetPattern renders the expected asset name for a request.
	AssetPattern func(Request) string
	// Token is an optional GitHub token, needed for private repositories and
	// useful against the anonymous rate limit.
	Token string
	// IncludePrerelease switches from /releases/latest to the newest /releases.
	IncludePrerelease bool
	// BaseURL is the API root of the host. Empty means https://api.github.com;
	// a Gitea or Forgejo instance uses https://git.example.com/api/v1, which
	// exposes the same /repos/{owner}/{repo}/releases layout.
	BaseURL string
	Client  *http.Client
}

// endpoint returns the API root and the page-size parameter it understands:
// GitHub calls it per_page, Gitea and Forgejo call it limit.
func (g *GitHubSource) endpoint() (base, pageParam string) {
	base = strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		return "https://api.github.com", "per_page"
	}
	return base, "limit"
}

func (g *GitHubSource) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *GitHubSource) Latest(ctx context.Context, req Request) (Release, error) {
	if g.Owner == "" || g.Repo == "" {
		return Release{}, fmt.Errorf("update: GitHubSource needs Owner and Repo")
	}

	base, pageParam := g.endpoint()
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, g.Owner, g.Repo)
	listMode := g.IncludePrerelease || req.Channel == "beta"
	if listMode {
		url = fmt.Sprintf("%s/repos/%s/%s/releases?%s=1", base, g.Owner, g.Repo, pageParam)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+g.Token)
	}

	resp, err := g.client().Do(httpReq)
	if err != nil {
		return Release{}, fmt.Errorf("update: querying GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: GitHub returned %s for %s", resp.Status, url)
	}

	var latest ghRelease
	if listMode {
		var releases []ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return Release{}, fmt.Errorf("update: decoding GitHub releases: %w", err)
		}
		if len(releases) == 0 {
			return Release{}, fmt.Errorf("update: no releases found for %s/%s", g.Owner, g.Repo)
		}
		latest = releases[0]
	} else {
		if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
			return Release{}, fmt.Errorf("update: decoding GitHub release: %w", err)
		}
	}

	expected := g.assetName(req)
	chosen := pickAsset(latest.Assets, expected)
	if chosen == nil {
		return Release{}, fmt.Errorf("update: release %s has no asset matching %q", latest.TagName, expected)
	}

	return Release{
		Version: latest.TagName,
		URL:     chosen.BrowserDownloadURL,
		SHA256:  strings.TrimPrefix(chosen.Digest, "sha256:"),
		Notes:   latest.Body,
	}, nil
}

func (g *GitHubSource) assetName(req Request) string {
	if g.AssetPattern != nil {
		return g.AssetPattern(req)
	}
	return fmt.Sprintf("%s_%s_%s", g.Repo, req.OS, req.Arch)
}

func pickAsset(assets []ghAsset, expected string) *ghAsset {
	for i := range assets {
		if assets[i].Name == expected {
			return &assets[i]
		}
	}
	for i := range assets {
		if strings.Contains(assets[i].Name, expected) {
			return &assets[i]
		}
	}
	return nil
}

// URLSource points at one release artifact directly, which is the shortest
// path when the releases live on a self-hosted Gitea/Forgejo (or any static
// file server) and you do not want to depend on a release API:
//
//	URL: https://git.example.com/lxxilyd/gottyd/releases/download/v2026.10.23/gottyd_linux_amd64
//
// The version is taken from Version when set, otherwise from the version-like
// segment of the URL (the release tag). SHA256 is optional: leave it empty to
// skip the built-in digest check, or point it at a published checksum.
type URLSource struct {
	URL     string
	Version string
	SHA256  string
	Notes   string
	// Token is sent as a bearer token, for private repositories.
	Token string
	// Header overrides the Authorization header name (default "Authorization").
	Header string
	Client *http.Client
}

// versionFromURL returns the version-like token in a URL, for example
//
//	.../releases/download/v2026.10.23-main-abc1234/gottyd_linux_amd64 -> v2026.10.23-main-abc1234
//	.../1.4.2/app_linux                                              -> 1.4.2
//
// It scans by hand instead of using regexp: one pattern is not worth linking
// the regexp engine and its unicode tables (about 250 KB).
func versionFromURL(s string) string {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	isWord := func(c byte) bool {
		return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '+'
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			continue
		}
		j := i
		ok := true
		for seg := 0; seg < 3; seg++ {
			start := j
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			if j == start {
				ok = false
				break
			}
			if seg < 2 {
				if j >= len(s) || s[j] != '.' {
					ok = false
					break
				}
				j++
			}
		}
		if !ok {
			continue
		}
		start := i
		if start > 0 && (s[start-1] == 'v' || s[start-1] == 'V') {
			start--
		}
		end := j
		for end < len(s) && isWord(s[end]) {
			end++
		}
		return s[start:end]
	}
	return ""
}

func (s *URLSource) Latest(ctx context.Context, req Request) (Release, error) {
	if s.URL == "" {
		return Release{}, errors.New("update: URLSource needs URL")
	}
	version := s.Version
	if version == "" {
		version = versionFromURL(s.URL)
	}
	if version == "" {
		return Release{}, fmt.Errorf("update: cannot tell the version from %q, set Version", s.URL)
	}
	return Release{
		Version: version,
		URL:     s.URL,
		SHA256:  strings.TrimPrefix(s.SHA256, "sha256:"),
		Notes:   s.Notes,
	}, nil
}

// StaticSource reads a small JSON document describing the newest build:
//
//	{"version": "1.4.2", "url": "https://example.com/app", "sha256": "...", "notes": "..."}
//
// A relative "url" (starting with "/") is resolved against the feed host, which
// keeps the feed usable behind a CDN.
type StaticSource struct {
	URL    string
	Token  string
	Header string
	Client *http.Client
}

func (s *StaticSource) Latest(ctx context.Context, req Request) (Release, error) {
	if s.URL == "" {
		return Release{}, fmt.Errorf("update: StaticSource needs a URL")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return Release{}, err
	}
	if s.Token != "" {
		header := s.Header
		if header == "" {
			header = "Authorization"
		}
		httpReq.Header.Set(header, s.Token)
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Release{}, fmt.Errorf("update: fetching %s: %w", s.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: %s returned %s", s.URL, resp.Status)
	}

	var payload struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		SHA256  string `json:"sha256"`
		Notes   string `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("update: decoding %s: %w", s.URL, err)
	}
	if payload.Version == "" || payload.URL == "" {
		return Release{}, fmt.Errorf("update: %s did not report both version and url", s.URL)
	}

	if strings.HasPrefix(payload.URL, "/") {
		if resolved, ok := resolveRelative(s.URL, payload.URL); ok {
			payload.URL = resolved
		}
	}

	return Release{
		Version: payload.Version,
		URL:     payload.URL,
		SHA256:  strings.TrimPrefix(payload.SHA256, "sha256:"),
		Notes:   payload.Notes,
	}, nil
}

// resolveRelative turns "/app" into "https://host/app" using the feed URL.
func resolveRelative(base, ref string) (string, bool) {
	schemeIdx := strings.Index(base, "://")
	if schemeIdx <= 0 {
		return "", false
	}
	rest := base[schemeIdx+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return base + ref, true
	}
	return base[:schemeIdx+3] + rest[:slash] + ref, true
}

// CurrentPlatform builds a Request for the running binary.
func CurrentPlatform(current, channel string) Request {
	return Request{
		Current: current,
		Channel: channel,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}
