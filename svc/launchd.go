//go:build darwin

package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// launchdManager installs a LaunchDaemon when running as root (starts at boot)
// or a LaunchAgent otherwise (starts at login), mirroring what cloudflared does.
type launchdManager struct {
	def       Definition
	label     string
	plistPath string
	logDir    string
}

func newLaunchdManager(def Definition) Manager {
	m := &launchdManager{def: def, label: def.Name}

	if os.Geteuid() == 0 {
		m.plistPath = filepath.Join("/Library/LaunchDaemons", def.Name+".plist")
		m.logDir = "/Library/Logs"
		return m
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return m // Install reports the missing path
	}
	m.plistPath = filepath.Join(home, "Library/LaunchAgents", def.Name+".plist")
	m.logDir = filepath.Join(home, "Library/Logs")
	return m
}

func (m *launchdManager) Backend() string        { return "launchd" }
func (m *launchdManager) Definition() Definition { return m.def }

// The plist data and its renderer live in render.go (no build tag), so the
// generated plist can be golden tested on any machine.

func (m *launchdManager) Install(ctx context.Context) error {
	if m.plistPath == "" {
		return errors.New("svc: cannot determine launchd plist path (no home directory and not root)")
	}

	logDir := m.def.LogDir
	if logDir == "" {
		logDir = m.logDir
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		logDir = os.TempDir()
	}

	content := buildLaunchd(launchdPlistFor(m.def, m.label, logDir))
	if err := writeFile(m.plistPath, content, 0o644); err != nil {
		return err
	}

	// Reload so an already loaded job picks up the new definition.
	_ = run(ctx, "launchctl", "unload", "-w", m.plistPath)
	if err := run(ctx, "launchctl", "load", "-w", m.plistPath); err != nil {
		return err
	}
	if !m.def.AutoStart {
		// RunAtLoad already started it; stop but keep the job registered.
		_ = run(ctx, "launchctl", "stop", m.label)
	}
	return nil
}

func (m *launchdManager) Uninstall(ctx context.Context) error {
	if m.plistPath == "" {
		return nil
	}
	_ = run(ctx, "launchctl", "unload", "-w", m.plistPath)
	return removeFile(m.plistPath)
}

func (m *launchdManager) Start(ctx context.Context) error {
	return run(ctx, "launchctl", "start", m.label)
}

func (m *launchdManager) Stop(ctx context.Context) error {
	return run(ctx, "launchctl", "stop", m.label)
}

func (m *launchdManager) Restart(ctx context.Context) error {
	// launchd has no restart verb; a kickstart is the documented equivalent.
	if err := run(ctx, "launchctl", "kickstart", "-k", m.label); err == nil {
		return nil
	}
	if err := run(ctx, "launchctl", "stop", m.label); err != nil {
		return err
	}
	return run(ctx, "launchctl", "start", m.label)
}

var launchdPIDPattern = regexp.MustCompile(`"PID"\s*=\s*(\d+)`)

func (m *launchdManager) Status(ctx context.Context) (Status, error) {
	if m.plistPath == "" || !fileExists(m.plistPath) {
		return Status{State: StateNotInstalled, Detail: m.plistPath}, nil
	}

	out, err := runQuiet(ctx, "launchctl", "list", m.label)
	if err != nil {
		// Registered plist but no loaded job.
		return Status{State: StateStopped, Detail: "plist only"}, nil
	}
	if match := launchdPIDPattern.FindStringSubmatch(out); len(match) == 2 {
		if pid, convErr := strconv.Atoi(match[1]); convErr == nil {
			return Status{State: StateRunning, PID: pid, Detail: "launchctl"}, nil
		}
	}
	return Status{State: StateStopped, Detail: "launchctl"}, nil
}
