# goservice

Install a Go daemon as a native system service, and keep it up to date — with no
CLI framework, logging library or application dependency.

Extracted from cloudflared's `service install` / `service uninstall` and
self-update code, but decoupled from it and hardened for the platforms that
matter in practice: OpenWrt/vendor routers, the BSDs and Windows Server.

**Language: [中文](README.md) · [English](README.en.md)**

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

### Self-hosted git (Gitea / Forgejo)

`GitHubSource` is not GitHub-only: the release API path is the same on
Gitea/Forgejo, only `BaseURL` changes — including the `per_page` vs `limit`
difference, which is handled internally.

```go
&update.GitHubSource{
	Owner:   "lxxilyd",
	Repo:    "gottyd",
	BaseURL: "https://git.example.com/api/v1", // API root of the instance
	Token:   cfg.Token,                        // private repository: used for lookup and download
}
```

`Token` covers both the release query and the artifact download. On a private
GitHub repository the download switches to the asset API with
`Accept: application/octet-stream`, because `browser_download_url` redirects to
another host and Go drops `Authorization` on a cross-host redirect — the plain
URL would come back 404.

### GitLab (gitlab.com or self-hosted)

GitLab's release API has nothing in common with GitHub's — projects are
addressed by ID or full path, files live under `assets.links`, and the notes
field is `description` — so it is a source of its own rather than a `BaseURL`
switch:

```go
&update.GitLabSource{
	Project: "group/gottyd",                   // or a numeric ID; slashes become %2F for you
	// BaseURL: "https://gitlab.example.com",  // "/api/v4" is appended when missing
	Token: cfg.Token,                          // PAT / project / group token, lookup and download
	// TokenHeader: "Authorization",           // for an OAuth2 token, default PRIVATE-TOKEN
	// AssetPattern: func(req update.Request) string { return ... }, // required with a numeric ID
}
```

The download prefers `direct_asset_url` (GitLab's own permanent download path,
which is what makes a private project work) and falls back to `url`. The default
asset name is `<project>_<os>_<arch>`, where `<project>` is the last path
segment — that is why a numeric ID needs `AssetPattern`. `IncludePrerelease`
switches to the `/releases` list, which also surfaces future dated releases, and
it keeps walking down until a release carries an asset for this platform.

### Any download URL

Hand it a link — a self-hosted git download path, an object store, a CDN, an
internal file server:

```go
updater := &update.Updater{
	Source: &update.URLSource{
		URL:   "https://git.example.com/lxxilyd/gottyd/releases/download/v2026.10.23/gottyd_linux_amd64",
		Token: cfg.Token,          // optional, sent with the download
		// Header: "PRIVATE-TOKEN",  // custom auth header name, default Authorization
		// SHA256: "...",            // optional, checked when present
	},
	Version: version,
	Restart: manager.Restart,
}
```

When `Version` is empty it is read from the link by looking for three
dot-separated numbers — the URL above yields `v2026.10.23`. If there is nothing
to read the source returns an error asking for an explicit version.

If you would rather have the server decide the version, publish a JSON feed:

```go
&update.StaticSource{URL: "https://example.com/latest.json"}
// {"version": "1.4.2", "url": "https://example.com/app", "sha256": "..."}
// a url starting with "/" is resolved against the feed host, so a CDN works
```

### Auth headers

Both paths authenticate through the same helper:

```go
release.Header = update.AuthHeader("", "glpat-xxx")               // Authorization: Bearer glpat-xxx
release.Header = update.AuthHeader("PRIVATE-TOKEN", "glpat-xxx")  // PRIVATE-TOKEN: glpat-xxx
release.Header = update.AuthHeader("", "token 0123abcd")          // sent as-is when it has a scheme
```

Precedence: `Release.Header` over `Updater.Header` — the latter is the place for
one credential shared by every artifact, and works with a custom `Source` too.
`Updater.Client` accepts your own client for mTLS or a proxy.

### Everything else

The replacement is written to `<binary>.new` next to the target and renamed into
place, so a crash never leaves a half written binary. SHA-256 is checked when
the release exposes one; `Updater.Verify` adds a signature check. Set
`Updater.Managed` to refuse self-update when a package manager owns the binary.
`update.CleanupOldBinary` removes the `.old` file a Windows self-update leaves
behind.

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

The module path is intentionally short (`module github.com/lxxilyd/goservice`).
For local work either add a `go.work` file or a replace directive:

```
require github.com/lxxilyd/goservice v0.0.0
replace github.com/lxxilyd/goservice => ../goservice
```

## Tests

```
go test ./...                                  # unit tests, no privileges needed
GOOS=linux GOARCH=mipsle go build ./...        # and any other target
```

`examples/agent` is a runnable daemon that installs itself as a service and
self-updates; it is the shortest way to see the intended wiring:

```
agent -repo you/repo update                                                     # GitHub
agent -repo you/repo -base-url https://git.example.com/api/v1 -token T update   # self-hosted Gitea
agent -gitlab group/app -base-url https://gitlab.example.com -token T update    # GitLab
agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 update # any download URL
```
