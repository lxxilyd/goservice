//go:build linux

package svc

import (
	"context"
	"path/filepath"
	"strings"
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

type procdTemplateData struct {
	Name     string
	ExecLine string
	Restart  bool
	Respawn  string
	Env      []string
}

func (m *procdManager) Install(ctx context.Context) error {
	// threshold(3600s) timeout(5s) retry(5) matches OpenWrt's own defaults for
	// always-on daemons: respawn up to 5 times within an hour.
	data := procdTemplateData{
		Name:     m.def.Name,
		ExecLine: shellQuote(m.def.ExecPath) + " " + argLine(m.def.Args),
		Restart:  m.def.Restart,
		Respawn:  "3600 5 5",
		Env:      envPairLines(m.def.Env),
	}

	content := buildProcd(data)
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

// envPairLines renders KEY=VALUE pairs for procd_set_param env.
func envPairLines(env map[string]string) []string {
	lines := make([]string, 0, len(env))
	for k, v := range env {
		lines = append(lines, k+"="+shellQuote(v))
	}
	return lines
}

func buildProcd(d procdTemplateData) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh /etc/rc.common\n#\n# " + d.Name + " - installed by goservice\n\nSTART=95\nSTOP=10\nUSE_PROCD=1\n\nstart_service() {\n\tprocd_open_instance\n\tprocd_set_param command " + d.ExecLine + "\n\tprocd_set_param stdout 1\n\tprocd_set_param stderr 1")
	if d.Restart {
		b.WriteString("\n\tprocd_set_param respawn " + d.Respawn)
	} else {
		b.WriteString("\n\tprocd_set_param respawn 0")
	}
	for _, e := range d.Env {
		b.WriteString("\n\tprocd_set_param env " + e)
	}
	b.WriteString("\n\tprocd_close_instance\n}\n\nservice_triggers() {\n\tprocd_add_reload_trigger \"" + d.Name + "\"\n}\n")
	return b.String()
}
