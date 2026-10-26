# goservice

Install a Go daemon as a native system service, and keep it up to date — with no
CLI framework, logging library or application dependency.

**Language: [中文](README.md) · [English](README.en.md)**

```
goservice.Handle(...)   root package: one call in main, `service <verb>` on the CLI
svc/                    service: install / uninstall / start / stop / restart / status
update/                 self-update: version check, streamed download, verify, unpack, replace
```

## Quick start

```go
func main() {
	_ = update.CleanupOldBinary("") // Windows: drop the .old from the last update

	handled, err := goservice.Handle(context.Background(), flag.Args(), goservice.Config{
		Service: svc.Definition{
			Name:      "mydaemon",
			ExecPath:  "/usr/local/bin/mydaemon", // set it, do not rely on the default
			Args:      []string{"run"},           // the service runs exactly this line
			Restart:   true,
			AutoStart: true,
		},
		Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"}, // enables service update
		Version: version, // injected at build time: -ldflags "-X main.version=1.2.3"
	})
	if handled {
		if err != nil {
			log.Fatal(err)
		}
		return // install / status / update already ran
	}

	// not a service command: do the daemon's own work
}
```

Import `github.com/lxxilyd/goservice`; the package is also called `goservice`. CLI: `mydaemon service install|uninstall|start|stop|restart|status|update`.

Need only half of it? Skip the root package: `svc.New` + `svc.Do` for service
management, `update.Updater` for self-update. The root package links both,
`net/http` included.

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

## Self-update

`Source` decides where versions come from, `Auth` is configured once and used for
both the lookup and the download, `Target` decides where it goes and how it is
verified.

| Source | Use it when |
| --- | --- |
| `GitHubSource` | GitHub, or self-hosted Gitea/Forgejo (`BaseURL` accepts the site root or `.../api/v1`; `/api/v1` is appended when missing) |
| `GitLabSource` | gitlab.com or self-hosted GitLab (`Project: "group/app"`, `PRIVATE-TOKEN` by default) |
| `URLSource` | any download URL; it must point at the **latest** build — a versioned link only works with `Apply` |
| `StaticSource` | the server publishes `{"version","url","sha256"}` and owns the version |

```go
up := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon", Auth: update.Auth{Token: cfg.Token}},
	Version: version,
	Target: update.Target{
		Path:   "/usr/local/bin/mydaemon", // defaults to the running binary; set it in production
		Unpack: update.TarGz("mydaemon"),  // only when the release is a tar.gz/zip
	},
	Restart: manager.Restart, // the new binary takes over after the replace
}
result, err := up.Update(ctx) // update.ErrUpToDate when nothing to do
```

- **Credentials**: `update.Auth{Token: "..."}` sends `Authorization: Bearer`; a
  GitLab PAT uses `PRIVATE-TOKEN` automatically; `Header` overrides the name.
- **Asset names**: the default pattern is `<repo>_<os>_<arch>`, and `-` vs `_`
  plus `.exe`/`.tar.gz` style suffixes are ignored, so `gottyd-windows-amd64.exe`
  just matches. Matching never crosses architectures (`gottyd_linux_arm` will not
  pick `gottyd_linux_arm64`); use `AssetPattern` when the naming is irregular.
- **Unpacking**: `update.TarGz("member")` / `update.Zip("member")`, or your own
  `func(rel, io.Reader, io.Writer) error`. Verification runs before unpacking.
- **Size and safety**: `Target.MaxBytes` defaults to 512 MiB (negative disables),
  the download is streamed so memory stays flat; `Target.Verify` receives the
  artifact path for a signature check.
- **Replacement**: `<binary>.part` → verified/unpacked into `<binary>.new` →
  renamed into place, so a crash never leaves half a binary.

## Windows and embedded notes

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
agent -repo you/repo service update             # self-update from GitHub
agent -repo you/repo -unpack app service update # take app out of a tar.gz/zip
```
