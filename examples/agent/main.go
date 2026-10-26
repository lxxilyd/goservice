// Command agent is a self-contained example of the goservice packages: a small
// HTTP daemon that can install itself as a system service and update itself.
//
//	agent -addr 127.0.0.1:8080                     run in the foreground
//	agent -addr 127.0.0.1:8080 service install     install + start as a service
//	agent service status | start | stop | restart
//	agent service uninstall
//	agent backends                                 list the backends this build has
//
// The update source is configured once, then used by "service update":
//
//	agent -repo you/mydaemon service update                                   # GitHub
//	agent -repo you/mydaemon -base-url https://git.example.com/api/v1 service update
//	agent -gitlab group/app -base-url https://gitlab.example.com service update
//	agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 service update
//	agent -repo you/mydaemon -unpack app service update                       # tar.gz/zip
//	agent -backend sysv service install                                       # force a backend
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lxxilyd/goservice/daemon"
	"github.com/lxxilyd/goservice/svc"
	"github.com/lxxilyd/goservice/update"
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
	// A Windows self-update renames the running binary to <exe>.old and cannot
	// delete it until the process exits, so the leftover is cleaned up here at
	// startup. A no-op elsewhere.
	_ = update.CleanupOldBinary("")

	name := flag.String("name", "example-agent", "service name")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	backend := flag.String("backend", "", "force a service backend instead of detecting one (see svc.Backends)")
	repo := flag.String("repo", "", "owner/repo used by service update")
	gitlab := flag.String("gitlab", "", "GitLab project path or ID used by service update")
	baseURL := flag.String("base-url", "", "API root of a self-hosted git host, e.g. https://git.example.com/api/v1")
	url := flag.String("url", "", "download one artifact directly, bypassing any release API")
	remote := flag.String("remote-version", "", "version of -url when it cannot be read from the link")
	token := flag.String("token", "", "token for a private repository or an authenticated download")
	unpack := flag.String("unpack", "", "member to extract from a tar.gz/zip artifact before installing")
	maxBytes := flag.Int64("max-bytes", 0, "cap the artifact size in bytes (0 default, negative unlimited)")
	logDir := flag.String("logdir", "", "directory for stdout/stderr on init-script backends")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] service install|uninstall|start|stop|restart|status|update\n", os.Args[0])
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

	source, err := updateSource(updateFlags{
		repo:    *repo,
		gitlab:  *gitlab,
		baseURL: *baseURL,
		url:     *url,
		version: *remote,
		token:   *token,
	})
	if err != nil {
		return err
	}

	// The whole service surface in one call: "service install", "service
	// status", "service update" and the rest. Anything that is not a service
	// command falls through, and the daemon handles its own arguments below.
	handled, err := daemon.Handle(ctx, flag.Args(), daemon.Config{
		Backend: *backend,
		Service: def,
		Source:  source,
		Version: version,
		Auth:    update.Auth{Token: *token},
		Target: update.Target{
			Unpack:   unpackByURL(*unpack),
			MaxBytes: *maxBytes,
		},
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

// updateFlags are the flags that describe where releases come from.
type updateFlags struct {
	repo    string
	gitlab  string
	baseURL string
	url     string
	version string
	token   string
}

// updateSource builds the source those flags describe: a single download URL, a
// GitLab project, or a GitHub-compatible release API. It returns nil when
// nothing was configured, which is what turns "service update" into an error
// instead of a silent no-op.
func updateSource(f updateFlags) (update.Source, error) {
	auth := update.Auth{Token: f.token}
	configured := 0
	for _, set := range []bool{f.repo != "", f.gitlab != "", f.url != ""} {
		if set {
			configured++
		}
	}
	if configured > 1 {
		return nil, errors.New("use only one of -repo, -gitlab or -url")
	}

	switch {
	case f.url != "":
		return &update.URLSource{URL: f.url, Version: f.version, Auth: auth}, nil
	case f.gitlab != "":
		return &update.GitLabSource{Project: f.gitlab, BaseURL: f.baseURL, Auth: auth}, nil
	case f.repo != "":
		parts := strings.SplitN(f.repo, "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("-repo must look like owner/repo, got %q", f.repo)
		}
		return &update.GitHubSource{
			Owner:   parts[0],
			Repo:    parts[1],
			BaseURL: f.baseURL,
			Auth:    auth,
		}, nil
	}
	return nil, nil
}

// unpackByURL builds the extraction hook. The choice between zip and tar.gz is
// made per release, from Release.URL, because a Source only knows the artifact
// URL once it has resolved one.
func unpackByURL(member string) update.UnpackFunc {
	if member == "" {
		return nil
	}
	return func(rel update.Release, artifact io.Reader, w io.Writer) error {
		artifactURL := strings.ToLower(strings.SplitN(rel.URL, "?", 2)[0])
		if strings.HasSuffix(artifactURL, ".zip") {
			return update.Zip(member)(rel, artifact, w)
		}
		return update.TarGz(member)(rel, artifact, w)
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
