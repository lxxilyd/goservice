package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabSourceEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                                  "https://gitlab.com/api/v4",
		"https://git.example.com":           "https://git.example.com/api/v4",
		"https://git.example.com/":          "https://git.example.com/api/v4",
		"https://git.example.com/api/v4/":   "https://git.example.com/api/v4",
		"https://example.com/gitlab/api/v4": "https://example.com/gitlab/api/v4",
	}
	for base, want := range cases {
		if got := (&GitLabSource{BaseURL: base}).endpoint(); got != want {
			t.Fatalf("endpoint(%q) = %q, want %q", base, got, want)
		}
	}

	// A bare token goes in GitLab's own header, an OAuth2 token in
	// Authorization — the Auth type carries that rule.
	if got := (Auth{Token: "glpat-1"}).HeaderFor("PRIVATE-TOKEN").Get("PRIVATE-TOKEN"); got != "glpat-1" {
		t.Fatalf("default token header = %q", got)
	}
	if got := (Auth{Token: "oauth", Header: "Authorization"}).HeaderFor("PRIVATE-TOKEN").Get("Authorization"); got != "Bearer oauth" {
		t.Fatalf("Authorization override = %q", got)
	}
}

func TestEscapeProject(t *testing.T) {
	cases := map[string]string{
		"group/gottyd":     "group%2Fgottyd",
		"group/sub/gottyd": "group%2Fsub%2Fgottyd",
		"42":               "42",
		"group%2Fgottyd":   "group%2Fgottyd", // already encoded
		"group%2fgottyd":   "group%2fgottyd",
	}
	for in, want := range cases {
		if got := escapeProject(in); got != want {
			t.Fatalf("escapeProject(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGitLabSourceLatest drives the source against a server that speaks the
// GitLab release API, checking the encoded project path and the token plumbing.
func TestGitLabSourceLatest(t *testing.T) {
	var seen []string
	var token []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.RequestURI)
		token = append(token, r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			io.WriteString(w, `{"tag_name":"v1.2.3","description":"notes","assets":{"count":2,"links":[
				{"name":"gottyd_darwin_arm64","url":"https://dl.example.com/darwin"},
				{"name":"gottyd_linux_amd64","url":"https://dl.example.com/linux",
				 "direct_asset_url":"https://git.example.com/group/gottyd/-/releases/v1.2.3/downloads/gottyd_linux_amd64"}]}}`)
			return
		}
		io.WriteString(w, `[
			{"tag_name":"v1.4.0","assets":{"links":[
				{"name":"gottyd_darwin_arm64","url":"https://dl.example.com/v1.4.0/darwin"}]}},
			{"tag_name":"v1.3.0","assets":{"links":[
				{"name":"gottyd_linux_amd64","url":"https://dl.example.com/v1.3.0/linux"}]}}]`)
	}))
	defer srv.Close()

	src := &GitLabSource{
		Project: "group/gottyd",
		BaseURL: srv.URL,
		Auth:    Auth{Token: "glpat-1"},
		Client:  srv.Client(),
	}

	rel, err := src.Latest(context.Background(), Request{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.2.3" || rel.Notes != "notes" {
		t.Fatalf("release = %+v", rel)
	}
	// direct_asset_url wins over the raw url: it is the path GitLab signs for
	// a private project.
	if want := "https://git.example.com/group/gottyd/-/releases/v1.2.3/downloads/gottyd_linux_amd64"; rel.URL != want {
		t.Fatalf("URL = %q, want %q", rel.URL, want)
	}
	if got := rel.Header.Get("PRIVATE-TOKEN"); got != "glpat-1" {
		t.Fatalf("download PRIVATE-TOKEN = %q", got)
	}
	if want := "/api/v4/projects/group%2Fgottyd/releases/latest"; seen[0] != want {
		t.Fatalf("request = %q, want %q", seen[0], want)
	}
	if token[0] != "glpat-1" {
		t.Fatalf("API PRIVATE-TOKEN = %q", token[0])
	}

	// List mode: the newest release has no build for this platform, so the
	// next one down is used.
	src.IncludePrerelease = true
	rel, err = src.Latest(context.Background(), Request{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Latest(list): %v", err)
	}
	if rel.Version != "v1.3.0" || rel.URL != "https://dl.example.com/v1.3.0/linux" {
		t.Fatalf("list release = %+v", rel)
	}
	if want := "/api/v4/projects/group%2Fgottyd/releases?per_page=20"; seen[1] != want {
		t.Fatalf("list request = %q, want %q", seen[1], want)
	}

	if _, err := src.Latest(context.Background(), Request{OS: "plan9", Arch: "386"}); err == nil {
		t.Fatal("a request no release can satisfy was accepted")
	}
	if _, err := (&GitLabSource{}).Latest(context.Background(), Request{}); err == nil {
		t.Fatal("an empty Project was accepted")
	}
}

func TestGitLabSourceAssetPattern(t *testing.T) {
	// A numeric project ID has no name to build the default pattern from.
	src := &GitLabSource{Project: "42", AssetPattern: func(req Request) string {
		return "app-" + req.OS + "-" + req.Arch
	}}
	if got := src.assetName(Request{OS: "linux", Arch: "arm64"}); got != "app-linux-arm64" {
		t.Fatalf("assetName = %q", got)
	}
	if got := (&GitLabSource{Project: "group/gottyd"}).assetName(Request{OS: "linux", Arch: "amd64"}); got != "gottyd_linux_amd64" {
		t.Fatalf("default assetName = %q", got)
	}
}
