// Package update implements self-update for a single binary: version check,
// download, checksum/signature verification and an atomic replacement that
// survives the process still running.
//
// The package knows nothing about where releases come from (Source) or how the
// process is supervised (Restart hook), so it can be reused by any daemon that
// is installed through the sibling svc package.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	// ErrUpToDate is returned by Update when the latest release is not newer.
	ErrUpToDate = errors.New("update: already up to date")
	// ErrChecksumMismatch is returned when the downloaded bytes do not match the
	// expected SHA-256.
	ErrChecksumMismatch = errors.New("update: checksum mismatch")
)

// Release describes one published build.
type Release struct {
	Version string
	// URL is the artifact to download. It may point anywhere: a release asset,
	// an object store, a signed CDN link.
	URL string
	// SHA256 is the hex encoded digest of the artifact. Empty disables the
	// built-in check (use Verify for signatures instead).
	SHA256 string
	Notes  string
	// Header is sent with the download request, for private repositories,
	// signed URLs or an object store that wants an extra header. Build it with
	// AuthHeader for the usual token cases. Entries here override
	// Updater.Header.
	Header http.Header
}

// AuthHeader builds a download Header carrying token. An empty name means
// "Authorization", where a bare token (no scheme yet) is sent as
// "Bearer <token>"; a custom name such as GitLab's "PRIVATE-TOKEN" gets the
// value untouched. It returns nil for an empty token, which leaves the
// download anonymous.
//
//	update.AuthHeader("", "glpat-...")               -> Authorization: Bearer glpat-...
//	update.AuthHeader("PRIVATE-TOKEN", "glpat-...")  -> PRIVATE-TOKEN: glpat-...
//	update.AuthHeader("", "token 0123abcd")          -> Authorization: token 0123abcd
func AuthHeader(name, token string) http.Header {
	if token == "" {
		return nil
	}
	if name == "" {
		name = "Authorization"
	}
	value := token
	if name == "Authorization" && !strings.Contains(token, " ") {
		value = "Bearer " + token
	}
	header := http.Header{}
	header.Set(name, value)
	return header
}

// Request is what a Source needs to pick the right artifact.
type Request struct {
	Current string
	Channel string
	OS      string
	Arch    string
}

// Source resolves the newest release for a request.
type Source interface {
	Latest(ctx context.Context, req Request) (Release, error)
}

// Result reports what happened.
type Result struct {
	Updated bool
	From    string
	To      string
}

// Updater ties a Source to a binary on disk.
type Updater struct {
	// Source is where new versions are discovered.
	Source Source
	// Version is the version currently running, used for the comparison.
	Version string
	// Channel is passed through to the Source ("stable", "beta", ...).
	Channel string
	// BinaryPath defaults to the running executable, symlinks resolved.
	BinaryPath string
	// Client defaults to a 10 minute timeout client.
	Client *http.Client
	// Header is added to every download request and is the place for a
	// credential that is the same for every artifact. Release.Header wins on
	// key conflicts, so a source that knows better can still override it.
	Header http.Header
	// Verify, when set, runs on the downloaded bytes before they are installed.
	// Use it for a signature check; SHA-256 is already handled via Release.
	Verify func(rel Release, data []byte) error
	// Restart, when set, is called after a successful replacement. Wire it to
	// svc.Manager.Restart so the new binary actually takes over.
	Restart func(ctx context.Context) error
	// Managed, when set and returning true, disables self-update (the binary is
	// owned by a package manager and must be upgraded through it).
	Managed func() bool
}

// Check reports the newest release and whether it is newer than the running
// version.
func (u *Updater) Check(ctx context.Context) (Release, bool, error) {
	if u.Source == nil {
		return Release{}, false, errors.New("update: Updater.Source is required")
	}
	if u.Managed != nil && u.Managed() {
		return Release{}, false, fmt.Errorf("update: binary is managed by a package manager")
	}

	rel, err := u.Source.Latest(ctx, Request{
		Current: u.Version,
		Channel: u.Channel,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	})
	if err != nil {
		return Release{}, false, err
	}
	return rel, Compare(rel.Version, u.Version) > 0, nil
}

// Update checks and, when a newer release exists, installs it.
func (u *Updater) Update(ctx context.Context) (Result, error) {
	rel, available, err := u.Check(ctx)
	if err != nil {
		return Result{}, err
	}
	if !available {
		return Result{From: u.Version, To: u.Version}, ErrUpToDate
	}
	return u.Apply(ctx, rel)
}

// Apply installs rel unconditionally (no version comparison).
func (u *Updater) Apply(ctx context.Context, rel Release) (Result, error) {
	if rel.URL == "" {
		return Result{}, errors.New("update: release has no download URL")
	}

	target, err := u.binaryPath()
	if err != nil {
		return Result{}, err
	}

	data, err := u.download(ctx, rel)
	if err != nil {
		return Result{}, err
	}
	if rel.SHA256 != "" {
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimPrefix(rel.SHA256, "sha256:")) {
			return Result{}, fmt.Errorf("%w: %s", ErrChecksumMismatch, rel.URL)
		}
	}
	if u.Verify != nil {
		if err := u.Verify(rel, data); err != nil {
			return Result{}, fmt.Errorf("update: verification failed: %w", err)
		}
	}

	staged := target + ".new"
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		return Result{}, fmt.Errorf("update: staging %s: %w", staged, err)
	}
	// writeFile honours the umask, be explicit about the mode.
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return Result{}, fmt.Errorf("update: chmod %s: %w", staged, err)
	}

	if err := replaceFile(staged, target); err != nil {
		_ = os.Remove(staged)
		return Result{}, fmt.Errorf("update: replacing %s: %w", target, err)
	}

	if u.Restart != nil {
		if err := u.Restart(ctx); err != nil {
			return Result{Updated: true, From: u.Version, To: rel.Version}, fmt.Errorf(
				"update: binary replaced but restart failed: %w", err)
		}
	}

	return Result{Updated: true, From: u.Version, To: rel.Version}, nil
}

func (u *Updater) binaryPath() (string, error) {
	if u.BinaryPath != "" {
		return u.BinaryPath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locating the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func (u *Updater) download(ctx context.Context, rel Release) ([]byte, error) {
	client := u.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("update: building request: %w", err)
	}
	for name, values := range u.Header {
		for _, value := range values {
			req.Header.Set(name, value)
		}
	}
	for name, values := range rel.Header {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Set(name, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", rel.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: downloading %s: unexpected status %s", rel.URL, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// CleanupOldBinary removes the .old file a Windows self-update leaves behind.
// Call it at startup; it is a no-op elsewhere.
func CleanupOldBinary(binaryPath string) error {
	if binaryPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil
		}
		binaryPath = exe
	}
	return cleanupOldBinary(binaryPath)
}
