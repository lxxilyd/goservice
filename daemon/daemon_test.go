package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/lxxilyd/goservice/svc"
	"github.com/lxxilyd/goservice/update"
)

// TestHandleIgnoresOtherCommands is the contract the caller depends on: anything
// that is not "service <verb>" must fall through untouched.
func TestHandleIgnoresOtherCommands(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bash", "-l"},
		{"backends"},
		{"--", "service", "install"},
	} {
		handled, err := Handle(context.Background(), args, Config{})
		if handled || err != nil {
			t.Fatalf("Handle(%v) = (%v, %v), want (false, nil)", args, handled, err)
		}
	}
}

// TestHandleUsageAndUnknownVerb covers the errors that never touch a backend.
func TestHandleUsageAndUnknownVerb(t *testing.T) {
	handled, err := Handle(context.Background(), []string{"service"}, Config{})
	if !handled || err == nil {
		t.Fatalf("bare service = (%v, %v), want an error", handled, err)
	}
	if !strings.Contains(err.Error(), "install") || !strings.Contains(err.Error(), "update") {
		t.Fatalf("usage error = %q, want the verb list", err)
	}

	handled, err = Handle(context.Background(), []string{"service"}, Config{Usage: "usage: gottyd service <verb>"})
	if !handled || err == nil || err.Error() != "usage: gottyd service <verb>" {
		t.Fatalf("custom usage = (%v, %v)", handled, err)
	}

	handled, err = Handle(context.Background(), []string{"service", "installl"}, Config{})
	if !handled || err == nil || !strings.Contains(err.Error(), "installl") {
		t.Fatalf("unknown verb = (%v, %v)", handled, err)
	}
}

// TestHandleUpdateNeedsSource keeps the failure readable instead of letting it
// surface as a nil pointer later.
func TestHandleUpdateNeedsSource(t *testing.T) {
	handled, err := Handle(context.Background(), []string{"service", "update"}, Config{})
	if !handled || err == nil || !strings.Contains(err.Error(), "Config.Source") {
		t.Fatalf("update without a source = (%v, %v)", handled, err)
	}
}

// TestHandleWritesToOut proves the status output goes where the caller asked,
// using a source that reports the running version so the update stops before it
// downloads or touches any backend state.
func TestHandleWritesToOut(t *testing.T) {
	var out strings.Builder
	cfg := Config{
		Service: svc.Definition{Name: "testdaemon", ExecPath: "/bin/true"},
		Source:  upToDateSource{},
		Version: "v1.0.0",
		Out:     &out,
	}
	handled, err := Handle(context.Background(), []string{"service", "update"}, cfg)
	if !handled || err != nil {
		t.Fatalf("Handle = (%v, %v)", handled, err)
	}
	if got := out.String(); got != "already up to date (v1.0.0)\n" {
		t.Fatalf("output = %q", got)
	}
}

// upToDateSource reports the running version, so Update stops at ErrUpToDate
// before it downloads or replaces anything.
type upToDateSource struct{}

func (upToDateSource) Latest(context.Context, update.Request) (update.Release, error) {
	return update.Release{Version: "v1.0.0"}, nil
}
