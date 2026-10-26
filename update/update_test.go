package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthHeader(t *testing.T) {
	if got := AuthHeader("", ""); got != nil {
		t.Fatalf("AuthHeader with no token = %v, want nil", got)
	}

	cases := []struct{ name, token, wantHeader, wantValue string }{
		{"", "abc", "Authorization", "Bearer abc"},
		{"", "token abc", "Authorization", "token abc"},
		{"PRIVATE-TOKEN", "glpat-1", "PRIVATE-TOKEN", "glpat-1"},
	}
	for _, tc := range cases {
		header := AuthHeader(tc.name, tc.token)
		if got := header.Get(tc.wantHeader); got != tc.wantValue {
			t.Fatalf("AuthHeader(%q, %q) = %q, want %q", tc.name, tc.token, got, tc.wantValue)
		}
	}
}

// TestApplyAuthenticatedDownload walks the whole path for the "any download
// URL" case: a URLSource with a token, an endpoint that refuses anonymous
// requests, and an atomic replace on disk.
func TestApplyAuthenticatedDownload(t *testing.T) {
	const payload = "brand new binary"

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "app")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	updater := &Updater{
		Source: &URLSource{
			URL:     srv.URL + "/releases/download/v2.0.0/app_linux_amd64",
			Version: "v2.0.0",
			Auth:    Auth{Token: "s3cret"},
		},
		Version: "v1.0.0",
		Target:  Target{Path: target, Client: srv.Client()},
	}

	result, err := updater.Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !result.Updated || result.From != "v1.0.0" || result.To != "v2.0.0" {
		t.Fatalf("result = %+v", result)
	}
	if len(seen) != 1 || seen[0] != "Bearer s3cret" {
		t.Fatalf("Authorization seen by the server = %v", seen)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != payload {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatalf("staging file was left behind: %v", err)
	}
}

// TestUpdaterAuthFallback covers the case where the credential is configured
// once on the Updater, for a custom Source that does not fill Release.Header.
func TestUpdaterAuthFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer from-updater" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Release") != "yes" {
			http.Error(w, "missing release header", http.StatusBadRequest)
			return
		}
		w.Write([]byte("bin"))
	}))
	defer srv.Close()

	source := staticReleaseSource{rel: Release{
		Version: "v2.0.0",
		URL:     srv.URL + "/app",
		Header:  http.Header{"X-Release": []string{"yes"}},
	}}

	updater := &Updater{
		Source:  source,
		Version: "v1.0.0",
		Auth:    Auth{Token: "from-updater"},
		Target:  Target{Path: filepath.Join(t.TempDir(), "app"), Client: srv.Client()},
	}
	if _, err := updater.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// staticReleaseSource stands in for a user supplied Source.
type staticReleaseSource struct{ rel Release }

func (s staticReleaseSource) Latest(context.Context, Request) (Release, error) { return s.rel, nil }

// TestStaticSourceAuthenticatesBothHops is the regression test for the gap where
// the feed credential covered the JSON document only, and the artifact download
// went out anonymous: a private artifact came back 401 with no hint why.
func TestStaticSourceAuthenticatesBothHops(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path+" "+r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/latest.json" {
			io.WriteString(w, `{"version":"v2.0.0","url":"/app"}`)
			return
		}
		w.Write([]byte("binary v2"))
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "app")
	updater := &Updater{
		Source:  &StaticSource{URL: srv.URL + "/latest.json", Auth: Auth{Token: "s3cret"}},
		Version: "v1.0.0",
		Target:  Target{Path: target, Client: srv.Client()},
	}
	if _, err := updater.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %v, want the feed and the artifact", requests)
	}
	for _, request := range requests {
		if !strings.HasSuffix(request, "Bearer s3cret") {
			t.Fatalf("request went out without the credential: %q", request)
		}
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "binary v2" {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
}
