//go:build freebsd || openbsd || netbsd

package svc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// rcdDir is where third party rc.d scripts live. OpenBSD only scans /etc/rc.d
// for services managed by rcctl, the other BSDs use /usr/local/etc/rc.d.
func rcdDir() string {
	if runtime.GOOS == "openbsd" {
		return "/etc/rc.d"
	}
	return "/usr/local/etc/rc.d"
}

// rcdConfPath is the file rc.d variables are persisted in.
func rcdConfPath() string {
	if runtime.GOOS == "freebsd" {
		return "/etc/rc.conf"
	}
	return "/etc/rc.conf.local"
}

type rcdManager struct {
	def Definition
}

func newRcdManager(def Definition) Manager { return &rcdManager{def: def} }

func (m *rcdManager) Backend() string        { return "rcd" }
func (m *rcdManager) Definition() Definition { return m.def }
func (m *rcdManager) scriptPath() string     { return filepath.Join(rcdDir(), m.def.Name) }

// rcvar must be a valid shell identifier, so '-' and '.' become '_'.
func (m *rcdManager) rcvar() string {
	replacer := strings.NewReplacer("-", "_", ".", "_")
	return replacer.Replace(m.def.Name) + "_enable"
}

type rcdTemplateData struct {
	Name     string
	Rcvar    string
	ConfPath string
	Exec     string
	ArgsLine string
}

func (m *rcdManager) Install(ctx context.Context) error {
	data := rcdTemplateData{
		Name:     m.def.Name,
		Rcvar:    m.rcvar(),
		ConfPath: rcdConfPath(),
		Exec:     m.def.ExecPath,
		ArgsLine: argLine(m.def.Args),
	}

	content := buildRcd(data)
	if err := writeFile(m.scriptPath(), content, 0o755); err != nil {
		return err
	}

	if m.def.AutoStart {
		if err := m.enable(ctx); err != nil {
			return err
		}
	}
	return run(ctx, m.scriptPath(), "start")
}

func (m *rcdManager) enable(ctx context.Context) error {
	// FreeBSD ships sysrc, which edits the right file and keeps formatting.
	if runtime.GOOS == "freebsd" && hasCommand("sysrc") {
		return run(ctx, "sysrc", m.rcvar()+"=YES")
	}
	return setShellVar(rcdConfPath(), m.rcvar(), "YES")
}

func (m *rcdManager) Uninstall(ctx context.Context) error {
	if fileExists(m.scriptPath()) {
		_ = run(ctx, m.scriptPath(), "stop")
	}
	_ = setShellVar(rcdConfPath(), m.rcvar(), "NO")
	_ = removeFile(filepath.Join("/var/run", m.def.Name+".pid"))
	return removeFile(m.scriptPath())
}

func (m *rcdManager) Start(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "start")
}

func (m *rcdManager) Stop(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "stop")
}

func (m *rcdManager) Restart(ctx context.Context) error {
	return run(ctx, m.scriptPath(), "restart")
}

func (m *rcdManager) Status(ctx context.Context) (Status, error) {
	if !fileExists(m.scriptPath()) {
		return Status{State: StateNotInstalled, Detail: m.scriptPath()}, nil
	}

	if pid := readPIDFile(filepath.Join("/var/run", m.def.Name+".pid")); pid > 0 && pidAlive(pid) {
		return Status{State: StateRunning, PID: pid, Detail: "pidfile"}, nil
	}
	if pid := findProcessByCommand(ctx, m.def.ExecPath); pid > 0 {
		return Status{State: StateRunning, PID: pid, Detail: "process"}, nil
	}
	return Status{State: StateStopped, Detail: m.scriptPath()}, nil
}

// findProcessByCommand matches the first field of `ps -o args`, which is the
// executable path on all three BSDs.
func findProcessByCommand(ctx context.Context, exePath string) int {
	out, err := runQuiet(ctx, "ps", "-ax", "-o", "pid=,args=")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] != exePath {
			continue
		}
		if pid, convErr := strconv.Atoi(fields[0]); convErr == nil {
			return pid
		}
	}
	return 0
}

// setShellVar writes key="value" into an rc.conf style file, replacing an
// existing assignment instead of appending duplicates.
func setShellVar(path, key, value string) error {
	desired := key + "=\"" + value + "\""

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			if !replaced {
				out = append(out, desired)
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		// Drop the trailing empty line left by the final newline before appending.
		if n := len(out); n > 0 && out[n-1] == "" {
			out = out[:n-1]
		}
		out = append(out, desired, "")
	}

	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

func buildRcd(d rcdTemplateData) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n#\n# PROVIDE: " + d.Name + "\n# REQUIRE: NETWORKING\n# KEYWORD: shutdown\n#\n# Installed by goservice.\n# Enable with:  sysrc " + d.Rcvar + "=YES     (FreeBSD)\n#               echo '" + d.Rcvar + "=\"YES\"' >> " + d.ConfPath + "   (OpenBSD/NetBSD)\n\n. /etc/rc.subr\n\nname=\"" + d.Name + "\"\nrcvar=\"" + d.Rcvar + "\"\ncommand=\"" + d.Exec + "\"")
	if d.ArgsLine != "" {
		b.WriteString("\ncommand_args=\"" + d.ArgsLine + "\"")
	}
	b.WriteString("\n\nload_rc_config $name\n: ${ " + d.Rcvar + ":=\"NO\" }\n\nrun_rc_command \"$1\"\n")
	return b.String()
}
