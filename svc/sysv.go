//go:build linux

package svc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// sysvDir holds init scripts on SysV, BusyBox, Entware and most vendor firmwares.
const sysvDir = "/etc/init.d"

type sysvManager struct {
	def Definition
}

func newSysvManager(def Definition) Manager { return &sysvManager{def: def} }

func (m *sysvManager) Backend() string        { return "sysv" }
func (m *sysvManager) Definition() Definition { return m.def }
func (m *sysvManager) scriptPath() string     { return filepath.Join(sysvDir, m.def.Name) }

// sysvTemplate is intentionally POSIX sh + BusyBox compatible:
//   - no bashisms such as {1..10}
//   - no `readlink -f` (absent on many CPE firmwares)
//   - liveness via `kill -0` instead of parsing `ps`
//   - the command line is embedded in a function so quoted arguments survive
//     (expanding a pre-quoted string would pass the quotes literally)
type sysvTemplateData struct {
	Name        string
	NameQ       string
	Description string
	LogDirQ     string
	EnvFile     string
	EnvFileQ    string
	Env         []string
	ExecLine    string
}

// resolveLogDir picks a writable log directory. Embedded rootfs are frequently
// read-only or lack /var/log entirely, so fall back to /tmp instead of handing
// the daemon an unusable redirection target.
func resolveLogDir(def Definition) string {
	if def.LogDir != "" {
		if err := os.MkdirAll(def.LogDir, 0o755); err == nil {
			return def.LogDir
		}
		return "/tmp"
	}
	if err := os.MkdirAll("/var/log", 0o755); err == nil {
		return "/var/log"
	}
	return "/tmp"
}

func (m *sysvManager) Install(ctx context.Context) error {
	logDir := resolveLogDir(m.def)

	data := sysvTemplateData{
		Name:        m.def.Name,
		NameQ:       shellQuote(m.def.Name),
		Description: m.def.Description,
		LogDirQ:     shellQuote(logDir),
		EnvFile:     m.def.EnvFile,
		Env:         []string{},
		ExecLine:    shellQuote(m.def.ExecPath) + " " + argLine(m.def.Args),
	}
	if m.def.EnvFile != "" {
		data.EnvFileQ = shellQuote(m.def.EnvFile)
	}
	if lines := envLines(m.def.Env); lines != "" {
		data.Env = strings.Split(lines, "\n")
	}

	content := buildSysv(data)
	if err := writeFile(m.scriptPath(), content, 0o755); err != nil {
		return err
	}

	if err := enableSysv(ctx, m.def.Name); err != nil {
		return err
	}
	return run(ctx, m.scriptPath(), "restart")
}

// enableSysv registers boot symlinks with whatever tool exists. Many router
// firmwares execute every script under /etc/init.d at boot, in which case no
// registration is needed at all.
func enableSysv(ctx context.Context, name string) error {
	switch {
	case hasCommand("update-rc.d"):
		return run(ctx, "update-rc.d", name, "defaults")
	case hasCommand("chkconfig"):
		return run(ctx, "chkconfig", "--add", name)
	case hasCommand("insserv"):
		return run(ctx, "insserv", name)
	default:
		return nil
	}
}

func disableSysv(ctx context.Context, name string) error {
	switch {
	case hasCommand("update-rc.d"):
		return run(ctx, "update-rc.d", "-f", name, "remove")
	case hasCommand("chkconfig"):
		return run(ctx, "chkconfig", "--del", name)
	case hasCommand("insserv"):
		return run(ctx, "insserv", "-r", name)
	default:
		return nil
	}
}

func (m *sysvManager) Uninstall(ctx context.Context) error {
	if fileExists(m.scriptPath()) {
		_ = run(ctx, m.scriptPath(), "stop")
	}
	_ = disableSysv(ctx, m.def.Name)
	_ = removeFile("/var/run/" + m.def.Name + ".pid")
	return removeFile(m.scriptPath())
}

func (m *sysvManager) Start(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "start")
}

func (m *sysvManager) Stop(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "stop")
}

func (m *sysvManager) Restart(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "restart")
}

func (m *sysvManager) Status(ctx context.Context) (Status, error) {
	if !fileExists(m.scriptPath()) {
		return Status{State: StateNotInstalled, Detail: m.scriptPath()}, nil
	}

	pid := readPIDFile("/var/run/" + m.def.Name + ".pid")
	if pid > 0 && pidAlive(pid) {
		return Status{State: StateRunning, PID: pid, Detail: "pidfile"}, nil
	}
	// The script exists but wrote no usable pid file (e.g. the daemon was
	// started by something else): fall back to the executable name.
	if pid := findProcessByExe(m.def.ExecPath); pid > 0 {
		return Status{State: StateRunning, PID: pid, Detail: "process"}, nil
	}
	return Status{State: StateStopped, Detail: "pidfile"}, nil
}

const sysvTail = `

run_daemon() {
    exec @@EXEC@@
}

get_pid() {
    [ -f "$pid_file" ] && cat "$pid_file"
}

is_running() {
    pid=$(get_pid)
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

start() {
    if is_running; then
        echo "$name is already running"
        return 0
    fi
    [ -d "$log_dir" ] || mkdir -p "$log_dir" 2>/dev/null
    echo "Starting $name"
    run_daemon >>"$stdout_log" 2>>"$stderr_log" &
    echo $! >"$pid_file"
    return 0
}

stop() {
    if ! is_running; then
        echo "$name is not running"
        rm -f "$pid_file"
        return 0
    fi
    echo "Stopping $name"
    pid=$(get_pid)
    kill "$pid" 2>/dev/null
    i=0
    while [ $i -lt 10 ]; do
        is_running || break
        sleep 1
        i=$((i + 1))
    done
    if is_running; then
        echo "$name did not stop, sending SIGKILL"
        kill -9 "$pid" 2>/dev/null
    fi
    rm -f "$pid_file"
    return 0
}

status() {
    if is_running; then
        echo "$name is running"
        return 0
    fi
    echo "$name is not running"
    return 1
}

case "$1" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; start ;;
    status)  status ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 2
        ;;
esac

exit 0
`

func buildSysv(d sysvTemplateData) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n#\n# " + d.Name + " - installed by goservice\n#\n# chkconfig: 2345 95 05\n# description: " + d.Description + "\n#\n### BEGIN INIT INFO\n# Provides:          " + d.Name + "\n# Required-Start:    $network\n# Required-Stop:     $network\n# Default-Start:     2 3 4 5\n# Default-Stop:      0 1 6\n# Short-Description: " + d.Name + "\n# Description:       " + d.Description + "\n### END INIT INFO\n\nname=" + d.NameQ + "\npid_file=\"/var/run/" + d.Name + ".pid\"\nlog_dir=" + d.LogDirQ + "\nstdout_log=\"$log_dir/" + d.Name + ".log\"\nstderr_log=\"$log_dir/" + d.Name + ".err\"\n")
	if d.EnvFile != "" {
		b.WriteString("\n[ -r " + d.EnvFileQ + " ] && . " + d.EnvFileQ)
	}
	for _, e := range d.Env {
		b.WriteString("\n" + e)
	}
	b.WriteString(strings.Replace(sysvTail, "@@EXEC@@", d.ExecLine, 1))
	return b.String()
}
