// Package svc installs and controls a long running daemon as a native system
// service on Linux (systemd, OpenWrt procd, plain SysV/BusyBox), macOS
// (launchd), the BSDs (rc.d) and Windows (Service Control Manager).
//
// The package is deliberately free of any CLI, logging or application
// framework: you describe the service with a Definition and the package writes
// the platform files, enables the unit and starts it.
//
// Embedded targets (OpenWrt, Entware, vendor routers, CPEs) are first class:
// the SysV template is POSIX sh only (no bashisms such as `{1..10}` or
// `readlink -f`) and procd is used when /etc/rc.common is present.
package svc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	// ErrNotInstalled is returned when an operation needs an installed service.
	ErrNotInstalled = errors.New("service is not installed")
	// ErrUnsupported is returned on platforms without a known service manager.
	ErrUnsupported = errors.New("service management is not supported on this platform")
)

// State is the coarse state of a service.
type State int

const (
	StateUnknown State = iota
	StateRunning
	StateStopped
	StateNotInstalled
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	case StateNotInstalled:
		return "not-installed"
	default:
		return "unknown"
	}
}

// Status is a best effort snapshot; Detail carries backend specific information
// (unit state, pid file, launchd label, ...) for logging and debugging.
type Status struct {
	State  State
	PID    int
	Detail string
}

// Definition describes the service to manage.
//
// Only Name and ExecPath are strictly required; ExecPath defaults to the
// currently running binary. Args are passed verbatim to the process, so put
// subcommands here (e.g. []string{"tunnel", "run", "--token", tok}).
type Definition struct {
	// Name is the service identifier: systemd/procd/sysv unit name, launchd
	// label, rc.d script name or Windows service name. Keep it filesystem and
	// shell safe (lowercase, digits, '-', '_').
	Name string
	// DisplayName is shown by Windows and used as the unit Description.
	DisplayName string
	// Description is an optional longer description.
	Description string
	// ExecPath is the absolute path of the binary to run.
	ExecPath string
	// Args are the arguments passed to ExecPath.
	Args []string
	// User/Group to run as. Empty means whatever the service manager defaults
	// to (root for system units). Ignored on Windows.
	User  string
	Group string
	// WorkingDir sets the process working directory when supported.
	WorkingDir string
	// LogDir receives stdout/stderr on backends that cannot capture output
	// themselves (SysV, procd, rc.d). Empty disables redirection where the
	// backend can do that natively (systemd/journald, launchd).
	LogDir string
	// Env holds extra environment variables written into the unit.
	Env map[string]string
	// EnvFile is an optional file with KEY=VALUE lines sourced by the init
	// script (SysV/procd/rc.d).
	EnvFile string
	// Restart restarts the process when it exits unexpectedly.
	Restart bool
	// RestartSec is the delay before a restart; defaults to 5s.
	RestartSec time.Duration
	// Dependencies are the units/services that must be up first. On Linux they
	// become After=/Wants= (e.g. "network-online.target"), on Windows the SCM
	// dependency list (e.g. "Tcpip"), elsewhere they are ignored.
	Dependencies []string
	// Notify emits sd_notify READY=1, switching the systemd unit to Type=notify.
	// Use svc.NotifyReady from your program once it is serving traffic.
	Notify bool
	// AutoStart enables the service at boot.
	AutoStart bool
}

// normalize validates the definition and fills in defaults.
func (d *Definition) normalize() error {
	if d.Name == "" {
		return errors.New("svc: Definition.Name is required")
	}
	if strings.ContainsAny(d.Name, " \t/\\\"'") {
		return fmt.Errorf("svc: Definition.Name %q must not contain spaces or path separators", d.Name)
	}

	if d.ExecPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("svc: cannot determine executable path: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		d.ExecPath = exe
	}

	if d.DisplayName == "" {
		d.DisplayName = d.Name
	}
	if d.Description == "" {
		d.Description = d.DisplayName
	}
	if d.RestartSec <= 0 {
		d.RestartSec = 5 * time.Second
	}
	return nil
}

// Manager controls one installed service.
type Manager interface {
	// Backend reports the concrete implementation: "systemd", "procd",
	// "sysv", "launchd", "rcd" or "windows".
	Backend() string
	// Definition returns the (normalized) definition in use.
	Definition() Definition
	Install(ctx context.Context) error
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Status(ctx context.Context) (Status, error)
}

// New returns the Manager that fits the current machine.
func New(def Definition) (Manager, error) {
	return NewWith("", def)
}

// Do runs one of the commands a daemon's CLI exposes: install, uninstall,
// start, stop, restart or status. It is the whole body of the usual
// "gottyd -service=<verb>" switch, so application code does not need to repeat
// it; status prints one line to out and everything else reports an error.
func Do(ctx context.Context, manager Manager, command string, out io.Writer) error {
	switch command {
	case "install":
		return manager.Install(ctx)
	case "uninstall":
		return manager.Uninstall(ctx)
	case "start":
		return manager.Start(ctx)
	case "stop":
		return manager.Stop(ctx)
	case "restart":
		return manager.Restart(ctx)
	case "status":
		status, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if out != nil {
			fmt.Fprintf(out, "%s pid=%d %s\n", status.State, status.PID, status.Detail)
		}
		return nil
	default:
		return fmt.Errorf("svc: unknown service command %q (want install, uninstall, start, stop, restart or status)", command)
	}
}

// ServiceCommand recognises the conventional "<binary> service <verb>" shape —
// the one cloudflared and most daemons use — and returns the verb together with
// the remaining arguments, so the rest can become Definition.Args:
//
//	["service","install","-p","8080","bash"] -> "install", ["-p","8080","bash"], true
//	["service"]                              -> "",        nil,                  true
//	["bash"]                                 -> "",        nil,                  false
//	["--","service","install"]               -> "",        nil,                  false
//
// An empty verb with ok means "service" was given without a command, which is a
// usage error for the caller to report. A leading "--" opts out, so a daemon
// whose own command is literally "service" can still run it.
func ServiceCommand(args []string) (verb string, rest []string, ok bool) {
	if len(args) == 0 || args[0] != "service" {
		return "", nil, false
	}
	if len(args) < 2 {
		return "", nil, true
	}
	return args[1], append([]string(nil), args[2:]...), true
}

// StripFlags removes the named flags from an argument list, which is how an
// application turns its own os.Args into Definition.Args: drop the flags that
// only made sense on the command line (-service, -d), keep everything else for
// the unit file.
//
// Only the flag token is removed, never the following argument, so a value must
// be attached: -service=install is stripped, "-service install" is not. A
// trailing command such as "bash" or its own flags are always preserved.
func StripFlags(args []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[strings.TrimLeft(name, "-")] = true
	}

	out := make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			// Everything after it belongs to the command, flags included.
			out = append(out, args[i:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			name := strings.TrimLeft(arg, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			if drop[name] {
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

// NewWith returns the Manager for a named backend instead of the one detected
// for this machine, which is how you install a different unit than the host
// would choose: generate a SysV script on a systemd box, script the procd path
// on a router whose detection is wrong, or exercise a backend in a test.
//
// An empty name means automatic detection, exactly like New. Backends lists
// what this build can offer; asking for anything else is an error that names
// the alternatives.
func NewWith(backend string, def Definition) (Manager, error) {
	if err := def.normalize(); err != nil {
		return nil, err
	}
	if backend == "" {
		return newPlatformManager(def)
	}

	constructor, ok := backends[backend]
	if !ok {
		available := Backends()
		if len(available) == 0 {
			return nil, fmt.Errorf("svc: backend %q is not available here: %w", backend, ErrUnsupported)
		}
		return nil, fmt.Errorf("svc: unknown backend %q, this platform provides %s", backend, strings.Join(available, ", "))
	}
	return constructor(def), nil
}

// Backends lists the backends compiled into this build, sorted by name. It is
// what NewWith accepts, and it changes with the target platform: "systemd",
// "procd" and "sysv" on Linux, "launchd" on macOS, "rcd" on the BSDs and
// "windows" on Windows.
func Backends() []string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// -----------------------------------------------------------------------------
// helpers shared by the backends
// -----------------------------------------------------------------------------

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasCommand reports whether name is on PATH.
func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// run executes a command and folds its combined output into the error, which is
// what you want when the failure comes from systemctl/launchctl/service.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}

// runQuiet is run() without the error decoration, for probes.
func runQuiet(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func writeFile(path, content string, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("svc: creating %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("svc: writing %s: %w", path, err)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("svc: removing %s: %w", path, err)
	}
	return nil
}

// shellQuote single quotes s for /bin/sh.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// systemdQuote quotes an ExecStart argument. systemd does not go through a
// shell, so only whitespace and a few characters need protection.
func systemdQuote(s string) string {
	needsQuote := s == "" || strings.ContainsAny(s, " \t\"'\\;$")
	if !needsQuote {
		return s
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

// xmlEscape escapes the five XML entities, for launchd plists.
func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

// argLine renders args for an init script that runs through a shell.
func argLine(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, shellQuote(a))
	}
	return strings.Join(quoted, " ")
}

// envLines renders KEY=VALUE pairs for a shell script or an EnvFile, in a stable
// order so reinstalling does not rewrite the script with shuffled lines.
func envLines(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	lines := make([]string, 0, len(env))
	for _, key := range sortedEnvKeys(env) {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(env[key])))
	}
	return strings.Join(lines, "\n")
}

// readPIDFile reads the first pid from a pid file, 0 when absent or invalid.
func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parsePID(string(data))
}

// parsePID extracts a leading integer from s (pid files often end with a
// newline, some init scripts write "1234 " or "pid: 1234").
func parsePID(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	fields := strings.Fields(s)
	last := fields[len(fields)-1]
	var pid int
	if _, err := fmt.Sscanf(last, "%d", &pid); err != nil {
		return 0
	}
	return pid
}
