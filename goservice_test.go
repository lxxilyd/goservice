package goservice

import (
	"context"
	"strings"
	"testing"

	"github.com/lxxilyd/goservice/svc"
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
	if !strings.Contains(err.Error(), "install") || !strings.Contains(err.Error(), "status") {
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

	// Self-update is not part of this module any more.
	handled, err = Handle(context.Background(), []string{"service", "update"}, Config{})
	if !handled || err == nil || !strings.Contains(err.Error(), "update") {
		t.Fatalf("removed update verb = (%v, %v)", handled, err)
	}
}

// TestHandleLeavesTheDaemonToTheCaller: outside the Windows service manager the
// daemon is the caller's business. Handle must report "not a service command"
// and leave Run alone, or main would never reach its own foreground path.
func TestHandleLeavesTheDaemonToTheCaller(t *testing.T) {
	called := false
	cfg := Config{
		Service: svc.Definition{Name: "testdaemon", ExecPath: "/bin/true"},
		Run:     func(context.Context) error { called = true; return nil },
	}

	handled, err := Handle(context.Background(), []string{"run"}, cfg)
	if handled || err != nil {
		t.Fatalf("Handle(run) = (%v, %v), want (false, nil)", handled, err)
	}
	if called {
		t.Fatal("Run was called outside the service manager")
	}
}
