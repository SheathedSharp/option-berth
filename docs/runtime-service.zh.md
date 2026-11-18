# 本地后台服务

[English](runtime-service.md) | 中文

Runtime Service 是 oberth 在本机运行的后台进程，负责观察服务、维护本次运行记录和持久存储。它与项目配置中的 API、后台任务不是同一个东西。“Oberth Runtime”指运行管理这一领域，不是另一个需要安装的进程。

## 查看或控制后台

下面是几个独立操作，不要当作一段脚本盲目连着运行。`run --detach` 启动后台进程；`stop` 停的是这个后台进程，不是项目服务。要停止项目服务，用 `down`。

```sh
oberth runtime service status --json
oberth runtime service run --detach
oberth runtime service log
oberth runtime service stop
```

没有后台运行时，`status --json` 会返回非零退出码。`doctor` 检查安装与观察权限；`doctor --fix` 可能修改状态，请先看清准备修什么。[命令参考](cli/infrastructure.zh.md)维护完整参数和返回字段。

## 用户级常驻服务

[安装脚本](install.zh.md)把 runtime service 注册成用户级单元：macOS 是 launchd LaunchAgent（`com.sheathedsharp.option-berth.runtime`），Linux 是 systemd 用户单元（`option-berth-runtime.service`）。该单元运行 `oberth runtime service run --persistent`，因此一个 runtime service 会跨登录、跨 shell 常驻，而不是由当时需要它的命令顺手拉起。它不需要管理员权限。

单元安装之后，它就拥有这个 runtime service，CLI 改为请求它而不是自己起进程：`runtime service restart` 重启该单元，发现没有后台在跑的写命令也会请求单元启动。supervisor 拒绝时原样报错，CLI 不会退回自己起一个游离进程——两个所有者会争同一个 socket 和同一把锁。单元文件存在时 `run --detach` 也会拒绝并指向 `runtime service restart`，原因相同。

`install.sh --no-service`（或 `BERTH_INSTALL_NO_SERVICE=1`）跳过注册，没有单元的机器保持下面描述的行为。Windows 没有这样的单元：那里的 runtime service 仍由 `run --detach` 启动，并遵循空闲超时。

## 为什么有时没有后台

手动启动的后台遵循默认 30 分钟空闲超时：没有保持连接的客户端、没有活动运行，且一段时间没有操作时，它可能正常退出，因此 socket 不存在不等于启动崩溃。由用户级单元启动的后台带 `--persistent`，永远不会因空闲退出；这种情况下 socket 不存在说明单元没在运行。两种情况都要看后台自身日志和 supervisor 的日志，区分“没启动过”“主动停止”“空闲退出”和“启动失败”，不能凭一个检查项猜原因。

只有观察能力预检允许时，项目操作才可能自动启动匹配的后台。如果沙箱不能查看监听端口，不应再从里面启动一个同样看不见的替代后台；应在普通终端明确启动。不要放宽沙箱，也不要靠反复启动当作修复。

## 数据库与版本切换

一个数据库同时只能由一个后台拥有；换 socket，或让两个 `BERTH_HOME` 指向同一个文件，都不能绕过。在线历史和 doctor 的数据库查询通过拥有它的后台读取。`--no-runtime-service` 不会暗中连接后台：数据库被占用时明确返回 `runtime_service_busy`，离线读取仍然只读。

协议 3 有意移除了旧的后台命令和端点名。替换旧安装之前，先用匹配旧后台的 CLI 正常停止它。不要清锁、删 socket、重置数据库或终止身份不明的进程。精确的名称与配置对应关系见[切换合同](cli/infrastructure.zh.md#runtime-service-协议-3-切换)。
