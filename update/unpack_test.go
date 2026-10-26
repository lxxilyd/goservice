package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGz builds an in-memory .tar.gz with the given members.
func tarGz(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipFile writes a zip with the given members and returns its path.
func zipFile(t *testing.T, members map[string]string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for name, body := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestTarGzUnpack(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"app_1.2.3/README.md": "docs",
		"app_1.2.3/app":       "the binary",
	})
	rel := Release{URL: "https://dl.example.com/app_1.2.3_linux_amd64.tar.gz"}

	var out bytes.Buffer
	if err := TarGz("app")(rel, bytes.NewReader(archive), &out); err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	if out.String() != "the binary" {
		t.Fatalf("extracted %q", out.String())
	}

	// The full path wins over the base name.
	out.Reset()
	if err := TarGz("app_1.2.3/README.md")(rel, bytes.NewReader(archive), &out); err != nil {
		t.Fatalf("TarGz(exact): %v", err)
	}
	if out.String() != "docs" {
		t.Fatalf("extracted %q", out.String())
	}

	// A miss names the member and what the archive really holds.
	err := TarGz("missing")(rel, bytes.NewReader(archive), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no entry") || !strings.Contains(err.Error(), "app_1.2.3/app") {
		t.Fatalf("missing member error = %v", err)
	}

	// Not gzip at all.
	if err := TarGz("app")(rel, strings.NewReader("plain"), io.Discard); err == nil {
		t.Fatal("a non gzip artifact was accepted")
	}
}

func TestZipUnpack(t *testing.T) {
	file := zipFile(t, map[string]string{
		"docs/README.md": "docs",
		"bin/app.exe":    "windows binary",
	})
	rel := Release{URL: "https://dl.example.com/app_windows_amd64.zip"}

	var out bytes.Buffer
	if err := Zip("app.exe")(rel, file, &out); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	if out.String() != "windows binary" {
		t.Fatalf("extracted %q", out.String())
	}

	if err := Zip("nope")(rel, file, io.Discard); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("missing member error = %v", err)
	}

	// A non-seekable reader cannot be read as a zip; the error says why.
	if err := Zip("bin/app.exe")(rel, io.LimitReader(file, 1<<20), io.Discard); err == nil {
		t.Fatal("a non *os.File artifact was accepted")
	}
}

// TestApplyUnpackTarGz walks the whole archive path: download, checksum, unpack
// into the staging file and replace, with no leftovers on disk.
func TestApplyUnpackTarGz(t *testing.T) {
	binary := "extracted binary v2"
	archive := tarGz(t, map[string]string{
		"app_v2/LICENSE": "license",
		"app_v2/app":     binary,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "app")
	if err := os.WriteFile(target, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	updater := &Updater{
		Source:  &URLSource{URL: srv.URL + "/download/v2.0.0/app.tar.gz", Version: "v2.0.0"},
		Version: "v1.0.0",
		Target:  Target{Path: target, Client: srv.Client(), Unpack: TarGz("app")},
	}
	if _, err := updater.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil || string(data) != binary {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
	// Windows keeps the previous image as <target>.old for CleanupOldBinary;
	// everywhere else the directory holds nothing but the binary.
	for _, leftover := range []string{target + ".part", target + ".new"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind: %v", leftover, err)
		}
	}
}

func TestApplyMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 1024))
	}))
	defer srv.Close()

	dir := t.TempDir()
	updater := &Updater{
		Source:  &URLSource{URL: srv.URL + "/v2.0.0/app", Version: "v2.0.0"},
		Version: "v1.0.0",
		Target:  Target{Path: filepath.Join(dir, "app"), Client: srv.Client(), MaxBytes: 100},
	}
	if _, err := updater.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized artifact error = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("failed update left %d files behind", len(entries))
	}
}

func TestApplyVerifyGetsPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("payload"))
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "app")
	var seen string
	updater := &Updater{
		Source:  &URLSource{URL: srv.URL + "/v2.0.0/app", Version: "v2.0.0"},
		Version: "v1.0.0",
		Target: Target{
			Path:   target,
			Client: srv.Client(),
			Verify: func(rel Release, path string) error {
				seen = path
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if string(data) != "payload" {
					return io.ErrUnexpectedEOF
				}
				return nil
			},
		},
	}
	if _, err := updater.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.HasSuffix(seen, ".part") {
		t.Fatalf("Verify saw %q, want the staged artifact", seen)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("the artifact file was left behind: %v", err)
	}
}
