package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubSourceEndpoint(t *testing.T) {
	base, param := (&GitHubSource{}).endpoint()
	if base != "https://api.github.com" || param != "per_page" {
		t.Fatalf("default endpoint = %q/%q", base, param)
	}
	base, param = (&GitHubSource{BaseURL: "https://git.example.com/api/v1/"}).endpoint()
	if base != "https://git.example.com/api/v1" || param != "limit" {
		t.Fatalf("custom endpoint = %q/%q", base, param)
	}
}

// TestGitHubSourceGitea drives the source against a server that speaks the
// Gitea release API, which has the same shape as GitHub's.
func TestGitHubSourceGitea(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			io.WriteString(w, `{"tag_name":"v1.2.3","body":"notes","assets":[
				{"name":"gottyd_linux_amd64","browser_download_url":"https://dl.example.com/v1.2.3"}]}`)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			io.WriteString(w, `[{"tag_name":"v1.3.0","assets":[
				{"name":"gottyd_linux_amd64","browser_download_url":"https://dl.example.com/v1.3.0"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := &GitHubSource{
		Owner:   "lxxilyd",
		Repo:    "gottyd",
		BaseURL: srv.URL + "/api/v1",
		Client:  srv.Client(),
	}

	rel, err := src.Latest(context.Background(), Request{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.2.3" || rel.URL != "https://dl.example.com/v1.2.3" {
		t.Fatalf("release = %+v", rel)
	}
	// Gitea does not report a per-asset digest, so the built-in check is off.
	if rel.SHA256 != "" {
		t.Fatalf("SHA256 = %q, want empty", rel.SHA256)
	}
	want := "/api/v1/repos/lxxilyd/gottyd/releases/latest"
	if len(seen) != 1 || seen[0] != want {
		t.Fatalf("requests = %v, want %s", seen, want)
	}

	src.IncludePrerelease = true
	rel, err = src.Latest(context.Background(), Request{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Latest(list): %v", err)
	}
	if rel.Version != "v1.3.0" {
		t.Fatalf("list release = %+v", rel)
	}
	if want := "/api/v1/repos/lxxilyd/gottyd/releases?limit=20"; seen[1] != want {
		t.Fatalf("list request = %q, want %q", seen[1], want)
	}
}

// TestGitHubSourceListFallsBack checks that a newest release without a build
// for this platform does not fail the update; the next one down is used.
func TestGitHubSourceListFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[
			{"tag_name":"v2.0.0","assets":[
				{"name":"gottyd_darwin_arm64","browser_download_url":"https://dl.example.com/v2.0.0"}]},
			{"tag_name":"v1.9.0","assets":[
				{"name":"gottyd_linux_mipsle","browser_download_url":"https://dl.example.com/v1.9.0"}]}]`)
	}))
	defer srv.Close()

	src := &GitHubSource{Owner: "lxxilyd", Repo: "gottyd", BaseURL: srv.URL + "/api/v1", Client: srv.Client()}
	rel, err := src.Latest(context.Background(), Request{OS: "linux", Arch: "mipsle", Channel: "beta"})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.9.0" || rel.URL != "https://dl.example.com/v1.9.0" {
		t.Fatalf("release = %+v, want the v1.9.0 fallback", rel)
	}

	if _, err := src.Latest(context.Background(), Request{OS: "plan9", Arch: "386", Channel: "beta"}); err == nil {
		t.Fatal("a request no release can satisfy was accepted")
	}
}

// TestGitHubSourcePrivateRelease covers the token plumbing: on GitHub a private
// asset is downloaded through the asset API, because the browser URL redirects
// to another host and loses the Authorization header on the way.
func TestGitHubSourcePrivateRelease(t *testing.T) {
	src := &GitHubSource{Owner: "lxxilyd", Repo: "gottyd", Token: "s3cret"}

	rel := src.release(ghRelease{TagName: "v1.2.3"}, &ghAsset{
		Name:               "gottyd_linux_amd64",
		BrowserDownloadURL: "https://github.com/lxxilyd/gottyd/releases/download/v1.2.3/gottyd_linux_amd64",
		URL:                "https://api.github.com/repos/lxxilyd/gottyd/releases/assets/42",
	})
	if rel.URL != "https://api.github.com/repos/lxxilyd/gottyd/releases/assets/42" {
		t.Fatalf("URL = %q, want the asset API", rel.URL)
	}
	if got := rel.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := rel.Header.Get("Accept"); got != "application/octet-stream" {
		t.Fatalf("Accept = %q", got)
	}

	// Gitea serves the file path itself, so the plain URL is kept there.
	src.BaseURL = "https://git.example.com/api/v1"
	rel = src.release(ghRelease{TagName: "v1.2.3"}, &ghAsset{
		BrowserDownloadURL: "https://git.example.com/lxxilyd/gottyd/releases/download/v1.2.3/gottyd_linux_amd64",
	})
	if !strings.HasPrefix(rel.URL, "https://git.example.com/lxxilyd/gottyd/releases/download/") {
		t.Fatalf("URL = %q, want the browser download URL", rel.URL)
	}
	if got := rel.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := rel.Header.Get("Accept"); got != "" {
		t.Fatalf("Accept = %q, want empty", got)
	}
}

func TestURLSourceVersionFromURL(t *testing.T) {
	cases := map[string]string{
		"https://git.example.com/lxxilyd/gottyd/releases/download/v2026.10.23-main-abc1234/gottyd_linux_amd64": "v2026.10.23-main-abc1234",
		"https://dl.example.com/1.4.2/app_linux": "1.4.2",
	}
	for url, want := range cases {
		rel, err := (&URLSource{URL: url}).Latest(context.Background(), Request{})
		if err != nil {
			t.Fatalf("Latest(%q): %v", url, err)
		}
		if rel.Version != want || rel.URL != url {
			t.Fatalf("Latest(%q) = %+v, want version %q", url, rel, want)
		}
	}

	if _, err := (&URLSource{URL: "https://dl.example.com/latest/gottyd"}).Latest(context.Background(), Request{}); err == nil {
		t.Fatal("URL without a version was accepted")
	}
	if _, err := (&URLSource{}).Latest(context.Background(), Request{}); err == nil {
		t.Fatal("empty URL was accepted")
	}

	rel, err := (&URLSource{URL: "https://dl.example.com/latest/gottyd", Version: "v9.9.9", SHA256: "sha256:AB12"}).
		Latest(context.Background(), Request{})
	if err != nil {
		t.Fatalf("explicit version: %v", err)
	}
	if rel.Version != "v9.9.9" || rel.SHA256 != "AB12" {
		t.Fatalf("explicit version release = %+v", rel)
	}
}
