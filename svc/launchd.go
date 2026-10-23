//go:build darwin

package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

type launchdEnv struct {
	K string
	V string
}

type launchdPlistData struct {
	Label      string
	Exec       string
	Args       []string
	Restart    bool
	Throttle   int
	Stdout     string
	Stderr     string
	WorkingDir string
	Env        []launchdEnv
}

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

	data := launchdPlistData{
		Label:    xmlEscape(m.label),
		Exec:     xmlEscape(m.def.ExecPath),
		Restart:  m.def.Restart,
		Throttle: int(m.def.RestartSec.Seconds()),
		Stdout:   xmlEscape(filepath.Join(logDir, m.def.Name+".log")),
		Stderr:   xmlEscape(filepath.Join(logDir, m.def.Name+".err")),
	}
	for _, arg := range m.def.Args {
		data.Args = append(data.Args, xmlEscape(arg))
	}
	if m.def.WorkingDir != "" {
		data.WorkingDir = xmlEscape(m.def.WorkingDir)
	}
	for k, v := range m.def.Env {
		data.Env = append(data.Env, launchdEnv{K: xmlEscape(k), V: xmlEscape(v)})
	}

	content := buildLaunchd(data)
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

func buildLaunchd(d launchdPlistData) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n\t<key>Label</key>\n\t<string>" + d.Label + "</string>\n\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>" + d.Exec + "</string>")
	for _, a := range d.Args {
		b.WriteString("\n\t\t<string>" + a + "</string>")
	}
	b.WriteString("\n\t</array>\n\t<key>RunAtLoad</key>\n\t<true/>")
	if d.Restart {
		b.WriteString("\n\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>")
	}
	b.WriteString("\n\t<key>ThrottleInterval</key>\n\t<integer>" + strconv.Itoa(d.Throttle) + "</integer>\n\t<key>StandardOutPath</key>\n\t<string>" + d.Stdout + "</string>\n\t<key>StandardErrorPath</key>\n\t<string>" + d.Stderr + "</string>")
	if d.WorkingDir != "" {
		b.WriteString("\n\t<key>WorkingDirectory</key>\n\t<string>" + d.WorkingDir + "</string>")
	}
	if len(d.Env) > 0 {
		b.WriteString("\n\t<key>EnvironmentVariables</key>\n\t<dict>")
		for _, e := range d.Env {
			b.WriteString("\n\t\t<key>" + e.K + "</key>\n\t\t<string>" + e.V + "</string>")
		}
		b.WriteString("\n\t</dict>")
	}
	b.WriteString("\n</dict>\n</plist>\n")
	return b.String()
}
