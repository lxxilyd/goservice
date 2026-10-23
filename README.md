# goservice

Install a Go daemon as a native system service, and keep it up to date — with no
CLI framework, logging library or application dependency.

Extracted from cloudflared's `service install` / `service uninstall` and
self-update code, but decoupled from it and hardened for the platforms that
matter in practice: OpenWrt/vendor routers, the BSDs and Windows Server.

```
goservice/
├── svc/       install / uninstall / start / stop / restart / status
└── update/    version check, verified download, atomic replace, restart
```

## Platform matrix

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux + systemd | `systemd` | unit in `/etc/systemd/system`, `Type=notify` if `Definition.Notify` |
| OpenWrt / CPE firmware | `procd` | `/etc/init.d/<name>` with `USE_PROCD=1`, logs to syslog, respawn |
| Linux without systemd | `sysv` | POSIX sh script, `update-rc.d` / `chkconfig` / `insserv` when present |
| macOS | `launchd` | LaunchDaemon as root, LaunchAgent otherwise |
| FreeBSD / OpenBSD / NetBSD | `rcd` | `rc.subr` script, `/usr/local/etc/rc.d` (OpenBSD: `/etc/rc.d`) |
| Windows | `windows` | Service Control Manager, recovery actions, event log |

Verified builds: `linux/{amd64,arm64,arm,mips,mipsle}`, `darwin/{amd64,arm64}`,
`freebsd|openbsd|netbsd/{amd64,arm64}`, `windows/{amd64,386}`.
`go vet ./...` and `go test ./...` are clean.

## Quick start

```go
def := svc.Definition{
	Name:         "mydaemon",
	DisplayName:  "My Daemon",
	Args:         []string{"run", "--config", "/etc/mydaemon.yml"},
	Restart:      true,               // restarts on failure
	AutoStart:    true,               // enable at boot
	Dependencies: []string{"network-online.target"},
}

// Windows only: if the SCM started us, serve and return.
if handled, err := svc.RunServiceIfNeeded("mydaemon", run); handled {
	return err
}

manager, err := svc.New(def)          // picks the backend for this machine
err = manager.Install(ctx)            // writes the unit, enables and starts it
status, err := manager.Status(ctx)    // State, PID, backend detail
```

`svc.NotifyReady()` / `NotifyStopping()` are no-ops outside Linux, so they can
be called unconditionally when `Definition.Notify` is set.

## Self-update

```go
updater := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"},
	Version: version,
	Restart: manager.Restart,   // the new binary takes over after the replace
}
result, err := updater.Update(ctx)   // update.ErrUpToDate when nothing to do
```

Anything else can be a `Source`: `update.StaticSource` reads a
`{"version","url","sha256"}` JSON document, and the interface is one method.

The replacement is written to `<binary>.new` next to the target and renamed into
place, so a crash never leaves a half written binary. SHA-256 is checked when
the release exposes one; `Updater.Verify` adds a signature check. Set
`Updater.Managed` to refuse self-update when a package manager owns the binary.

## Windows Server: the traps this package already handles

cloudflared has a long history of Windows service bugs; these are the ones that
were fixed here on purpose.

1. **Half installed service (cloudflared #1498, Windows Server 2016).**
   `eventlog.InstallAsEventCreate` fails with *"registry key already exists"* and
   the original code deleted the service in response — leaving the EventLog
   source behind, so every later install failed and uninstall reported "not
   installed". Here the event log is best effort: it never rolls the service
   back, and `Uninstall` removes the EventLog source even when the service
   record is already gone.
2. **`CreateService` deleting itself when the description fails.** x/sys
   deletes the service if the follow-up description update fails. The service is
   therefore created without a description, which is applied afterwards and
   ignored on failure.
3. **A silently empty `ImagePath`.** A service registered without arguments
   starts, finds nothing to do and exits, which looks like "installed but not
   running", and the event log shows only "service starting". `Status().Detail`
   now reports the real `BinaryPathName` so the command line can be verified at
   a glance, and `Definition.Args` is what lands there (x/sys quotes paths with
   spaces correctly since v0.30).
4. **`ERROR_SERVICE_MARKED_FOR_DELETE`** after a failed install is reported as
   such instead of a bare "access denied".
5. **Elevation.** `mgr.Connect` failures are translated into "run as
   Administrator".
6. **Boot order.** `Definition.Dependencies` maps to the SCM dependency list
   (e.g. `Tcpip`), the Windows equivalent of `After=`.

Note that services run in session 0 with a profile of
`C:\Windows\System32\config\systemprofile`, so configure absolute paths for
config files and credentials — `WorkingDir` only changes the CWD.

## Embedded (routers, CPEs, OpenWrt)

* The `sysv` backend writes **POSIX sh only**: no `{1..10}` bashisms, no
  `readlink -f`, liveness via `kill -0` instead of parsing `ps`.
* The command line is embedded in a shell function instead of a pre-quoted
  string, otherwise the quotes reach the daemon as literal characters.
* If `LogDir` is not writable (read-only rootfs) the backend falls back to
  `/tmp` instead of handing the daemon an unusable redirection target.
* `procd` is selected when `/etc/rc.common` plus `procd`/`openwrt_release`
  exist, so vendor firmware that ships a SysV-looking `/etc/init.d` still gets
  procd respawn and syslog.
* `findProcessByExe` scans `/proc/<pid>/exe`, because `pgrep`/`pidof` are
  missing or BusyBox variants on most firmware.
* Cross-compiles for `linux/mips`, `linux/mipsle`, `linux/arm`, `linux/arm64`.

## Using it from another module

The module path is intentionally short (`module goservice`). For local work
either add a `go.work` file or a replace directive:

```
require goservice v0.0.0
replace goservice => ../goservice
```

Change the first line of `go.mod` to a real import path before publishing.

## Tests

```
go test ./...                                  # 20 unit tests, no privileges needed
GOOS=linux GOARCH=mipsle go build ./...        # and any other target
```

`examples/agent` is a runnable daemon that installs itself as a service and
self-updates; it is the shortest way to see the intended wiring.
