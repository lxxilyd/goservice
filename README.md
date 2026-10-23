# goservice

把 Go 守护进程装成系统原生服务，并让它自己升级 —— 不依赖 CLI 框架、日志库或任何应用层依赖。

代码脱胎于 cloudflared 的 `service install` / `service uninstall` 与自更新逻辑，但与之解耦，并针对实际会遇到的平台做了加固：OpenWrt/厂商路由器、各类 BSD、Windows Server。

**语言：[中文](README.md) · [English](README.en.md)**

```
goservice/
├── svc/       安装 / 卸载 / 启动 / 停止 / 重启 / 状态
└── update/    版本检查、校验后下载、原子替换、重启
```

## 平台支持

| 平台 | 后端 | 说明 |
| --- | --- | --- |
| Linux + systemd | `systemd` | unit 写入 `/etc/systemd/system`；`Definition.Notify` 为真时用 `Type=notify` |
| OpenWrt / CPE 固件 | `procd` | 生成 `/etc/init.d/<name>`，`USE_PROCD=1`，日志进 syslog，自动拉起 |
| 无 systemd 的 Linux | `sysv` | 纯 POSIX sh 脚本；有 `update-rc.d` / `chkconfig` / `insserv` 就顺手用上 |
| macOS | `launchd` | root 用 LaunchDaemon，普通用户用 LaunchAgent |
| FreeBSD / OpenBSD / NetBSD | `rcd` | `rc.subr` 脚本，放 `/usr/local/etc/rc.d`（OpenBSD 为 `/etc/rc.d`） |
| Windows | `windows` | 服务控制管理器、失败自动恢复、事件日志 |

已验证可编译：`linux/{amd64,arm64,arm,mips,mipsle}`、`darwin/{amd64,arm64}`、`freebsd|openbsd|netbsd/{amd64,arm64}`、`windows/{amd64,386}`。
`go vet ./...` 与 `go test ./...` 均干净。

## 快速开始

```go
def := svc.Definition{
	Name:         "mydaemon",
	DisplayName:  "My Daemon",
	Args:         []string{"run", "--config", "/etc/mydaemon.yml"},
	Restart:      true,               // 失败后自动重启
	AutoStart:    true,               // 开机自启
	Dependencies: []string{"network-online.target"},
}

// 仅 Windows：如果本进程是由 SCM 拉起来的，就地服务并返回。
if handled, err := svc.RunServiceIfNeeded("mydaemon", run); handled {
	return err
}

manager, err := svc.New(def)          // 为本机挑选合适的后端
err = manager.Install(ctx)            // 写 unit、enable、启动
status, err := manager.Status(ctx)    // State、PID、后端细节
```

`svc.NotifyReady()` / `NotifyStopping()` 在非 Linux 平台是空操作，只要设了 `Definition.Notify` 就可以无条件调用。

## 自更新

```go
updater := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"},
	Version: version,
	Restart: manager.Restart,   // 替换完成后由新二进制接管
}
result, err := updater.Update(ctx)   // 无新版本时返回 update.ErrUpToDate
```

### 自建 Git（Gitea / Forgejo）

`GitHubSource` 不只服务 GitHub：它的 release API 路径与 Gitea/Forgejo 一致，改 `BaseURL` 即可（分页参数 `per_page` / `limit` 的差异已内部处理）。

```go
&update.GitHubSource{
	Owner:   "lxxilyd",
	Repo:    "gottyd",
	BaseURL: "https://git.example.com/api/v1", // 自建 Gitea/Forgejo 的 API 根
	Token:   cfg.Token,                        // 私有库：查询和下载都用它
}
```

`Token` 同时用于查询 release 和下载产物。用 GitHub 私有库时会自动改用 asset API 地址并带 `Accept: application/octet-stream` —— 因为 `browser_download_url` 会跳转到另一个域名，Go 在跨域跳转时会丢掉 `Authorization`，直接下会 404。

### GitLab（gitlab.com 或自建）

GitLab 的 release API 和 GitHub 完全不是一套（项目按 ID 或完整路径寻址、文件挂在 `assets.links` 下、描述字段叫 `description`），所以是独立的源，而不是 `BaseURL` 的开关：

```go
&update.GitLabSource{
	Project: "group/gottyd",                    // 也可以写数字 ID；斜杠会自动编码成 %2F
	// BaseURL: "https://gitlab.example.com",   // 不写 /api/v4 会自动补上
	Token: cfg.Token,                           // PAT / project / group token，查询和下载都用
	// TokenHeader: "Authorization",            // OAuth2 token 时改这里，默认 PRIVATE-TOKEN
	// AssetPattern: func(req update.Request) string { return ... }, // 用数字 ID 时必须给
}
```

下载优先取 `direct_asset_url`（GitLab 自己签的永久下载路径，私有库靠它），没有才退回 `url`。默认资产名是 `<项目名>_<os>_<arch>`，其中项目名取路径最后一段；用数字 ID 时拼不出名字，请设 `AssetPattern`。`IncludePrerelease` 走 `/releases` 列表，未来日期的预发布也一并可见，列表同样会往下找第一个含本平台资产的 release。

### 任意下载地址

想直接给一个下载链接（自建 git 的下载路径、对象存储、CDN、内网文件服务器都行）：

```go
updater := &update.Updater{
	Source: &update.URLSource{
		URL:     "https://git.example.com/lxxilyd/gottyd/releases/download/v2026.10.23/gottyd_linux_amd64",
		Token:   cfg.Token,             // 需要鉴权时填，会带在下载请求上
		// Header: "PRIVATE-TOKEN",     // 自定义鉴权头名，默认 Authorization
		// SHA256: "...",               // 可选：有校验值就校
	},
	Version: version,
	Restart: manager.Restart,
}
```

`Version` 不填时从链接里按“三段数字”的规则提取，例如上面的链接解析出 `v2026.10.23`；提取不到会直接报错，要求显式指定。

如果不希望客户端自己判断版本，可以让服务端出一份 JSON：

```go
&update.StaticSource{URL: "https://example.com/latest.json"}
// {"version": "1.4.2", "url": "https://example.com/app", "sha256": "..."}
// url 以 "/" 开头时按 feed 所在域解析，方便挂在 CDN 后面
```

### 鉴权头

两条通道都能带鉴权，共用一个 `AuthHeader` 构造：

```go
release.Header = update.AuthHeader("", "glpat-xxx")              // Authorization: Bearer glpat-xxx
release.Header = update.AuthHeader("PRIVATE-TOKEN", "glpat-xxx") // PRIVATE-TOKEN: glpat-xxx
release.Header = update.AuthHeader("", "token 0123abcd")          // 已带 scheme 时原样发送
```

优先级：`Release.Header` > `Updater.Header`（后者适合“所有产物共用一个凭据”的场景，自定义 `Source` 也能用）。`Updater.Client` 可换成带 mTLS 或自定义代理的 client。

### 其余行为

替换会先写同目录的 `<binary>.new` 再 rename 到位，崩在中间也不会留下半个二进制。release 带 SHA-256 时自动校验，`Updater.Verify` 再加一道签名校验。`Updater.Managed` 返回真时拒绝自更新，把升级交给包管理器。`update.CleanupOldBinary` 用于清理 Windows 自更新留下的 `.old` 文件，启动时调一次即可。

## Windows Server：这里已经处理掉的坑

cloudflared 在 Windows 服务上踩过一串问题，以下都是有意修掉的：

1. **半装状态（cloudflared #1498，Windows Server 2016）。**
   `eventlog.InstallAsEventCreate` 报 *“registry key already exists”*，原实现在这里把服务删掉了 —— EventLog 源却留着，于是之后每次安装都失败、卸载还报“未安装”。这里事件日志是尽力而为：失败绝不回滚服务，`Uninstall` 也会在服务记录已消失时清掉 EventLog 源。
2. **`CreateService` 因描述设置失败而删掉自己。** x/sys 在后续描述更新失败时会删除服务。所以创建时不带描述，之后再补，失败就忽略。
3. **`ImagePath` 悄悄为空。** 注册时没带参数的服务能启动、没事可做、然后退出，看起来就是“装了但没跑”，事件日志只有一行“服务正在启动”。`Status().Detail` 现在会返回真实的 `BinaryPathName`，命令行一眼可查；`Definition.Args` 就是写进去的东西（x/sys 自 v0.30 起能正确给带空格的路径加引号）。
4. **安装失败后的 `ERROR_SERVICE_MARKED_FOR_DELETE`** 会如实上报，而不是笼统的“拒绝访问”。
5. **提权。** `mgr.Connect` 失败会翻译成“请以管理员身份运行”。
6. **启动顺序。** `Definition.Dependencies` 映射到 SCM 的依赖列表（如 `Tcpip`），相当于 `After=`。

注意服务跑在 session 0，用户配置目录是 `C:\Windows\System32\config\systemprofile`，配置文件和凭据请写绝对路径 —— `WorkingDir` 只改工作目录。

## 嵌入式（路由器 / CPE / OpenWrt）

* `sysv` 后端只写 **POSIX sh**：没有 `{1..10}` 这类 bash 扩展，不用 `readlink -f`，存活判断用 `kill -0` 而不是解析 `ps`。
* 命令行嵌在 shell 函数里而不是预先拼好的字符串，否则引号会原样传给守护进程。
* `LogDir` 不可写（只读 rootfs）时回退到 `/tmp`，不会给守护进程一个用不了的重定向目标。
* `/etc/rc.common` 与 `procd`/`openwrt_release` 同时存在时优先 `procd`，这样固件里那个长得像 SysV 的 `/etc/init.d` 也能拿到 respawn 和 syslog。
* `findProcessByExe` 扫 `/proc/<pid>/exe`，因为多数固件上的 `pgrep`/`pidof` 缺失或是 BusyBox 变种。
* 可交叉编译到 `linux/mips`、`linux/mipsle`、`linux/arm`、`linux/arm64`。

## 在别的模块里使用

模块路径有意取短（`module github.com/lxxilyd/goservice`）。本地开发加 `go.work` 或 replace 指令即可：

```
require github.com/lxxilyd/goservice v0.0.0
replace github.com/lxxilyd/goservice => ../goservice
```

## 测试

```
go test ./...                                  # 单元测试，不需要任何权限
GOOS=linux GOARCH=mipsle go build ./...        # 以及其它目标平台
```

`examples/agent` 是一个可直接跑的守护进程，能把自己装成服务并自更新，是了解接线方式最快的入口：

```
agent -repo you/repo update                                                      # GitHub
agent -repo you/repo -base-url https://git.example.com/api/v1 -token T update    # 自建 Gitea
agent -gitlab group/app -base-url https://gitlab.example.com -token T update      # GitLab
agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 update  # 任意下载地址
```
