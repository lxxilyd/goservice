package update

import "strings"

// How well an asset name matched the expected one.
const (
	assetNone = iota
	assetLoose
	assetExact
)

// assetExtensions are stripped from an asset name before it is compared with the
// expected one, so a release that ships "gottyd_windows_amd64.exe" still
// satisfies the default "<repo>_<os>_<arch>" pattern. The list is matched
// longest first so ".tar.gz" wins over ".gz".
var assetExtensions = []string{
	".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst", ".tar",
	".tgz", ".zip", ".7z", ".gz", ".xz", ".bz2", ".zst",
	".exe", ".bin", ".msi", ".deb", ".rpm", ".apk",
}

// normaliseAsset folds an asset name down to its shape: separators become "_",
// the archive/exec extension goes away and the case is ignored, so
// "Gottyd-Windows-amd64.exe" and "gottyd_windows_amd64" describe the same file.
func normaliseAsset(name string) string {
	out := strings.ReplaceAll(name, "-", "_")
	lower := strings.ToLower(out)
	for _, ext := range assetExtensions {
		if strings.HasSuffix(lower, ext) {
			out = out[:len(out)-len(ext)]
			break
		}
	}
	return strings.ToLower(out)
}

// assetMatch reports how an asset name relates to the expected one.
//
// It is deliberately not a plain strings.Contains: the pattern
// "mydaemon_linux_arm" must never select "mydaemon_linux_arm64", which would
// hand an ARMv5 device a binary it cannot run. A containment match is only
// accepted at a name boundary, and the separator and extension differences are
// ignored so that the common "gottyd-windows-amd64.exe" naming works.
func assetMatch(name, expected string) int {
	if name == "" || expected == "" {
		return assetNone
	}
	if name == expected {
		return assetExact
	}
	got, want := normaliseAsset(name), normaliseAsset(expected)
	if got == want {
		return assetExact
	}
	at := strings.Index(got, want)
	if at < 0 {
		return assetNone
	}
	if at > 0 && !isAssetBoundary(got[at-1]) {
		return assetNone
	}
	if end := at + len(want); end < len(got) && !isAssetBoundary(got[end]) {
		return assetNone
	}
	return assetLoose
}

func isAssetBoundary(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c == '+'
}
