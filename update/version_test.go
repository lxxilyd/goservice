package update

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2025.8.1", "2025.8.1", 0},
		{"2025.8.1", "2025.8.2", -1},
		{"2025.9.0", "2025.8.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.3.0", 0},
		{"1.10.0", "1.9.9", 1},
		{"1.2.3-beta", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc1", 1},
		{"1.2.3+build5", "1.2.3", 0},
		{"dev", "1.0.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveRelative(t *testing.T) {
	got, ok := resolveRelative("https://example.com/api/feed.json", "/dl/app")
	if !ok || got != "https://example.com/dl/app" {
		t.Fatalf("resolveRelative = %q, %v", got, ok)
	}
	if _, ok := resolveRelative("not a url", "/dl"); ok {
		t.Fatal("expected failure for a non-URL base")
	}
}

func TestTokenizeVersion(t *testing.T) {
	got := tokenizeVersion("v2025.8.1-rc2+build7")
	want := []string{"2025", "8", "1", "rc2"}
	if len(got) != len(want) {
		t.Fatalf("tokenizeVersion = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenizeVersion = %v, want %v", got, want)
		}
	}
}
