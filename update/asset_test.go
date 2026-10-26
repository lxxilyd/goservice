package update

import "testing"

// TestAssetMatch covers the naming conventions real releases use, and the one
// case where a loose match would be dangerous: the pattern for GOARCH=arm must
// never pick the arm64 asset.
func TestAssetMatch(t *testing.T) {
	cases := []struct {
		name, expected string
		want           int
	}{
		// Exact, and the same file behind separators and extensions.
		{"gottyd_linux_amd64", "gottyd_linux_amd64", assetExact},
		{"gottyd-windows-amd64.exe", "gottyd_windows_amd64", assetExact},
		{"Gottyd-Windows-amd64.exe", "gottyd_windows_amd64", assetExact},
		{"gottyd_linux_amd64.tar.gz", "gottyd_linux_amd64", assetExact},
		{"mydaemon_linux_amd64.zip", "mydaemon_linux_amd64", assetExact},

		// Containment at a boundary is still allowed, for names that carry a
		// version or a build tag around the pattern.
		{"gottyd_1.2.3_linux_amd64", "1.2.3_linux_amd64", assetLoose},
		{"mydaemon_linux_amd64_debug", "mydaemon_linux_amd64", assetLoose},

		// The dangerous one: arm against arm64 / armv7, and amd64 against 386.
		{"gottyd_linux_arm64", "gottyd_linux_arm", assetNone},
		{"gottyd_linux_armv7", "gottyd_linux_arm", assetNone},
		{"gottyd_linux_amd64", "gottyd_linux_386", assetNone},
		{"gottyd_windows_amd64.exe", "gottyd_windows_arm64", assetNone},

		// Other platforms, and empty input.
		{"gottyd_darwin_arm64", "gottyd_darwin_amd64", assetNone},
		{"", "gottyd_linux_amd64", assetNone},
		{"gottyd_linux_amd64", "", assetNone},
	}

	for _, tc := range cases {
		if got := assetMatch(tc.name, tc.expected); got != tc.want {
			t.Errorf("assetMatch(%q, %q) = %d, want %d", tc.name, tc.expected, got, tc.want)
		}
	}
}

// TestPickAssetPrefersExact keeps the selection order stable: an exact match
// later in the list beats a loose match earlier in it.
func TestPickAssetPrefersExact(t *testing.T) {
	assets := []ghAsset{
		{Name: "gottyd_linux_amd64_debug"},
		{Name: "gottyd-linux-amd64"},
	}
	got := pickAsset(assets, "gottyd_linux_amd64")
	if got == nil || got.Name != "gottyd-linux-amd64" {
		t.Fatalf("picked %v, want the exact match", got)
	}
}
