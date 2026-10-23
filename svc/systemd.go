//go:build linux

package svc

import (
	"context"
	"path/filepath"
	"strings"
)

// systemdUnitDir is the location of administrator managed units.
const systemdUnitDir = "/etc/systemd/system"

type systemdManager struct {
	def Definition
}

func newSystemdManager(def Definition) Manager { return &systemdManager{def: def} }

func (m *systemdManager) Backend() string        { return "systemd" }
func (m *systemdManager) Definition() Definition { return m.def }
func (m *systemdManager) unitName() string       { return m.def.Name + ".service" }
func (m *systemdManager) unitPath() string       { return filepath.Join(systemdUnitDir, m.unitName()) }

type systemdUnitData struct {
	Description string
	Type        string
	Exec        string
	WorkingDir  string
	User        string
	Group       string
	Env         []string
	EnvFile     string
	Restart     bool
	RestartSec  string
	After       string
	Wants       string
}

// unitDependencies renders After=/Wants=. network-online.target is always kept
// so a daemon does not race the network on boot.
func unitDependencies(deps []string) (after, wants string) {
	units := make([]string, 0, len(deps)+1)
	units = append(units, deps...)
	units = append(units, "network-online.target")
	return strings.Join(units, " "), strings.Join(units, " ")
}

// systemdExec quotes ExecStart arguments the way systemd (not a shell) expects.
func systemdExec(def Definition) string {
	parts := make([]string, 0, len(def.Args)+1)
	parts = append(parts, systemdQuote(def.ExecPath))
	for _, arg := range def.Args {
		parts = append(parts, systemdQuote(arg))
	}
	return strings.Join(parts, " ")
}

func (m *systemdManager) Install(ctx context.Context) error {
	after, wants := unitDependencies(m.def.Dependencies)

	data := systemdUnitData{
		Description: m.def.Description,
		Type:        "simple",
		Exec:        systemdExec(m.def),
		User:        m.def.User,
		Group:       m.def.Group,
		Restart:     m.def.Restart,
		RestartSec:  m.def.RestartSec.String(),
		EnvFile:     m.def.EnvFile,
		After:       after,
		Wants:       wants,
	}
	if m.def.Notify {
		data.Type = "notify"
	}
	if m.def.WorkingDir != "" {
		data.WorkingDir = systemdQuote(m.def.WorkingDir)
	}
	for k, v := range m.def.Env {
		data.Env = append(data.Env, systemdQuote(k+"="+v))
	}

	content := buildSystemd(data)
	if err := writeFile(m.unitPath(), content, 0o644); err != nil {
		return err
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if m.def.AutoStart {
		if err := run(ctx, "systemctl", "enable", m.unitName()); err != nil {
			return err
		}
	}
	return run(ctx, "systemctl", "restart", m.unitName())
}

func (m *systemdManager) Uninstall(ctx context.Context) error {
	// Best effort: the unit may be half installed or already stopped.
	_ = run(ctx, "systemctl", "stop", m.unitName())
	_ = run(ctx, "systemctl", "disable", m.unitName())

	if err := removeFile(m.unitPath()); err != nil {
		return err
	}
	return run(ctx, "systemctl", "daemon-reload")
}

func (m *systemdManager) Start(ctx context.Context) error {
	return run(ctx, "systemctl", "start", m.unitName())
}

func (m *systemdManager) Stop(ctx context.Context) error {
	return run(ctx, "systemctl", "stop", m.unitName())
}

func (m *systemdManager) Restart(ctx context.Context) error {
	return run(ctx, "systemctl", "restart", m.unitName())
}

func (m *systemdManager) Status(ctx context.Context) (Status, error) {
	if !fileExists(m.unitPath()) {
		return Status{State: StateNotInstalled, Detail: m.unitPath()}, nil
	}

	// `is-active` exits non-zero for inactive/failed units, which is not an
	// error condition for us, so the output is what matters.
	state, _ := runQuiet(ctx, "systemctl", "is-active", m.unitName())
	status := Status{Detail: state}
	if state == "active" || state == "activating" || state == "reloading" {
		status.State = StateRunning
	} else {
		status.State = StateStopped
	}

	if pid, err := runQuiet(ctx, "systemctl", "show", "-p", "MainPID", "--value", m.unitName()); err == nil {
		status.PID = parsePID(pid)
	}
	return status, nil
}

func buildSystemd(d systemdUnitData) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=" + d.Description + "\nAfter=" + d.After + "\nWants=" + d.Wants + "\n\n[Service]\nType=" + d.Type + "\nExecStart=" + d.Exec)
	if d.WorkingDir != "" {
		b.WriteString("\nWorkingDirectory=" + d.WorkingDir)
	}
	if d.User != "" {
		b.WriteString("\nUser=" + d.User)
	}
	if d.Group != "" {
		b.WriteString("\nGroup=" + d.Group)
	}
	for _, e := range d.Env {
		b.WriteString("\nEnvironment=" + e)
	}
	if d.EnvFile != "" {
		b.WriteString("\nEnvironmentFile=-" + d.EnvFile)
	}
	if d.Restart {
		b.WriteString("\nRestart=on-failure\nRestartSec=" + d.RestartSec)
	}
	b.WriteString("\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}
