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
├── update/    version check, streamed download, unpack, atomic replace, restart
└── daemon/    the two above as a single call in main (service <verb>)
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

### Where the binary lives is the integrator's job

An empty `ExecPath` means **the binary that is running right now** (symlinks
resolved). That is a convenience, not a recommendation: `svc` never copies
anything, so installing from `~/Downloads` or `/tmp/build` writes a unit that
points there forever, and cleaning that directory takes the service down.

A production install puts the binary at a stable path first (`/usr/local/bin/<name>`,
`%ProgramFiles%\<Name>\<name>.exe`) — via the installer, the package manager or a
copy step — then sets `Definition.ExecPath` explicitly and points
`update.Target.Path` at the same file. When the two disagree, self-update
replaces a different file than the one the service runs.

One more: re-running `install` from another path rewrites the unit on
Linux/BSD, while Windows keeps the original `BinaryPathName` (it is not in the
updated fields). To move a service, `uninstall` then `install` is predictable on
every platform.

### Integrating: one call in main

The `daemon` package folds "recognise the service command, install/manage the
service, self-update" into a single call, configured with three things:

```go
// mydaemon service install|uninstall|start|stop|restart|status|update
handled, err := daemon.Handle(ctx, flag.Args(), daemon.Config{
	Service: svc.Definition{
		Name:      "mydaemon",
		ExecPath:  "/usr/local/bin/mydaemon", // see "Where the binary lives"
		Restart:   true,
		AutoStart: true,
	},
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"}, // enables service update
	Version: version,
})
if handled {
	return err // the service command already ran
}
// not a service command (e.g. "mydaemon run"), carry on
```

The defaults remove most of the glue:

- When `Service.Args` is empty, the arguments **after** `service <verb>` become the
  service command line (`mydaemon service install -p 8080 bash` installs
  `-p 8080 bash`); `Config.Strip` names the flags that must not be copied (`-d`).
- `Config.Target.Path` defaults to `Service.ExecPath`, so an update can never
  replace a different file than the service runs.
- `service update` on a binary that is not installed as a service still works;
  the restart step is skipped instead of failing.
- Output goes to `Config.Out` (default stdout): one line for `status`, one for
  `update`.

Using just one package is still fine: skip `daemon` and call `svc.New` /
`svc.Do` or `update.Updater` directly. `daemon` is the thick layer — it depends
on both `svc` and `update`, so it links `net/http`; a daemon that only needs
service management should not import it.

### Without the daemon package

The same thing in three helpers, for callers that want their own flow:

```go
// mydaemon service install|uninstall|start|stop|restart|status
if verb, rest, ok := svc.ServiceCommand(flag.Args()); ok {
	manager, err := svc.New(svc.Definition{
		Name:      "mydaemon",
		ExecPath:  "/usr/local/bin/mydaemon",
		Args:      svc.StripFlags(rest, "d"),
		Restart:   true,
		AutoStart: true,
	})
	if err != nil {
		return err
	}
	return svc.Do(ctx, manager, verb, os.Stdout)
}
```

- `svc.ServiceCommand` recognises `service <verb>` and returns the verb plus the
  remaining arguments; `--` opts out, so a command literally called `service`
  can still be run.
- `svc.StripFlags` removes the flag token only, never the value after it, so
  write values attached (`-flag=value`).
- `svc.Do` dispatches the six verbs; `status` prints one line.

If the first positional argument is the command to run (gottyd does this), place
that block **before** the "a command is required" check. An application that
cannot spare the positional can use the flag form (`-service=install`) instead —
`Do` takes the same input.

### Choosing a backend

Detection is usually right, but sometimes you want that specific one: render a
SysV script on a systemd box to compare, force procd on vendor firmware whose
detection is wrong, or exercise a single backend in a test.

```go
manager, err := svc.NewWith("sysv", def)   // "" behaves like svc.New
fmt.Println(svc.Backends())                // what this build offers, sorted
```

The names depend on the target: `systemd` / `procd` / `sysv` on Linux,
`launchd` on macOS, `rcd` on the BSDs and `windows` on Windows. A wrong name
returns an error listing the alternatives; a platform with no known service
manager returns `svc.ErrUnsupported`.

One thing to know: some `Definition` fields are only honoured by some backends
and are **silently ignored** by the rest. Installing a service should not fail
because a field is unused, but it is worth knowing when debugging.

| Field | systemd | sysv | procd | launchd | rcd | windows |
| --- | --- | --- | --- | --- | --- | --- |
| `Env` | ✔ | ✔ | ✔ | ✔ | ✘ | ✘ |
| `WorkingDir` | ✔ | ✘ | ✘ | ✔ | ✘ | ✘ |
| `LogDir` | ✘ (journal) | ✔ | ✘ (syslog) | ✔ | ✘ | ✘ |

## Self-update

An update is three phases, and the `Updater` fields follow them:

| Phase | Fields | What they decide |
| --- | --- | --- |
| discovery | `Source` `Version` `Channel` | where the newest release comes from, what is running, which channel |
| install | `Auth` `Target` | the credential, the destination, the size cap, verification, unpacking |
| lifecycle | `Restart` `Managed` | what runs after the replace, when self-update is forbidden |

```go
up := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon", Auth: update.Auth{Token: cfg.Token}},
	Version: version,
	Target: update.Target{
		Path:   "/usr/local/bin/mydaemon", // where it goes; empty = the running binary
		Unpack: update.TarGz("mydaemon"),  // only when the release is an archive
	},
	Restart: manager.Restart, // the new binary takes over after the replace
}
result, err := up.Update(ctx) // update.ErrUpToDate when nothing to do
```

`Auth` is configured once and covers both hops. Set `Target.Path` explicitly to
the **installed** path (the same value as `svc.Definition.ExecPath`) — leaving it
empty means "wherever this process was started from", so an update run out of a
temp directory replaces the binary in that temp directory.

### Four sources, pick one

**① GitHub / self-hosted Gitea / Forgejo**

```go
&update.GitHubSource{
	Owner:   "lxxilyd",
	Repo:    "gottyd",
	BaseURL: "https://git.example.com/api/v1", // self-hosted only; empty for GitHub
	Auth:    update.Auth{Token: cfg.Token},    // private repository: lookup and download
}
```

The release API path is the same on Gitea/Forgejo, including the `per_page` vs
`limit` difference, which is handled internally. On a private GitHub repository
the download switches to the asset API with `Accept: application/octet-stream`,
because `browser_download_url` redirects to another host and Go drops
`Authorization` on a cross-host redirect — the plain URL would come back 404.

**② GitLab (gitlab.com or self-hosted)**

GitLab's release API has nothing in common with GitHub's — projects are
addressed by ID or full path, files live under `assets.links`, and the notes
field is `description` — so it is a source of its own:

```go
&update.GitLabSource{
	Project: "group/gottyd",               // or a numeric ID; slashes become %2F for you
	// BaseURL: "https://gitlab.example.com", // "/api/v4" is appended when missing
	Auth: update.Auth{Token: cfg.Token},   // goes in PRIVATE-TOKEN by default
	// Auth: update.Auth{Token: oauth, Header: "Authorization"}, // OAuth2 token
	// AssetPattern: func(req update.Request) string { return ... }, // required with a numeric ID
}
```

The download prefers `direct_asset_url` (GitLab's own permanent download path,
which is what makes a private project work) and falls back to `url`. The default
asset name is `<project>_<os>_<arch>`, where `<project>` is the last path
segment — that is why a numeric ID needs `AssetPattern`. `IncludePrerelease`
switches to the `/releases` list, which also surfaces future dated releases, and
it keeps walking down until a release carries an asset for this platform.

**③ Any download URL**

```go
&update.URLSource{
	URL:     "https://dl.example.com/gottyd_linux_amd64",
	Version: "v2026.10.23",                 // read from the link when empty
	Auth:    update.Auth{Token: cfg.Token}, // optional
	// SHA256: "...",                       // optional, checked when present
}
```

When `Version` is empty it is read from the link by looking for three
dot-separated numbers; if there is nothing to read the source returns an error
asking for an explicit version.

⚠️ The link must be a **stable pointer to the latest build**. If it carries a
version number, `Update()` will consider itself up to date forever (the versions
compare equal) — that case needs `Apply`, which skips the comparison:

```go
rel, err := up.Source.Latest(ctx, update.CurrentPlatform(version, ""))
result, err := up.Apply(ctx, rel)
```

**④ JSON feed (the server decides the version)**

```go
&update.StaticSource{
	URL:  "https://example.com/latest.json",
	Auth: update.Auth{Token: cfg.Token}, // covers the feed and the artifact
}
// {"version": "1.4.2", "url": "https://example.com/app", "sha256": "..."}
// a url starting with "/" is resolved against the feed host, so a CDN works
```

### Credentials (Auth)

One type, configured once, used by both hops:

```go
update.Auth{Token: "glpat-x"}                          // Authorization: Bearer glpat-x
update.Auth{Token: "glpat-x", Header: "PRIVATE-TOKEN"} // PRIVATE-TOKEN: glpat-x
update.Auth{Token: "token 0123"}                       // sent as-is when it has a scheme
```

A custom `Source` does not have to build headers by hand: `Updater.Auth` is
applied to the download, and `Auth.HTTPHeader()` gives a source the same header
to put in `Release.Header`. `Target.Client` accepts your own client for mTLS or
a proxy.

### Unpacking (tar.gz / zip)

By default the downloaded bytes are the binary. When the release is an archive,
set `Target.Unpack`: it sees the artifact as an `io.Reader` and writes the
executable to install.

```go
Target: update.Target{
	// finds app inside app_1.2.3_linux_amd64/, or a flat app_linux_amd64
	Unpack: update.TarGz("app"),
	// Unpack: update.Zip("app.exe"),
},
```

`TarGz` and `Zip` match the member by full path first, then by base name, and a
miss reports the entries the archive actually holds. `Zip` needs the artifact as
an `*os.File` (which is what `Updater` always passes), because a zip is read
through a random-access index.

The hook is one signature, so any format fits:

```go
Unpack: func(rel update.Release, artifact io.Reader, w io.Writer) error {
	// unwrap your own envelope, pick a CPU variant, or just copy the bytes
}
```

Verification runs **before** unpacking: `Release.SHA256` is the digest of the
artifact, not of the binary inside it.

### Size limit

`Target.MaxBytes` defaults to 512 MiB; a negative value disables the limit. The
download is streamed (`io.Copy` into `<binary>.part` while hashing), not
`io.ReadAll`, so a 40 MB binary costs a small buffer instead of 40 MB of RAM —
which is what makes this usable on a router.

### Integration checklist

Decide these six things and the wiring is done (`daemon.Handle` covers 1-5 in a single call):

1. **Source** — a git host → `GitHubSource` / `GitLabSource`; a single file → `URLSource` (a versioned link only works with `Apply`); a server-decided version → `StaticSource`.
2. **Auth** — only for private sources, one place for both hops.
3. **Target.Path** — the installed, stable path, matching `svc.Definition.ExecPath`; do not leave it empty in production.
4. **Target.Unpack / Target.Verify** — unpack hook when the release is an archive, plus a signature check if you sign.
5. **Restart** — wire `manager.Restart`, otherwise the new binary lands but nothing starts it.
6. **At startup** — call `update.CleanupOldBinary("")` once (removes `.old` on Windows, a no-op elsewhere).

### Everything else

The artifact lands in `<binary>.part` next to the target, becomes
`<binary>.new` once verified and unpacked, and is renamed into place, so a crash
at any point never leaves a half written binary. SHA-256 is checked when the
release exposes one; `Target.Verify` receives the **artifact path** (not the
bytes in memory, so it works for large files) for a signature check. Set
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

The module path is `github.com/lxxilyd/goservice`. For local work either add a
`go.work` file or a replace directive:

```
require github.com/lxxilyd/goservice v0.0.0
replace github.com/lxxilyd/goservice => ../goservice
```

## Tests

```
go test ./...                                  # unit tests, no privileges needed
GOOS=linux GOARCH=mipsle go build ./...        # and any other target
```

Five of the six `svc` backends write files for real (Windows goes through the
SCM API), so their renderers live in `render.go` without a build tag and are
compared against `svc/testdata/*.golden` — the unit, the SysV/procd/rc.d scripts
and the plist. That means a broken template is caught by `go test ./svc` on any
machine, not only on the platform that would consume it. `TestRenderIsStable`
renders repeatedly to catch `Definition.Env` (a map) shuffling the output
between installs.

Regenerate after an intentional change:

```
UPDATE_GOLDEN=1 go test ./svc
```

`examples/agent` is a runnable daemon that installs itself as a service and
self-updates; it is the shortest way to see the intended wiring:

```
agent backends                                                                  # backends this platform offers
agent -backend sysv service install                                             # write a specific backend's files
agent service status                                                            # one line: running pid=...
agent -repo you/repo service update                                             # GitHub
agent -repo you/repo -base-url https://git.example.com/api/v1 -token T service update  # Gitea
agent -gitlab group/app -base-url https://gitlab.example.com service update     # GitLab
agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 service update
agent -repo you/repo -unpack app service update                                 # tar.gz/zip
```
