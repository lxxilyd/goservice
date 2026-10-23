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
	if want := "/api/v1/repos/lxxilyd/gottyd/releases?limit=1"; seen[1] != want {
		t.Fatalf("list request = %q, want %q", seen[1], want)
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
