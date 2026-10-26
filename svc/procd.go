//go:build linux

package svc

import (
	"context"
	"path/filepath"
)

// procdManager drives OpenWrt (and vendor firmware that inherited rc.common).
// procd keeps the process alive itself, so stdout/stderr go to syslog instead of
// files and no pid file is involved.
type procdManager struct {
	def Definition
}

func newProcdManager(def Definition) Manager { return &procdManager{def: def} }

func (m *procdManager) Backend() string        { return "procd" }
func (m *procdManager) Definition() Definition { return m.def }
func (m *procdManager) scriptPath() string     { return filepath.Join(sysvDir, m.def.Name) }

// The procd script data and its renderer live in render.go (no build tag), so
// the generated script can be golden tested on any machine.

func (m *procdManager) Install(ctx context.Context) error {
	content := buildProcd(procdScriptFor(m.def))
	if err := writeFile(m.scriptPath(), content, 0o755); err != nil {
		return err
	}

	action := "disable"
	if m.def.AutoStart {
		action = "enable"
	}
	if err := run(ctx, m.scriptPath(), action); err != nil {
		return err
	}
	return run(ctx, m.scriptPath(), "restart")
}

func (m *procdManager) Uninstall(ctx context.Context) error {
	if fileExists(m.scriptPath()) {
		_ = run(ctx, m.scriptPath(), "stop")
		_ = run(ctx, m.scriptPath(), "disable")
	}
	return removeFile(m.scriptPath())
}

func (m *procdManager) Start(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "start")
}

func (m *procdManager) Stop(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "stop")
}

func (m *procdManager) Restart(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "restart")
}

func (m *procdManager) Status(ctx context.Context) (Status, error) {
	if !fileExists(m.scriptPath()) {
		return Status{State: StateNotInstalled, Detail: m.scriptPath()}, nil
	}
	// procd exposes no portable status subcommand across OpenWrt versions, so
	// resolve the running instance through /proc.
	if pid := findProcessByExe(m.def.ExecPath); pid > 0 {
		return Status{State: StateRunning, PID: pid, Detail: "proc"}, nil
	}
	return Status{State: StateStopped, Detail: "proc"}, nil
}
