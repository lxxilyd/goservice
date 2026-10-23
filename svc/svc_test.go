package svc

import "testing"

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":           "''",
		"plain":      "'plain'",
		"with space": "'with space'",
		"it's":       `'it'\''s'`,
		"a$b`c":      `'a$b` + "`" + `c'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSystemdQuote(t *testing.T) {
	cases := map[string]string{
		"simple":          "simple",
		"/usr/bin/daemon": "/usr/bin/daemon",
		"with space":      `"with space"`,
		`say "hi"`:        `"say \"hi\""`,
		"":                `""`,
	}
	for in, want := range cases {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestXMLEscape(t *testing.T) {
	got := xmlEscape(`a & b <c> "d" 'e'`)
	want := "a &amp; b &lt;c&gt; &quot;d&quot; &apos;e&apos;"
	if got != want {
		t.Errorf("xmlEscape = %q, want %q", got, want)
	}
}

func TestArgLine(t *testing.T) {
	got := argLine([]string{"run", "--token", "a b", "it's"})
	want := `'run' '--token' 'a b' 'it'\''s'`
	if got != want {
		t.Errorf("argLine = %q, want %q", got, want)
	}
}

func TestParsePID(t *testing.T) {
	cases := map[string]int{
		"1234\n":        1234,
		"  42  ":        42,
		"pid: 777":      777,
		"":              0,
		"not a number ": 0,
	}
	for in, want := range cases {
		if got := parsePID(in); got != want {
			t.Errorf("parsePID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeDefaults(t *testing.T) {
	def := Definition{Name: "my-daemon"}
	if err := def.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if def.ExecPath == "" {
		t.Error("ExecPath should default to the running executable")
	}
	if def.DisplayName != "my-daemon" {
		t.Errorf("DisplayName = %q, want my-daemon", def.DisplayName)
	}
	if def.RestartSec == 0 {
		t.Error("RestartSec should get a default")
	}

	bad := Definition{Name: "with space"}
	if err := bad.normalize(); err == nil {
		t.Error("expected an error for a name with a space")
	}

	empty := Definition{}
	if err := empty.normalize(); err == nil {
		t.Error("expected an error for an empty name")
	}
}
