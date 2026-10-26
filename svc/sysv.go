//go:build linux

package svc

import (
	"context"
	"os"
	"path/filepath"
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

// sysvTemplateData and its renderer live in render.go, which carries no build
// tag so the generated script can be golden tested anywhere.

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

	content := buildSysv(sysvScriptFor(m.def, logDir))
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
