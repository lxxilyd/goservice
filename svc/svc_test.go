package svc

import (
	"context"
	"strings"
	"testing"
)

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

// TestNewWithBackends checks that every backend this build advertises can be
// constructed by name, and that an unknown name fails with the alternatives.
func TestNewWithBackends(t *testing.T) {
	def := Definition{Name: "my-daemon", ExecPath: "/usr/local/bin/my-daemon"}

	names := Backends()
	for _, name := range names {
		manager, err := NewWith(name, def)
		if err != nil {
			t.Fatalf("NewWith(%q): %v", name, err)
		}
		if got := manager.Backend(); got != name {
			t.Errorf("NewWith(%q).Backend() = %q", name, got)
		}
	}

	if _, err := NewWith("definitely-not-a-backend", def); err == nil {
		t.Error("an unknown backend was accepted")
	} else if len(names) > 0 && !strings.Contains(err.Error(), names[0]) {
		t.Errorf("error %q does not list the available backends %v", err, names)
	}

	// An empty name keeps the detected backend.
	detected, err := NewWith("", def)
	if err != nil {
		t.Fatalf("NewWith(detected): %v", err)
	}
	if detected.Backend() == "" {
		t.Error("the detected backend has no name")
	}
}

// TestStripFlags is what turns os.Args into Definition.Args: the service-only
// flags disappear, the daemon's own flags and its command survive.
func TestStripFlags(t *testing.T) {
	cases := []struct {
		args  []string
		names []string
		want  []string
	}{
		{
			args:  []string{"-service=install", "-p", "8080", "-d", "bash", "-l"},
			names: []string{"service", "d"},
			want:  []string{"-p", "8080", "bash", "-l"},
		},
		{
			// Only the flag token goes: a detached value is not touched, which
			// is why -service=install is the documented form.
			args:  []string{"-service", "install"},
			names: []string{"service"},
			want:  []string{"install"},
		},
		{
			// Everything after "--" belongs to command.
			args:  []string{"-service=install", "--", "-d", "bash"},
			names: []string{"service", "d"},
			want:  []string{"--", "-d", "bash"},
		},
		{
			args:  []string{"-p", "8080", "bash"},
			names: nil,
			want:  []string{"-p", "8080", "bash"},
		},
	}

	for _, tc := range cases {
		got := StripFlags(tc.args, tc.names...)
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("StripFlags(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// fakeManager records the command Do dispatched.
type fakeManager struct {
	called string
	err    error
}

func (f *fakeManager) Backend() string        { return "fake" }
func (f *fakeManager) Definition() Definition { return Definition{} }
func (f *fakeManager) Install(context.Context) error {
	f.called = "install"
	return f.err
}
func (f *fakeManager) Uninstall(context.Context) error {
	f.called = "uninstall"
	return f.err
}
func (f *fakeManager) Start(context.Context) error {
	f.called = "start"
	return f.err
}
func (f *fakeManager) Stop(context.Context) error {
	f.called = "stop"
	return f.err
}
func (f *fakeManager) Restart(context.Context) error {
	f.called = "restart"
	return f.err
}
func (f *fakeManager) Status(context.Context) (Status, error) {
	f.called = "status"
	return Status{State: StateRunning, PID: 42, Detail: "unit"}, f.err
}

func TestDo(t *testing.T) {
	for _, command := range []string{"install", "uninstall", "start", "stop", "restart"} {
		manager := &fakeManager{}
		if err := Do(context.Background(), manager, command, nil); err != nil {
			t.Fatalf("Do(%q): %v", command, err)
		}
		if manager.called != command {
			t.Fatalf("Do(%q) called %q", command, manager.called)
		}
	}

	var out strings.Builder
	if err := Do(context.Background(), &fakeManager{}, "status", &out); err != nil {
		t.Fatalf("Do(status): %v", err)
	}
	if got := out.String(); got != "running pid=42 unit\n" {
		t.Fatalf("status line = %q", got)
	}

	if err := Do(context.Background(), &fakeManager{}, "nope", nil); err == nil {
		t.Fatal("an unknown service command was accepted")
	}
}

// TestServiceCommand covers the "<binary> service <verb>" shape: the verb comes
// out, the two words disappear, and "--" opts out so a command named "service"
// can still be run.
func TestServiceCommand(t *testing.T) {
	cases := []struct {
		args     []string
		wantVerb string
		wantRest []string
		wantOk   bool
	}{
		{[]string{"service", "install", "-p", "8080", "bash"}, "install", []string{"-p", "8080", "bash"}, true},
		{[]string{"service", "status"}, "status", []string{}, true},
		{[]string{"service"}, "", nil, true},
		{[]string{"bash", "-l"}, "", nil, false},
		{nil, "", nil, false},
		{[]string{"--", "service", "install"}, "", nil, false},
	}

	for _, tc := range cases {
		verb, rest, ok := ServiceCommand(tc.args)
		if verb != tc.wantVerb || ok != tc.wantOk || strings.Join(rest, " ") != strings.Join(tc.wantRest, " ") {
			t.Errorf("ServiceCommand(%v) = (%q, %v, %v), want (%q, %v, %v)",
				tc.args, verb, rest, ok, tc.wantVerb, tc.wantRest, tc.wantOk)
		}
	}
}
