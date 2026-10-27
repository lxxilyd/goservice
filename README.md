# goservice

把 Go 守护进程装成系统原生服务 —— 不依赖 CLI 框架、日志库或任何应用层依赖。

**语言：[中文](README.md) · [English](README.en.md)**

```
goservice.Handle(...)   模块根包：main 里一次调用，命令行是 `service <verb>`
svc/                    服务：安装 / 卸载 / 启动 / 停止 / 重启 / 状态
```

## 快速开始

```go
func main() {
	handled, err := goservice.Handle(context.Background(), flag.Args(), goservice.Config{
		Service: svc.Definition{
			Name:      "mydaemon",
			ExecPath:  "/usr/local/bin/mydaemon", // 显式写，别用默认值
			Args:      []string{"run"},           // 服务管理器只跑这一条命令行
			Restart:   true,
			AutoStart: true,
		},
		Run: work, // Windows 服务管理器直接拉起进程时跑它；控制台和 Linux 上不会调
	})
	if handled {
		if err != nil {
			log.Fatal(err)
		}
		return // install / status 等已执行完
	}

	// 不是服务命令，跑自己的逻辑
}
```

导入 `github.com/lxxilyd/goservice`，包名也是 `goservice`。命令行：`mydaemon service install|uninstall|start|stop|restart|status`。

想自己控制流程就直接用 `svc`：`svc.New(def)` + `svc.Do(ctx, manager, verb, os.Stdout)`。

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

## 服务定义要点

- **`ExecPath` 必须显式**：留空时取当前运行的二进制（解析符号链接），这是便利不是推荐。`svc` 不会替你复制文件——从 `/tmp` 或下载目录跑一次 `install`，unit 就永久指向那里。生产安装应先把二进制放到稳定路径（`/usr/local/bin/<name>`、`%ProgramFiles%\<Name>\<name>.exe`），再显式设置 `ExecPath`。
- **换路径重装**：已安装后从另一个路径再 `install`，Linux/BSD 会把 unit 改指新路径，Windows 不会（`BinaryPathName` 不在更新字段里）。想换路径，先 `uninstall` 再 `install` 更可预期。
- **`Run` 必须给**：Windows 上服务是由 SCM 直接拉起进程的，不是用户敲 `service start`。这时候进程要向 SCM 注册并汇报状态，否则约 30 秒后 SCM 会判定“服务未及时响应”并杀掉它。传了 `Run` 之后 `Handle` 自动处理这段，`main` 里不用再单独判断。
- **参数快照**：`Service.Args` 为空时，`service <verb>` 之后的参数成为服务命令行（`mydaemon service install -p 8080 bash`）；`Config.Strip` 指定哪些 flag 不该带进去（如 `-d`）。要写进服务的 flag 放在 `service <verb>` 之后。
- **Windows**：事件日志是尽力而为（失败不回滚服务）；`CreateService` 不带描述，避免 x/sys 删掉半装的服务；`Status().Detail` 显示真实 `BinaryPathName`，用于排查“装了但没跑”；服务跑在 session 0，配置和凭据请用绝对路径。
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
agent service uninstall
```
