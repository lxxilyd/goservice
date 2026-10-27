# goservice

Install a Go daemon as a native system service — with no CLI framework, logging
library or application dependency.

**Language: [中文](README.md) · [English](README.en.md)**

```
goservice.Handle(...)   root package: one call in main, `service <verb>` on the CLI
svc/                    service: install / uninstall / start / stop / restart / status
```

## Quick start

```go
func main() {
	handled, err := goservice.Handle(context.Background(), flag.Args(), goservice.Config{
		Service: svc.Definition{
			Name:      "mydaemon",
			ExecPath:  "/usr/local/bin/mydaemon", // set it, do not rely on the default
			Args:      []string{"run"},           // the service runs exactly this line
			Restart:   true,
			AutoStart: true,
		},
		Run: work, // used when the Windows SCM launches the process; never called in a console
	})
	if handled {
		if err != nil {
			log.Fatal(err)
		}
		return // install / status already ran
	}

	// not a service command: do the daemon's own work
}
```

Import `github.com/lxxilyd/goservice`; the package is also called `goservice`.
CLI: `mydaemon service install|uninstall|start|stop|restart|status`.

Prefer your own flow? Use `svc` directly: `svc.New(def)` and
`svc.Do(ctx, manager, verb, os.Stdout)`.

## Platforms

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux + systemd | `systemd` | unit in `/etc/systemd/system`, `Type=notify` when `Definition.Notify` |
| OpenWrt / CPE | `procd` | `/etc/init.d/<name>`, `USE_PROCD=1`, logs to syslog, respawn |
| Linux without systemd | `sysv` | POSIX sh only; uses `update-rc.d` / `chkconfig` / `insserv` when present |
| macOS | `launchd` | LaunchDaemon as root, LaunchAgent otherwise |
| FreeBSD / OpenBSD / NetBSD | `rcd` | `rc.subr` script in `/usr/local/etc/rc.d` (OpenBSD: `/etc/rc.d`) |
| Windows | `windows` | SCM, recovery actions, event log |

Verified targets: `linux/{amd64,arm64,arm,mips,mipsle}`, `darwin/{amd64,arm64}`,
`freebsd|openbsd|netbsd/{amd64,arm64}`, `windows/{amd64,386}`.

Backends are detected, or forced with `svc.NewWith("sysv", def)`; `svc.Backends()`
lists what this build offers. `Env` / `WorkingDir` / `LogDir` are only honoured by
some backends (Windows supports none of the three, `rcd` all three missing,
systemd logs to the journal) and are **silently ignored** elsewhere.

## Service definition notes

- **Set `ExecPath` explicitly**: empty means "the binary running right now"
  (symlinks resolved), which is a convenience, not a recommendation. `svc` never
  copies anything, so installing from `/tmp` or a download directory writes a
  unit pointing there forever. Put the binary at a stable path first
  (`/usr/local/bin/<name>`, `%ProgramFiles%\<Name>\<name>.exe`), then set it.
- **Moving a service**: re-running `install` from another path rewrites the unit
  on Linux/BSD while Windows keeps the original `BinaryPathName` (it is not in
  the updated fields). `uninstall` then `install` is predictable everywhere.
- **`Run` is required**: on Windows the SCM launches the process itself rather
  than a user typing "service start", and the process then has to register with
  the dispatcher and report status — without that the SCM kills it after ~30
  seconds ("service did not respond in a timely fashion"). `Handle` covers it
  whenever `Run` is set, so `main` needs no extra branch.
- **Argument snapshot**: when `Service.Args` is empty, the arguments after
  `service <verb>` become the service command line
  (`mydaemon service install -p 8080 bash`); `Config.Strip` names the flags that
  must not be copied (`-d`). Flags meant for the service go after the verb.
- **Windows**: the event log is best effort (it never rolls the service back);
  `CreateService` is called without a description so x/sys cannot delete a
  half-installed service; `Status().Detail` reports the real `BinaryPathName`,
  which is how "installed but not running" is diagnosed; services run in session
  0, so config and credentials need absolute paths.
- **Embedded**: `sysv` writes POSIX sh only (no bashisms, no `readlink -f`,
  liveness via `kill -0`); `LogDir` falls back to `/tmp` when the rootfs is
  read-only; `procd` wins when `/etc/rc.common` exists; process lookup scans
  `/proc/<pid>/exe` instead of relying on `pgrep`/`pidof`.

## Tests and example

```
go test ./...                            # unit tests, no privileges needed
GOOS=linux GOARCH=mipsle go build ./...  # cross-compile
UPDATE_GOLDEN=1 go test ./svc            # regenerate testdata after a template change
```

The five `svc` backends that write files are checked against
`svc/testdata/*.golden`, so a template change cannot silently break another
platform. `examples/agent` is a runnable daemon:

```
agent backends                                  # backends this build has
agent -backend sysv service install             # write a specific backend's files
agent service status                            # running pid=...
agent service uninstall
```
