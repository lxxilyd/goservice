// Command agent is a self-contained example of the goservice packages: a small
// HTTP daemon that can install itself as a system service and update itself.
//
//	agent -addr 127.0.0.1:8080                     run in the foreground
//	agent -addr 127.0.0.1:8080 install             install + start as a service
//	agent status | start | stop | restart
//	agent uninstall
//	agent -backend sysv install                    write a specific backend's files
//	agent -repo owner/repo update                  self-update from GitHub
//	agent -repo owner/repo -base-url https://git.example.com/api/v1 -token T update
//	agent -gitlab group/app -base-url https://gitlab.example.com -token T update
//	agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 update
//	agent -repo owner/repo -unpack app update      install the "app" member of a tar.gz/zip
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
	repo := flag.String("repo", "", "owner/repo used by the update command")
	gitlab := flag.String("gitlab", "", "GitLab project path or ID used by the update command")
	baseURL := flag.String("base-url", "", "API root of a self-hosted git host, e.g. https://git.example.com/api/v1")
	url := flag.String("url", "", "download one artifact directly, bypassing any release API")
	remote := flag.String("remote-version", "", "version of -url when it cannot be read from the link")
	token := flag.String("token", "", "token for a private repository or an authenticated download")
	unpack := flag.String("unpack", "", "member to extract from a tar.gz/zip artifact before installing")
	maxBytes := flag.Int64("max-bytes", 0, "cap the artifact size in bytes (0 default, negative unlimited)")
	logDir := flag.String("logdir", "", "directory for stdout/stderr on init-script backends")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] [run|install|uninstall|start|stop|restart|status|update|backends]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := flag.Arg(0)
	if command == "" {
		command = "run"
	}

	def := svc.Definition{
		Name:        *name,
		DisplayName: "Example Agent",
		Description: "goservice example daemon",
		// The service manager runs exactly this command line, so the subcommand
		// has to be part of the arguments.
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

	switch command {
	case "run":
		return serve(ctx, *addr)

	case "install", "uninstall", "start", "stop", "restart", "status":
		manager, err := svc.NewWith(*backend, def)
		if err != nil {
			return err
		}
		fmt.Printf("backend: %s\n", manager.Backend())
		return runServiceCommand(ctx, manager, command)

	case "update":
		return runUpdate(ctx, def, updateOptions{
			backend:  *backend,
			repo:     *repo,
			gitlab:   *gitlab,
			baseURL:  *baseURL,
			url:      *url,
			version:  *remote,
			token:    *token,
			unpack:   *unpack,
			maxBytes: *maxBytes,
		})

	case "backends":
		fmt.Println(strings.Join(svc.Backends(), "\n"))
		return nil

	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func runServiceCommand(ctx context.Context, manager svc.Manager, command string) error {
	switch command {
	case "install":
		if err := manager.Install(ctx); err != nil {
			return err
		}
		fmt.Println("installed")
		return nil
	case "uninstall":
		if err := manager.Uninstall(ctx); err != nil && !errors.Is(err, svc.ErrNotInstalled) {
			return err
		}
		fmt.Println("uninstalled")
		return nil
	case "start":
		return manager.Start(ctx)
	case "stop":
		return manager.Stop(ctx)
	case "restart":
		return manager.Restart(ctx)
	default:
		status, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("state: %s pid: %d detail: %s\n", status.State, status.PID, status.Detail)
		return nil
	}
}

// updateOptions is what the -url / -repo / -gitlab / -base-url / -token /
// -unpack / -max-bytes flags describe.
type updateOptions struct {
	backend  string
	repo     string
	gitlab   string
	baseURL  string
	url      string
	version  string
	token    string
	unpack   string
	maxBytes int64
}

func runUpdate(ctx context.Context, def svc.Definition, opts updateOptions) error {
	source, err := opts.source()
	if err != nil {
		return err
	}

	updater := &update.Updater{
		Source:   source,
		Version:  version,
		Unpack:   unpackByURL(opts.unpack),
		MaxBytes: opts.maxBytes,
		// Restart through the service manager so the new binary takes over.
		Restart: func(ctx context.Context) error {
			manager, err := svc.NewWith(opts.backend, def)
			if err != nil {
				return nil // not installed: the caller restarts it manually
			}
			return manager.Restart(ctx)
		},
	}

	result, err := updater.Update(ctx)
	if errors.Is(err, update.ErrUpToDate) {
		fmt.Printf("already up to date (%s)\n", version)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("updated %s -> %s\n", result.From, result.To)
	return nil
}

// source picks the release channel the flags describe: a single download URL,
// a GitLab project, or a GitHub-compatible release API (GitHub itself, or a
// self-hosted Gitea/Forgejo through BaseURL).
func (o updateOptions) source() (update.Source, error) {
	if o.url != "" {
		return &update.URLSource{URL: o.url, Version: o.version, Token: o.token}, nil
	}
	if o.gitlab != "" {
		return &update.GitLabSource{Project: o.gitlab, BaseURL: o.baseURL, Token: o.token}, nil
	}
	if o.repo == "" {
		return nil, errors.New("the update command needs -repo, -gitlab or -url")
	}
	parts := strings.SplitN(o.repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("-repo must look like owner/repo, got %q", o.repo)
	}
	return &update.GitHubSource{
		Owner:   parts[0],
		Repo:    parts[1],
		BaseURL: o.baseURL,
		Token:   o.token,
	}, nil
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
