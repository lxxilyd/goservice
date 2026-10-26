// Package goservice is the one call an application's main needs: it recognises
// "service <verb>" on the command line and does the right thing (install,
// uninstall, start, stop, restart, status or self-update), or reports that the
// arguments are not a service command so the application carries on.
//
// It exists so that integrating a daemon does not mean pasting the same twenty
// lines of dispatch, argv handling and restart plumbing into every main. It
// depends on both svc and update; an application that wants only one of them
// should use that package directly, since importing this one pulls both.
//
//	func main() {
//		handled, err := goservice.Handle(ctx, flag.Args(), goservice.Config{
//			Service: svc.Definition{Name: "gottyd", ExecPath: "/usr/local/bin/gottyd", Restart: true, AutoStart: true},
//			Source:  &update.GitHubSource{Owner: "lxxilyd", Repo: "gottyd"},
//			Version: Version,
//		})
//		if handled {
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
	"github.com/lxxilyd/goservice/update"
)

// verbs are the service commands Handle accepts.
var verbs = []string{"install", "uninstall", "start", "stop", "restart", "status", "update"}

// Config describes the daemon behind the "service" subcommand.
type Config struct {
	// Service is installed and managed. Name and ExecPath should be set
	// explicitly — see the note about ExecPath in the svc documentation.
	Service svc.Definition
	// Backend forces a specific service backend instead of detecting one, and
	// is passed to svc.NewWith. Empty means detection.
	Backend string

	// Source enables "service update". Nil means that verb is not offered.
	Source  update.Source
	Version string
	Channel string
	Auth    update.Auth
	// Target says how the artifact is verified and unpacked. Path defaults to
	// Service.ExecPath, which is the value an update must replace.
	Target update.Target

	// Strip names the flags that belong to the CLI only (-d, -logdir, ...) and
	// must not be copied into the installed command line. It only applies when
	// Service.Args is empty, in which case the arguments after the verb become
	// the service command line.
	Strip []string

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
func Handle(ctx context.Context, args []string, cfg Config) (handled bool, err error) {
	verb, rest, ok := svc.ServiceCommand(args)
	if !ok {
		return false, nil
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
	case verb == "update" && cfg.Source == nil:
		return true, errors.New(`goservice: "service update" needs Config.Source`)
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
	if verb != "update" {
		return true, svc.Do(ctx, manager, verb, out)
	}

	target := cfg.Target
	if target.Path == "" {
		target.Path = def.ExecPath
	}
	updater := &update.Updater{
		Source:  cfg.Source,
		Version: cfg.Version,
		Channel: cfg.Channel,
		Auth:    cfg.Auth,
		Target:  target,
		Restart: func(ctx context.Context) error {
			// Updating a binary that is not installed as a service is a normal
			// case, not a failure to restart something.
			if err := manager.Restart(ctx); err != nil && !errors.Is(err, svc.ErrNotInstalled) {
				return err
			}
			return nil
		},
	}

	result, err := updater.Update(ctx)
	switch {
	case errors.Is(err, update.ErrUpToDate):
		fmt.Fprintf(out, "already up to date (%s)\n", cfg.Version)
		return true, nil
	case err != nil:
		return true, err
	}
	fmt.Fprintf(out, "updated %s -> %s\n", result.From, result.To)
	return true, nil
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
