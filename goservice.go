// Package goservice is the one call an application's main needs: it recognises
// "service <verb>" on the command line and manages the service (install,
// uninstall, start, stop, restart, status), or reports that the arguments are
// not a service command so the application carries on.
//
// It exists so that integrating a daemon does not mean pasting the same twenty
// lines of dispatch and argv handling into every main. It is a thin layer over
// the svc package: an application that wants more control can call svc.New and
// svc.Do directly.
//
//	func main() {
//		served, err := goservice.Handle(ctx, flag.Args(), goservice.Config{
//			Service: svc.Definition{Name: "gottyd", ExecPath: "/usr/local/bin/gottyd", Restart: true, AutoStart: true},
//			Run:     serve, // used when the Windows service manager starts us
//		})
//		if served {
//			exit(err)
//		}
//		// ... the daemon's own work
//	}
package goservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lxxilyd/goservice/svc"
)

// verbs are the service commands Handle accepts.
var verbs = []string{"install", "uninstall", "start", "stop", "restart", "status"}

// Config describes the daemon behind the "service" subcommand.
type Config struct {
	// Service is installed and managed. Name and ExecPath should be set
	// explicitly — see the note about ExecPath in the svc documentation.
	Service svc.Definition
	// Backend forces a specific service backend instead of detecting one, and
	// is passed to svc.NewWith. Empty means detection.
	Backend string

	// Strip names the flags that belong to the CLI only (-d, -logdir, ...) and
	// must not be copied into the installed command line. It only applies when
	// Service.Args is empty, in which case the arguments after the verb become
	// the service command line.
	Strip []string

	// Run is the daemon itself, and it matters on Windows: when the Service
	// Control Manager starts the process, the process has to register with the
	// SCM dispatcher instead of just executing its flags, or the SCM gives up
	// after ~30 seconds with "service did not respond in a timely fashion".
	// Handle takes care of that whenever Run is set. Elsewhere, and in a
	// console, it is never called by Handle — the caller runs the daemon its
	// own way.
	Run func(ctx context.Context) error

	// Out receives the progress and status lines. Defaults to os.Stdout.
	Out io.Writer
	// Usage overrides the message printed when "service" is given without a
	// verb.
	Usage string
}

// Handle runs a "service <verb>" command line and reports whether it did.
//
// A false return means args[0] is not "service" (or it was escaped with "--"),
// so the caller should continue with its normal argument handling. A true
// return means the command has run, and err is its outcome.
//
// When Config.Run is set it also covers the other way a process ends up being
// started: the Windows service manager launching it directly instead of a user
// typing "service start". That case is not a command line at all, which is why
// it cannot be folded into the verb dispatch — but it belongs to the same call,
// so main does not have to know about it.
func Handle(ctx context.Context, args []string, cfg Config) (handled bool, err error) {
	verb, rest, ok := svc.ServiceCommand(args)
	if !ok {
		if cfg.Run == nil {
			return false, nil
		}
		// Not a service command. On Windows this may be the SCM talking, in
		// which case the daemon has to serve here and report its status; in a
		// console (and everywhere else) it reports false and the caller carries
		// on with its normal path.
		return svc.RunServiceIfNeeded(cfg.Service.Name, cfg.Run)
	}

	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}

	switch {
	case verb == "":
		return true, errors.New(cfg.usage())
	case !knownVerb(verb):
		return true, fmt.Errorf("goservice: unknown service command %q, want one of: %s", verb, joinVerbs())
	}

	def := cfg.Service
	if len(def.Args) == 0 {
		// Nothing was pinned down, so the argument vector after the verb is the
		// command line the service should run.
		def.Args = svc.StripFlags(rest, cfg.Strip...)
	}

	manager, err := svc.NewWith(cfg.Backend, def)
	if err != nil {
		return true, err
	}
	return true, svc.Do(ctx, manager, verb, out)
}

func (c Config) usage() string {
	if c.Usage != "" {
		return c.Usage
	}
	return "usage: service " + joinVerbs()
}

func knownVerb(verb string) bool {
	for _, known := range verbs {
		if known == verb {
			return true
		}
	}
	return false
}

func joinVerbs() string {
	out := ""
	for i, verb := range verbs {
		if i > 0 {
			out += "|"
		}
		out += verb
	}
	return out
}
