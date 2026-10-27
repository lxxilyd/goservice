// Command agent is a self-contained example of the goservice packages: a small
// daemon that installs itself as a system service. It deliberately does nothing
// useful — just a heartbeat until it is stopped — so that the whole file is
// about wiring a service, and it links no third party code at all.
//
//	agent                                 run in the foreground
//	agent service install                 install + start as a service
//	agent service status | start | stop | restart
//	agent service uninstall
//	agent -backend sysv service install   write a specific backend's files
//	agent backends                        list the backends this build has
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lxxilyd/goservice"
	"github.com/lxxilyd/goservice/svc"
)

// version is injected at build time:
//
//	go build -ldflags "-X main.version=1.2.3" ./examples/agent
var version = "dev"

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func realMain() error {
	name := flag.String("name", "example-agent", "service name")
	backend := flag.String("backend", "", "force a service backend instead of detecting one (see svc.Backends)")
	logDir := flag.String("logdir", "", "directory for stdout/stderr on init-script backends")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] service install|uninstall|start|stop|restart|status\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "       %s [flags] run|backends\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	def := svc.Definition{
		Name:        *name,
		DisplayName: "Example Agent",
		Description: "goservice example daemon",
		// The service manager runs exactly this command line, so the subcommand
		// has to be part of the arguments.
		//
		// Leaving Args empty is the other option: then the arguments after
		// "service <verb>" become the command line, e.g.
		//   agent service install run
		// installs "run". Config.Strip lists the flags that must not be copied
		// in that case.
		Args:         []string{"-name", *name, "run"},
		LogDir:       *logDir,
		Restart:      true,
		RestartSec:   5 * time.Second,
		AutoStart:    true,
		Dependencies: []string{"network-online.target"},
		// work() calls svc.NotifyReady, so the unit has to be Type=notify for
		// that to mean anything.
		Notify: true,
	}

	// One call covers both ways this process can start: "service install" and
	// the other verbs on the command line, or the Windows service manager
	// launching the binary directly (that is what Run is for). Anything else
	// falls through to the daemon below.
	handled, err := goservice.Handle(ctx, flag.Args(), goservice.Config{
		Backend: *backend,
		Service: def,
		Run:     work,
	})
	if handled {
		return err
	}

	switch command := flag.Arg(0); command {
	case "", "run":
		return work(ctx)
	case "backends":
		fmt.Println(strings.Join(svc.Backends(), "\n"))
		return nil
	default:
		return fmt.Errorf("unknown command %q, service commands look like %q", command, "service install")
	}
}

// work is the daemon: a heartbeat until the context is cancelled. Everything
// interesting about running as a service is the lifecycle around it — telling
// systemd we are up, and stopping cleanly when the context ends.
func work(ctx context.Context) error {
	log.Printf("example-agent %s started (pid %d)", version, os.Getpid())

	// Tell systemd (Type=notify) that we are serving.
	_ = svc.NotifyReady()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = svc.NotifyStopping()
			log.Println("shutting down")
			return nil
		case <-ticker.C:
			log.Printf("example-agent %s heartbeat", version)
		}
	}
}
