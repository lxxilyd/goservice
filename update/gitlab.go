package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// glLink is one downloadable asset attached to a GitLab release. "url" points
// at wherever the file lives; "direct_asset_url" is GitLab's own permanent
// download path and is the one that works for a private project.
type glLink struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	DirectAssetURL string `json:"direct_asset_url"`
}

type glRelease struct {
	TagName     string `json:"tag_name"`
	Description string `json:"description"`
	Assets      struct {
		Links []glLink `json:"links"`
	} `json:"assets"`
}

// GitLabSource resolves releases from a GitLab project, on gitlab.com or on a
// self-hosted instance.
//
// GitLab does not share GitHub's release API, so this is a separate Source
// rather than a BaseURL on GitHubSource: the project is addressed by ID or by
// its full path (URL-encoded for us, "group/sub/app" is fine), the release
// carries its files under assets.links instead of assets, and the description
// field is "description", not "body".
//
// Asset names are matched against AssetPattern when set, otherwise the default
// "<project>_<os>_<arch>" is used, where <project> is the last path segment.
// With a numeric project ID the default cannot work, so set AssetPattern.
type GitLabSource struct {
	// Project is the numeric project ID or the full path, "group/sub/app".
	Project string
	// BaseURL is the API root. Empty means https://gitlab.com/api/v4; a bare
	// instance URL without "/api/v4" gets it appended.
	BaseURL string
	// Auth is used for both the release API and the download, which is what
	// makes a private project work. A personal access token goes in
	// "PRIVATE-TOKEN" (the default); an OAuth2 token sets Header to
	// "Authorization".
	Auth Auth
	// AssetPattern renders the expected asset name for a request.
	AssetPattern func(Request) string
	// IncludePrerelease switches from /releases/latest to the newest /releases,
	// which is also how upcoming (future dated) releases are picked up.
	IncludePrerelease bool
	Client            *http.Client
}

func (g *GitLabSource) endpoint() string {
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		return "https://gitlab.com/api/v4"
	}
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	return base
}

func (g *GitLabSource) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// escapeProject turns "group/sub/app" into "group%2Fsub%2Fapp" as the API
// requires, and leaves an already encoded path or a numeric ID alone.
func escapeProject(project string) string {
	if strings.Contains(project, "%2F") || strings.Contains(project, "%2f") {
		return project
	}
	return url.PathEscape(project)
}

func (g *GitLabSource) Latest(ctx context.Context, req Request) (Release, error) {
	if g.Project == "" {
		return Release{}, fmt.Errorf("update: GitLabSource needs Project")
	}

	base := g.endpoint()
	project := escapeProject(g.Project)
	listMode := g.IncludePrerelease || req.Channel == "beta"
	target := fmt.Sprintf("%s/projects/%s/releases/latest", base, project)
	if listMode {
		target = fmt.Sprintf("%s/projects/%s/releases?per_page=%d", base, project, listPageSize)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Release{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	applyHeader(httpReq.Header, g.Auth.HeaderFor("PRIVATE-TOKEN"))

	resp, err := g.client().Do(httpReq)
	if err != nil {
		return Release{}, fmt.Errorf("update: querying GitLab: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: GitLab returned %s for %s", resp.Status, target)
	}

	var releases []glRelease
	if listMode {
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return Release{}, fmt.Errorf("update: decoding GitLab releases: %w", err)
		}
	} else {
		var single glRelease
		if err := json.NewDecoder(resp.Body).Decode(&single); err != nil {
			return Release{}, fmt.Errorf("update: decoding GitLab release: %w", err)
		}
		releases = []glRelease{single}
	}
	if len(releases) == 0 {
		return Release{}, fmt.Errorf("update: no releases found for %s", g.Project)
	}

	// Newest first, and the newest release does not always carry a build for
	// every platform, so keep looking instead of failing on the first entry.
	expected := g.assetName(req)
	for _, release := range releases {
		if link := pickLink(release.Assets.Links, expected); link != nil {
			return g.release(release, link), nil
		}
	}
	return Release{}, fmt.Errorf("update: no release of %s has an asset matching %q", g.Project, expected)
}

func (g *GitLabSource) assetName(req Request) string {
	if g.AssetPattern != nil {
		return g.AssetPattern(req)
	}
	name := g.Project
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return fmt.Sprintf("%s_%s_%s", name, req.OS, req.Arch)
}

// release carries the token through to the download, which is what makes a
// private project work.
func (g *GitLabSource) release(rel glRelease, link *glLink) Release {
	downloadURL := link.DirectAssetURL
	if downloadURL == "" {
		downloadURL = link.URL
	}
	return Release{
		Version: rel.TagName,
		URL:     downloadURL,
		Notes:   rel.Description,
		Header:  g.Auth.HeaderFor("PRIVATE-TOKEN"),
	}
}

func pickLink(links []glLink, expected string) *glLink {
	for i := range links {
		if links[i].Name == expected {
			return &links[i]
		}
	}
	for i := range links {
		if strings.Contains(links[i].Name, expected) {
			return &links[i]
		}
	}
	return nil
}
