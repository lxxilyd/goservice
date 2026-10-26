package svc

import (
	"path"
	"sort"
	"strconv"
	"strings"
)

// The renderers live here, without a build tag, for one reason: the unit file,
// init script and plist they produce are the part that must not silently rot
// when a field is added. Keeping them free of the platform wiring lets a single
// `go test ./svc` on any machine compare every backend against a golden file,
// instead of trusting that "it cross-compiles" means "it writes the right
// file". The platform files only connect these to systemctl/launchctl/rcctl.

// sortedEnvKeys gives the environment variables a stable order, so installing
// twice writes a byte identical unit instead of reshuffling map iteration.
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// -----------------------------------------------------------------------------
// systemd
// -----------------------------------------------------------------------------

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
// so a daemon does not race the network on boot, but callers spell it out often
// enough that appending it blindly produced "network-online.target
// network-online.target" in the unit — hence the dedupe.
func unitDependencies(deps []string) (after, wants string) {
	units := make([]string, 0, len(deps)+1)
	seen := make(map[string]bool, len(deps)+1)
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" || seen[dep] {
			continue
		}
		seen[dep] = true
		units = append(units, dep)
	}
	if !seen["network-online.target"] {
		units = append(units, "network-online.target")
	}
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

// systemdUnitFor is what Install renders; the test renders the same thing so the
// golden file cannot drift away from the installed unit.
func systemdUnitFor(def Definition) systemdUnitData {
	after, wants := unitDependencies(def.Dependencies)

	data := systemdUnitData{
		Description: def.Description,
		Type:        "simple",
		Exec:        systemdExec(def),
		User:        def.User,
		Group:       def.Group,
		Restart:     def.Restart,
		RestartSec:  def.RestartSec.String(),
		EnvFile:     def.EnvFile,
		After:       after,
		Wants:       wants,
	}
	if def.Notify {
		data.Type = "notify"
	}
	if def.WorkingDir != "" {
		data.WorkingDir = systemdQuote(def.WorkingDir)
	}
	for _, key := range sortedEnvKeys(def.Env) {
		data.Env = append(data.Env, systemdQuote(key+"="+def.Env[key]))
	}
	return data
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

// -----------------------------------------------------------------------------
// SysV / BusyBox
// -----------------------------------------------------------------------------

// sysvTemplateData is deliberately POSIX sh + BusyBox compatible: no bashisms
// such as {1..10}, no `readlink -f` (absent on many CPE firmwares), liveness via
// `kill -0` instead of parsing `ps`, and the command line embedded in a function
// so quoted arguments survive (expanding a pre-quoted string would pass the
// quotes literally).
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

// sysvScriptFor is what Install renders; logDir is resolved by the caller
// because it depends on which directories are writable on the target.
func sysvScriptFor(def Definition, logDir string) sysvTemplateData {
	data := sysvTemplateData{
		Name:        def.Name,
		NameQ:       shellQuote(def.Name),
		Description: def.Description,
		LogDirQ:     shellQuote(logDir),
		EnvFile:     def.EnvFile,
		Env:         []string{},
		ExecLine:    shellQuote(def.ExecPath) + " " + argLine(def.Args),
	}
	if def.EnvFile != "" {
		data.EnvFileQ = shellQuote(def.EnvFile)
	}
	if env := envLines(def.Env); env != "" {
		data.Env = strings.Split(env, "\n")
	}
	return data
}

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

// -----------------------------------------------------------------------------
// procd (OpenWrt)
// -----------------------------------------------------------------------------

type procdTemplateData struct {
	Name     string
	ExecLine string
	Restart  bool
	Respawn  string
	Env      []string
}

// envPairLines renders KEY=VALUE pairs for procd_set_param env, in a stable
// order so reinstalling does not rewrite the script with shuffled lines.
func envPairLines(env map[string]string) []string {
	lines := make([]string, 0, len(env))
	for _, key := range sortedEnvKeys(env) {
		lines = append(lines, key+"="+shellQuote(env[key]))
	}
	return lines
}

// procdScriptFor is what Install renders. threshold(3600s) timeout(5s) retry(5)
// matches OpenWrt's own defaults for always-on daemons: respawn up to 5 times
// within an hour.
func procdScriptFor(def Definition) procdTemplateData {
	return procdTemplateData{
		Name:     def.Name,
		ExecLine: shellQuote(def.ExecPath) + " " + argLine(def.Args),
		Restart:  def.Restart,
		Respawn:  "3600 5 5",
		Env:      envPairLines(def.Env),
	}
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

// -----------------------------------------------------------------------------
// launchd
// -----------------------------------------------------------------------------

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

// launchdPlistFor is what Install renders. The log paths are joined with
// path.Join, not filepath.Join, so the plist is identical whatever host
// rendered it — launchd only ever sees Unix paths.
func launchdPlistFor(def Definition, label, logDir string) launchdPlistData {
	data := launchdPlistData{
		Label:    xmlEscape(label),
		Exec:     xmlEscape(def.ExecPath),
		Restart:  def.Restart,
		Throttle: int(def.RestartSec.Seconds()),
		Stdout:   xmlEscape(path.Join(logDir, def.Name+".log")),
		Stderr:   xmlEscape(path.Join(logDir, def.Name+".err")),
	}
	for _, arg := range def.Args {
		data.Args = append(data.Args, xmlEscape(arg))
	}
	if def.WorkingDir != "" {
		data.WorkingDir = xmlEscape(def.WorkingDir)
	}
	for _, key := range sortedEnvKeys(def.Env) {
		data.Env = append(data.Env, launchdEnv{K: xmlEscape(key), V: xmlEscape(def.Env[key])})
	}
	return data
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

// -----------------------------------------------------------------------------
// rc.d (FreeBSD / OpenBSD / NetBSD)
// -----------------------------------------------------------------------------

type rcdTemplateData struct {
	Name     string
	Rcvar    string
	ConfPath string
	Exec     string
	ArgsLine string
}

// rcvarFor makes a valid shell identifier out of a service name, because rc.d
// reads the enable flag from that variable: '-' and '.' become '_'.
func rcvarFor(name string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_")
	return replacer.Replace(name) + "_enable"
}

// rcdScriptFor is what Install renders; confPath is /etc/rc.conf on FreeBSD and
// /etc/rc.conf.local elsewhere.
func rcdScriptFor(def Definition, confPath string) rcdTemplateData {
	return rcdTemplateData{
		Name:     def.Name,
		Rcvar:    rcvarFor(def.Name),
		ConfPath: confPath,
		Exec:     def.ExecPath,
		ArgsLine: argLine(def.Args),
	}
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
