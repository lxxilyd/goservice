# goservice

把 Go 守护进程装成系统原生服务，并让它自己升级 —— 不依赖 CLI 框架、日志库或任何应用层依赖。

**语言：[中文](README.md) · [English](README.en.md)**

```
goservice.Handle(...)   模块根包：main 里一次调用，命令行是 `service <verb>`
svc/                    服务：安装 / 卸载 / 启动 / 停止 / 重启 / 状态
update/                 自更新：版本检查、流式下载、校验、解包、原子替换
```

## 快速开始

```go
func main() {
	_ = update.CleanupOldBinary("") // Windows 清理上次更新留下的 .old

	handled, err := goservice.Handle(context.Background(), flag.Args(), goservice.Config{
		Service: svc.Definition{
			Name:      "mydaemon",
			ExecPath:  "/usr/local/bin/mydaemon", // 显式写，别用默认值
			Args:      []string{"run"},           // 服务管理器只跑这一条命令行
			Restart:   true,
			AutoStart: true,
		},
		Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"}, // 有它才有 service update
		Version: version, // 构建时 -ldflags "-X main.version=1.2.3"
	})
	if handled {
		if err != nil {
			log.Fatal(err)
		}
		return // install / status / update 等已执行完
	}

	// 不是服务命令，跑自己的逻辑
}
```

导入 `github.com/lxxilyd/goservice`，包名也是 `goservice`。命令行：`mydaemon service install|uninstall|start|stop|restart|status|update`。

只想用一半就别引根包：只要服务管理用 `svc.New` + `svc.Do`，只要自更新用 `update.Updater`（根包会同时链接两者，包含 `net/http`）。

## 平台支持

| 平台 | 后端 | 说明 |
| --- | --- | --- |
| Linux + systemd | `systemd` | unit 在 `/etc/systemd/system`，`Definition.Notify` 为真时用 `Type=notify` |
| OpenWrt / CPE | `procd` | `/etc/init.d/<name>`，`USE_PROCD=1`，日志进 syslog，自动拉起 |
| 无 systemd 的 Linux | `sysv` | 纯 POSIX sh；有 `update-rc.d` / `chkconfig` / `insserv` 就用上 |
| macOS | `launchd` | root 用 LaunchDaemon，普通用户用 LaunchAgent |
| FreeBSD / OpenBSD / NetBSD | `rcd` | `rc.subr` 脚本，`/usr/local/etc/rc.d`（OpenBSD 为 `/etc/rc.d`） |
| Windows | `windows` | SCM、失败自动恢复、事件日志 |

验证过的目标：`linux/{amd64,arm64,arm,mips,mipsle}`、`darwin/{amd64,arm64}`、`freebsd|openbsd|netbsd/{amd64,arm64}`、`windows/{amd64,386}`。

后端默认探测，也可以指定：`svc.NewWith("sysv", def)`，`svc.Backends()` 列出当前构建可用的名字。`Env` / `WorkingDir` / `LogDir` 只有部分后端支持（Windows 不支持这三项，`rcd` 全部不支持，systemd 的日志走 journal），其余后端**静默忽略**。

## 自更新

`Source` 决定版本从哪来，`Auth` 一处配置同时用于查询和下载，`Target` 决定装到哪、怎么校验。

| 源 | 用在什么场景 |
| --- | --- |
| `GitHubSource` | GitHub，或自建 Gitea/Forgejo（`BaseURL` 填站点根或 `.../api/v1` 都行，会自动补 `/api/v1`） |
| `GitLabSource` | gitlab.com 或自建 GitLab（`Project: "group/app"`，默认 `PRIVATE-TOKEN`） |
| `URLSource` | 任意下载地址；**链接必须指向最新**，带版本号的只能用 `Apply` 覆盖安装 |
| `StaticSource` | 服务端出一份 `{"version","url","sha256"}` JSON，由它决定版本 |

```go
up := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon", Auth: update.Auth{Token: cfg.Token}},
	Version: version,
	Target: update.Target{
		Path:   "/usr/local/bin/mydaemon", // 默认取当前二进制，生产环境不要留空
		Unpack: update.TarGz("mydaemon"),  // 发行包是 tar.gz/zip 时挂解包钩子
	},
	Restart: manager.Restart, // 替换完成后由新二进制接管
}
result, err := up.Update(ctx) // 无新版本时返回 update.ErrUpToDate
```

- **凭据**：`update.Auth{Token: "..."}` 走 `Authorization: Bearer`；GitLab PAT 自动用 `PRIVATE-TOKEN`；自定义头名填 `Header`。
- **资产名**：默认匹配 `<repo>_<os>_<arch>`，`-`/`_`、`.exe`/`.tar.gz` 这类差别不敏感，所以 `gottyd-windows-amd64.exe` 直接命中；匹配不会跨架构（`gottyd_linux_arm` 不会选到 `gottyd_linux_arm64`），命名不规则时用 `AssetPattern` 明确指定。
- **解包**：`update.TarGz("成员名")` / `update.Zip("成员名")`，或自己写 `func(rel, io.Reader, io.Writer) error`。校验发生在解包之前。
- **体积与安全**：`Target.MaxBytes` 默认 512 MiB（负值不限），下载是流式的，不占内存；`Target.Verify` 拿到产物路径，可做签名校验。
- **替换**：先写 `<binary>.part`，校验/解包后成为 `<binary>.new`，最后 rename，中途崩掉不留半个二进制。

## Windows 与嵌入式

- **Windows**：事件日志是尽力而为（失败不回滚服务）；`CreateService` 不带描述，避免 x/sys 删掉半装的服务；`Status().Detail` 会显示真实 `BinaryPathName`，用于排查“装了但没跑”；服务跑在 session 0，配置和凭据请用绝对路径。
- **嵌入式**：`sysv` 只写 POSIX sh（无 bashisms、无 `readlink -f`，存活判断用 `kill -0`）；`LogDir` 不可写时回退 `/tmp`；`/etc/rc.common` 存在时优先 `procd`；进程查找扫 `/proc/<pid>/exe`，不依赖 `pgrep`/`pidof`。

## 测试与示例

```
go test ./...                            # 单元测试，无需权限
GOOS=linux GOARCH=mipsle go build ./...  # 交叉编译
UPDATE_GOLDEN=1 go test ./svc            # 改了模板后重新生成 testdata
```

`svc` 的 5 个写文件后端由 `svc/testdata/*.golden` 比对产物，改动模板不会悄悄改坏别的平台。

`examples/agent` 是可直接运行的守护进程：

```
agent backends                                  # 本平台可用的后端
agent -backend sysv service install             # 指定后端写文件
agent service status                            # running pid=...
agent -repo you/repo service update             # 从 GitHub 自更新
agent -repo you/repo -unpack app service update # 从 tar.gz/zip 取 app
```
