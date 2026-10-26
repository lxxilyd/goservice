// Command agent is a self-contained example of the goservice packages: a small
// HTTP daemon that can install itself as a system service.
//
//	agent -addr 127.0.0.1:8080                     run in the foreground
//	agent -addr 127.0.0.1:8080 service install     install + start as a service
//	agent service status | start | stop | restart
//	agent service uninstall
//	agent -backend sysv service install            write a specific backend's files
//	agent backends                                 list the backends this build has
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
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
		//   agent service install -addr 127.0.0.1:8080 run
		// installs "-addr 127.0.0.1:8080 run". Config.Strip lists the flags that
		// must not be copied in that case.
		Args:         []string{"-name", *name, "-addr", *addr, "run"},
		LogDir:       *logDir,
		Restart:      true,
		RestartSec:   5 * time.Second,
		AutoStart:    true,
		Dependencies: []string{"network-online.target"},
		// serve() calls svc.NotifyReady, so the unit has to be Type=notify for
		// that to mean anything.
		Notify: true,
	}

	// On Windows the process may have been started by the Service Control
	// Manager. In that case serve and return; on other platforms this is false.
	if handled, err := svc.RunServiceIfNeeded(*name, func(ctx context.Context) error {
		return serve(ctx, *addr)
	}); handled {
		return err
	}

	// The whole service surface in one call: "service install", "service
	// status" and the rest. Anything that is not a service command falls
	// through, and the daemon handles its own arguments below.
	handled, err := goservice.Handle(ctx, flag.Args(), goservice.Config{
		Backend: *backend,
		Service: def,
	})
	if handled {
		return err
	}

	switch command := flag.Arg(0); command {
	case "", "run":
		return serve(ctx, *addr)
	case "backends":
		fmt.Println(strings.Join(svc.Backends(), "\n"))
		return nil
	default:
		return fmt.Errorf("unknown command %q, service commands look like %q", command, "service install")
	}
}

func serve(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "example-agent %s\n", version)
	})

	server := &http.Server{Handler: mux}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// Tell systemd (Type=notify) that we are serving.
	_ = svc.NotifyReady()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		_ = svc.NotifyStopping()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
