# goservice

把 Go 守护进程装成系统原生服务，并让它自己升级 —— 不依赖 CLI 框架、日志库或任何应用层依赖。

代码脱胎于 cloudflared 的 `service install` / `service uninstall` 与自更新逻辑，但与之解耦，并针对实际会遇到的平台做了加固：OpenWrt/厂商路由器、各类 BSD、Windows Server。

**语言：[中文](README.md) · [English](README.en.md)**

```
goservice/
├── svc/       安装 / 卸载 / 启动 / 停止 / 重启 / 状态
├── update/    版本检查、流式下载与校验、解包、原子替换、重启
└── daemon/    把上面两者合成 main 里的一次调用（service <verb>）
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

### 二进制放哪：集成方的事

`ExecPath` 留空时取**当前运行的那个二进制**（解析符号链接后）。这只是便利，不是推荐做法：`svc` 不会替你复制任何文件，从 `~/Downloads` 或 `/tmp/build` 跑一次 `install`，unit 就永久指向那里，目录一清服务就起不来。

生产安装应由安装脚本或包管理器先把二进制放到稳定路径（`/usr/local/bin/<name>`、`%ProgramFiles%\<Name>\<name>.exe`），再显式设置 `Definition.ExecPath`，并让 `update.Target.Path` 指向同一个文件——两处不一致时，自更新会替换到另一个路径去。

还有一点：已安装后从另一个路径再执行一次 `install`，Linux/BSD 会把 unit 改指新路径，Windows 不会（`BinaryPathName` 不在更新的字段里）。想换路径，先 `uninstall` 再 `install` 更可预期。

### 集成：main 里一次调用

`daemon` 包把“认服务命令、装/管服务、自更新”合成一个调用，配置就是三个字段：

```go
// mydaemon service install|uninstall|start|stop|restart|status|update
handled, err := daemon.Handle(ctx, flag.Args(), daemon.Config{
	Service: svc.Definition{
		Name:      "mydaemon",
		ExecPath:  "/usr/local/bin/mydaemon", // 见上文“二进制放哪”
		Restart:   true,
		AutoStart: true,
	},
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon"}, // 有它才有 service update
	Version: version,
})
if handled {
	return err // 服务命令已经执行完
}
// 不是服务命令（比如 `mydaemon run`），继续自己的逻辑
```

几个默认值省掉了大部分胶水：

- `Service.Args` 为空时，`service <verb>` **之后**的参数成为服务命令行（`mydaemon service install -p 8080 bash` → unit 里就是 `-p 8080 bash`）；`Config.Strip` 指定哪些 flag 不该带进去（如 `-d`）。
- 自更新的落点 `Config.Target.Path` 默认取 `Service.ExecPath`，不会出现“更新替换了另一个文件”。
- 服务没装时执行 `service update` 不会报错，只更新二进制，重启那步自动跳过。
- 输出走 `Config.Out`（默认 stdout）：`status` 一行、`update` 一行 `updated a -> b`。

只用其中一个包也行：不引 `daemon`，直接用 `svc.New` / `svc.Do` 或 `update.Updater`。`daemon` 是最厚的一层，它同时依赖 `svc` 和 `update`（也就是会链接 `net/http`），只要服务管理的话别引它。

### 不用 `daemon` 包时

三行等价的底层写法，适合想自己控制流程的场景：

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

- `svc.ServiceCommand` 认 `service <verb>`，返回动词和剩下的参数；`--` 可以逃逸，所以要执行一个真叫 `service` 的命令仍然可以。
- `svc.StripFlags` 只移除 flag 本身、不碰后面的参数，所以值要写成 `-flag=value`。
- `svc.Do` 分发六个动词，`status` 打一行。

如果应用的第一个位置参数就是“要执行的命令”（gottyd 就是），这段要放在“必须有命令”的校验**之前**。不想占用位置参数，也可以用 flag 形态（`-service=install`），`Do` 一样接。

### 指定后端

自动探测基本够用，但有时你就是要那一个：在 systemd 的机器上生成一份 SysV 脚本做对照、探测失败的定制固件上明确走 procd、或者在测试里只验证某个后端的产物。

```go
manager, err := svc.NewWith("sysv", def)   // 空字符串等价于 svc.New
fmt.Println(svc.Backends())                // 这个构建里有哪些后端，按名字排序
```

可用的名字随目标平台变化：Linux 是 `systemd` / `procd` / `sysv`，macOS 是 `launchd`，BSD 是 `rcd`，Windows 是 `windows`。写错名字会得到一条列出全部可选值的错误；没有已知服务管理器的平台返回 `svc.ErrUnsupported`。

有一点要知道：`Definition` 的部分字段只有部分后端认，其余后端会**静默忽略**（这是刻意的取舍，装服务不该因为多填了一个字段就失败，但排查时要心里有数）。

| 字段 | systemd | sysv | procd | launchd | rcd | windows |
| --- | --- | --- | --- | --- | --- | --- |
| `Env` | ✔ | ✔ | ✔ | ✔ | ✘ | ✘ |
| `WorkingDir` | ✔ | ✘ | ✘ | ✔ | ✘ | ✘ |
| `LogDir` | ✘（进 journal） | ✔ | ✘（进 syslog） | ✔ | ✘ | ✘ |

## 自更新

一次更新只有三件事，`Updater` 的字段就是按这三步排的：

| 阶段 | 字段 | 管什么 |
| --- | --- | --- |
| 发现 | `Source` `Version` `Channel` | 最新版本从哪来、当前是什么、走哪个通道 |
| 安装 | `Auth` `Target` | 凭据、装到哪个路径、大小上限、校验、解包 |
| 收尾 | `Restart` `Managed` | 装完怎么重启、什么时候禁止自更新 |

```go
up := &update.Updater{
	Source:  &update.GitHubSource{Owner: "you", Repo: "mydaemon", Auth: update.Auth{Token: cfg.Token}},
	Version: version,
	Target: update.Target{
		Path:   "/usr/local/bin/mydaemon", // 装到哪；留空 = 当前运行的二进制
		Unpack: update.TarGz("mydaemon"),  // 发行包是压缩包时才需要
	},
	Restart: manager.Restart, // 替换完成后由新二进制接管
}
result, err := up.Update(ctx) // 无新版本时返回 update.ErrUpToDate
```

`Auth` 只有一处：查询 release 和下载产物都用它。`Target.Path` 建议显式写成**已安装的那个路径**（和 `svc.Definition.ExecPath` 一致），不要依赖默认值——否则从临时目录跑一次更新，就把二进制替换到临时目录去了。

### 四种 Source，选一个

**① GitHub / 自建 Gitea / Forgejo**

```go
&update.GitHubSource{
	Owner:   "lxxilyd",
	Repo:    "gottyd",
	BaseURL: "https://git.example.com/api/v1", // 自建时填，GitHub 留空
	Auth:    update.Auth{Token: cfg.Token},    // 私有库：查询和下载都用
}
```

release API 路径与 Gitea/Forgejo 完全一致，分页参数 `per_page` / `limit` 的差异已内部处理。GitHub 私有库会自动改用 asset API 地址并带 `Accept: application/octet-stream` —— `browser_download_url` 会跨域跳转，Go 会丢掉 `Authorization`，直接下是 404。

**② GitLab（gitlab.com 或自建）**

GitLab 的 API 形状和 GitHub 完全不同（项目按 ID 或完整路径寻址、文件挂在 `assets.links` 下、描述字段叫 `description`），所以是独立的源：

```go
&update.GitLabSource{
	Project: "group/gottyd",                 // 数字 ID 也行；斜杠自动编码成 %2F
	// BaseURL: "https://gitlab.example.com", // 不写 /api/v4 会自动补上
	Auth: update.Auth{Token: cfg.Token},     // 默认走 PRIVATE-TOKEN
	// Auth: update.Auth{Token: oauth, Header: "Authorization"}, // OAuth2 token
	// AssetPattern: func(req update.Request) string { return ... }, // 数字 ID 时必须给
}
```

下载优先取 `direct_asset_url`（GitLab 自己签的永久下载路径，私有库靠它），没有才退回 `url`。默认资产名是 `<项目名>_<os>_<arch>`，项目名取路径最后一段；用数字 ID 时拼不出名字，请设 `AssetPattern`。`IncludePrerelease` 走 `/releases` 列表，未来日期的预发布也可见，列表同样会往下找第一个含本平台资产的 release。

**③ 任意下载地址**

```go
&update.URLSource{
	URL:     "https://dl.example.com/gottyd_linux_amd64",
	Version: "v2026.10.23",                 // 不填则从链接里按三段数字提取
	Auth:    update.Auth{Token: cfg.Token}, // 可选
	// SHA256: "...",                       // 可选：有校验值就校
}
```

⚠️ 这里的链接必须是“指向最新”的稳定地址。**如果链接里带版本号，`Update()` 会永远判定已是最新**（版本比较相等），这种场景要用 `Apply` 直接覆盖安装：

```go
rel, err := up.Source.Latest(ctx, update.CurrentPlatform(version, ""))
result, err := up.Apply(ctx, rel) // 跳过版本比较
```

**④ JSON feed（版本由服务端决定）**

```go
&update.StaticSource{
	URL:  "https://example.com/latest.json",
	Auth: update.Auth{Token: cfg.Token}, // feed 和产物共用同一个凭据
}
// {"version": "1.4.2", "url": "https://example.com/app", "sha256": "..."}
// url 以 "/" 开头时按 feed 所在域解析，方便挂在 CDN 后面
```

### 凭据（Auth）

一个类型、一处配置，查询和下载两跳通用：

```go
update.Auth{Token: "glpat-x"}                          // Authorization: Bearer glpat-x
update.Auth{Token: "glpat-x", Header: "PRIVATE-TOKEN"} // PRIVATE-TOKEN: glpat-x
update.Auth{Token: "token 0123"}                       // 已带 scheme 时原样发送
```

自己写 `Source` 时不用手动拼 header：`Updater.Auth` 会自动加到下载请求上；也可以在自己的 `Source` 里用 `Auth.HTTPHeader()` 得到同样的结果并放进 `Release.Header`。`Target.Client` 可换成带 mTLS 或自定义代理的 client。

### 解包（tar.gz / zip）

默认把下载到的字节直接当二进制用。发行包是压缩包时，给 `Target.Unpack` 挂个钩子——它拿到的只是 `io.Reader`，写出来的才是要装的可执行文件：

```go
Target: update.Target{
	// 从 app_1.2.3_linux_amd64/app 里取，也接受扁平命名的 app_linux_amd64
	Unpack: update.TarGz("app"),
	// Unpack: update.Zip("app.exe"),
},
```

`TarGz` / `Zip` 先按完整路径匹配成员名，再按 basename 兜底；找不到时报错会列出包里实际的文件名。`Zip` 需要产物是 `*os.File`（`Updater` 传进去的始终是），因为 zip 要随机读取。

钩子签名就是一行，任何格式都能自己接：

```go
Unpack: func(rel update.Release, artifact io.Reader, w io.Writer) error {
	// 解一个自己定义的封装、挑 CPU 变体、甚至只做完整性搬运
}
```

校验发生在解包**之前**：`Release.SHA256` 是产物的摘要，不是里面二进制的摘要。

### 大小上限

`Target.MaxBytes` 默认 512 MiB，负值表示不限。下载是流式的（`io.Copy` + 边下边算 SHA-256 写 `<binary>.part`），不是 `io.ReadAll`，所以 40 MB 的二进制只占几十 KB 的缓冲——路由器上尤其重要。

### 集成清单

按顺序确定下面几项，接完就能用（用 `daemon.Handle` 的话，1~5 一次调用就包含在内）：

1. **Source** —— 有 git 仓库就用 `GitHubSource` / `GitLabSource`；只有一个文件地址就用 `URLSource`（记住带版本号的链接只能 `Apply`）；想让服务端决定版本就用 `StaticSource`。
2. **Auth** —— 只有私有源才需要填，一处配置两跳通用。
3. **Target.Path** —— 必须是安装后的稳定路径，和 `svc.Definition.ExecPath` 保持一致；生产环境不要留空。
4. **Target.Unpack / Target.Verify** —— 发行包是压缩包就挂解包钩子；有签名就再加一道校验。
5. **Restart** —— 接 `manager.Restart`，否则新二进制装上了但没人拉起。
6. **启动时** —— 调一次 `update.CleanupOldBinary("")`（Windows 清理 `.old`，其它平台空操作）。

### 其余行为

替换会先把产物落到同目录的 `<binary>.part`，校验/解包后成为 `<binary>.new`，最后 rename 到位，中间任何一步崩掉都不会留下半个二进制。release 带 SHA-256 时自动校验；`Target.Verify` 拿的是**产物路径**（不是内存里的字节，大文件也能用），适合再做一道签名校验。`Updater.Managed` 返回真时拒绝自更新，把升级交给包管理器。`update.CleanupOldBinary` 用于清理 Windows 自更新留下的 `.old` 文件，启动时调一次即可。

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

模块路径是 `github.com/lxxilyd/goservice`。本地开发加 `go.work` 或 replace 指令即可：

```
require github.com/lxxilyd/goservice v0.0.0
replace github.com/lxxilyd/goservice => ../goservice
```

## 测试

```
go test ./...                                  # 单元测试，不需要任何权限
GOOS=linux GOARCH=mipsle go build ./...        # 以及其它目标平台
```

`svc` 有 6 个后端，其中 5 个要真正写出文件（Windows 走 SCM API，不生成文件），所以它们的渲染函数放在没有 build tag 的 `render.go` 里，由 `svc/testdata/*.golden` 逐个比对生成的 unit、SysV/procd/rc.d 脚本和 plist —— 在任何一台机器上跑测试都能发现“改了模板改坏了”。`TestRenderIsStable` 还会反复渲染，防止 `Definition.Env`（map）导致每次安装写出不同顺序的文件。

改模板后重新生成：

```
UPDATE_GOLDEN=1 go test ./svc
```

`examples/agent` 是一个可直接跑的守护进程，能把自己装成服务并自更新，是了解接线方式最快的入口：

```
agent backends                                                                    # 本平台可用的后端
agent -backend sysv service install                                               # 指定后端写文件
agent service status                                                              # running pid=... 一行
agent -repo you/repo service update                                               # GitHub
agent -repo you/repo -base-url https://git.example.com/api/v1 -token T service update  # 自建 Gitea
agent -gitlab group/app -base-url https://gitlab.example.com service update       # GitLab
agent -url https://dl.example.com/app_linux_amd64 -remote-version v1.2.3 service update # 任意地址
agent -repo you/repo -unpack app service update                                   # 从 tar.gz/zip 取 app
```
