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

没有后台运行时，`status --json` 会返回非零退出码。`doctor` 检查安装与观察权限；`doctor --fix` 可能修改状态，请先看清准备修什么。[命令参考](cli.zh.md)维护完整参数和返回字段。

## 为什么有时没有后台

默认空闲超时为 30 分钟。没有保持连接的客户端、没有活动运行，且一段时间没有操作时，后台可能正常退出。因此，socket 不存在不等于启动崩溃。要根据后台自身日志，区分“没启动过”“主动停止”“空闲退出”和“启动失败”，不能凭一个检查项猜原因。

只有观察能力预检允许时，项目操作才可能自动启动匹配的后台。如果沙箱不能查看监听端口，不应再从里面启动一个同样看不见的替代后台；应在普通终端明确启动。不要放宽沙箱，也不要靠反复启动当作修复。

## 数据库与版本切换

一个数据库同时只能由一个后台拥有；换 socket，或让两个 `BERTH_HOME` 指向同一个文件，都不能绕过。在线历史和 doctor 的数据库查询通过拥有它的后台读取。`--no-runtime-service` 不会暗中连接后台：数据库被占用时明确返回 `runtime_service_busy`，离线读取仍然只读。

协议 3 有意移除了旧的后台命令和端点名。替换旧安装之前，先用匹配旧后台的 CLI 正常停止它。不要清锁、删 socket、重置数据库或终止身份不明的进程。精确的名称与配置对应关系见[切换合同](cli.zh.md#runtime-service-协议-3-切换)。
