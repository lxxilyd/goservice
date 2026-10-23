package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			Token:   "s3cret",
			Version: "v2.0.0",
		},
		Version:    "v1.0.0",
		BinaryPath: target,
		Client:     srv.Client(),
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

// TestUpdaterHeaderFallback covers the case where the credential is configured
// once on the Updater, and that a Release level header wins over it.
func TestUpdaterHeaderFallback(t *testing.T) {
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
		Source:     source,
		Version:    "v1.0.0",
		BinaryPath: filepath.Join(t.TempDir(), "app"),
		Client:     srv.Client(),
		Header:     AuthHeader("", "from-updater"),
	}
	if _, err := updater.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// staticReleaseSource stands in for a user supplied Source.
type staticReleaseSource struct{ rel Release }

func (s staticReleaseSource) Latest(context.Context, Request) (Release, error) { return s.rel, nil }
