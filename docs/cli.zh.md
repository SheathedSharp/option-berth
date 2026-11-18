# oberth 接口参考

[English](cli.md) | 中文

这是一份查询手册，维护命令、JSON、RPC 与配置字段的精确行为。第一次使用先读[快速开始](quickstart.zh.md)；不必从头顺读本页。运行中的帮助以 `oberth <command> --help` 查询，自动生成的 schema 来自 Go 类型。

工作树（worktree）是一份独立项目目录；配置文件（manifest）是 `oberth.yaml`；服务（service）是声明的命令；运行（run）是一次具体启动；Runtime Service 是拥有运行注册和存储的本机后台。进程、监听和健康是不同状态，未知不能写成不存在。

先用[文档导航](index.zh.md)选择任务；下面给出所有命令共同遵守的规则、层表，以及每一层命令各自的页面。模型建议的授权入口见 [Attention](attention.zh.md)。

## 参考页

| 页面 | 内容 |
|---|---|
| [运行事实](cli/facts.zh.md) | `status`、`logs`、`events`、`worktrees` |
| [运行操作](cli/operations.zh.md) | `up`、`restart`、`down`、`start`、`kill` |
| [代码与声明](cli/declaration.zh.md) | `git`、`init`、清单边界与引用 |
| [交接](cli/handoff.zh.md) | `workplace`、`attn`、`agent`、agent stream、原生续接 |
| [基础设施](cli/infrastructure.zh.md) | `doctor`、`runtime service`、`version`、协议 3、`feedback`、首屏 |
| [agent 读取的 JSON 字段](cli/json.zh.md) | `status`、`up`、`restart` 输出里 agent 据此判断的字段 |

## 共通规则

- 命令在当前目录（或显式的路径 / 项目名）上工作。没有清单时，先运行 oberth init：需要清单的命令以 `not_found` 返回，并在 hint 里给出这一步。
- 默认输出给人读；普通 `--json` 时 stdout 恰好只有一个 JSON 值。显式流式命令每行一个值，错误写 stderr。
- 跟随日志和其他流式命令使用 NDJSON：每行一个 JSON 值。
- 退出码：0 成功，1 运行失败，2 参数或选项错误，130 被 Ctrl-C 中断。
- JSON 错误形状为 {"error":{"code":"...","message":"...","hint":"..."}}。
  稳定的 code 包括 invalid_params、not_found、invalid_config、ambiguous、
  permission_denied、runtime_service_busy、observation_empty、observation_unrecognized、
  observation_incomplete、observation_blind、observation_bind_denied 和 internal。
  每个 code 属于三类之一：拒绝表示请求本身不对、原样重发不会成功；条件表示当时
  环境未就绪、重试可能成功；故障表示引擎自身有问题。就绪超时不在其中：
  `up --wait` 把它报成该服务的状态，所以它作为运行时事实传递，而不是传输层错误。
  观察类 code 都不是“没有监听”：
  - observation_empty：采集命令以 0 退出但没有任何输出；由自证探针判定是真空表还是失明。
  - observation_unrecognized：有输出但没有可识别的表头，属于格式问题。
  - observation_incomplete：部分输出行无法解码。可读行照常发布，坏行对应的端口保持未知，
    `observation.state` 为 partial；命令整体失败时仍按失败处理。
  - observation_blind：探针能绑定本地端口，但同一采集器看不到自己的监听。
  - observation_bind_denied：探针连本地端口都不能绑定，从这里启动的服务也无法监听。
  hint 指向离开沙箱重试、升级 option-berth 或运行
  `oberth doctor --only listener_observation` 检查当前进程。
- 每个平台按一条有序的采集链读监听表：macOS 是 `netstat` → `lsof`，Linux 是 `ss` → `netstat`，
  Windows 只有 `netstat -ano`。链首是主采集器，`observation.source` 不写它；只有主采集器读不出、
  由后面的采集器答话时才写那一级的名字。换下一级的条件只有“这一趟没有留下可用行”：工具没装、
  以 0 退出却没打印表，或它自己的诊断信息把这一趟判废且没有任何可读行。已经读到行的那一趟即使
  失败也就此定论——可读行连同判定一起发布，不会被别的工具的表顶掉；打印了表却解不出来属于解码
  问题，同样不换。所有采集器都失败时，保留主采集器的 code 和 hint，其余采集器的原因并入同一句
  `detail`。
- macOS 27 起，由非 Apple 签名父进程（Go、Homebrew Python 等）启动的子进程执行 `netstat`
  会以 0 退出却拿不到 TCP 表。这种“一行都没有”仍按 observation_empty 处理，由自证探针判定；
  探针能看到自己的监听时，本轮由 `lsof` 答话并照常报 observable。`doctor` 的 detail 与
  `runtime service status` 的 `observation` 描述的是同一轮观察。
- 有名字的 source 比主采集器窄，它的 `detail` 句子写出该采集器声明了哪些限制：非 root 的 `lsof`
  看不到其他用户的 socket，root 进程持有的端口可能不在表里，而且它只把双栈通配 socket 报成 `::`，
  不补另一个地址族；Linux `netstat` 同样只把双栈通配 socket 报在一个地址族下。表里出现的行仍是
  事实，但缺失不是：消费者先读 `observation.source`，再判断端口是否空闲。窄表里缺失的端口
  `machine[].listening_known` 为 false，`port: auto` 逐个用绑定实测而不是信任缺失，
  人读的 `status` 打印 `listening unknown`。
- `runtime_service.observe_sources`（默认 1）决定一轮扫描最多读这条链的几级。默认仍是单采集器，
  由 `observation.source` 写明答话的那一级；大于 1 的 N 会被裁到本平台采集链的长度（macOS 和
  Linux 为 2，Windows 为 1），并发布 `observation.sources`——按链顺序列出参与合并的采集器，
  此时 `observation.source` 为空。只有一级的链无论 N 多大都仍是单源读，两个字段都不出现。
  读到行的采集器贡献这些行；跑干净却什么都没看到的只贡献名字；
  硬失败什么都不贡献，所以失败的一级永远不能把没读到的端口变成“没有监听”。合并表一律是窄表：
  表里的行是事实，缺失的端口是未知而不是空闲，`machine[].listening_known` 与有名字的 source
  同一规则，`detail` 写明这轮由哪些采集器合并而来。
- `observation.degraded_since` 与 `observation.recovered_at` 是由 Runtime Service 维护的
  RFC 3339 时间。前者是已发布的观察不再由主采集器整表读出的起始时刻；后者是这样的降级之后
  重新开始健康运行的时刻，并一直保留到下一次降级。`checked_at` 只是最近一次检查的时刻，不是
  降级的起点。直读（`--no-runtime-service`）没有历史，这两个字段都为空。
- --no-runtime-service 让事实读取在本地采集，不连接或启动 Runtime Service；记录及起停 run 的操作仍需要宿主。历史记录只在离线时从同一数据库只读读取；活跃拥有者持有数据库时，直接读取返回 `runtime_service_busy`，不能假装历史为空。在线读取应去掉该参数，通过拥有者宿主查询。
- 不接受任何不被读取的协议字段。协议 schema 由 runtime service 的 Go 类型生成，因此在那里声明的字段是
  每个客户端都可以依赖的承诺；一个没有任何 handler 处理的字段比缺一个更糟，因为调用会成功，
  调用方无法知道它被忽略了。因此方法参数可能携带的每个字段都由本仓库自己的代码读取，
  否则 `internal/runtimesvc/rpc` 让构建失败。撤回一个已发布字段属于协议变更；有意删除的名字记录在
  该包里，使它们不会悄悄回来。

<a id="命令面"></a>

## 按任务查命令

CLI 按 **agent 想做的事**分层，不按内部模块分：事实是「现在是什么状态」，操作是「让它跑起来或
停下来」，代码与声明是「读代码和清单」，交接是「留下或取回工作、把会话交给外部 agent」，
基础设施是运行环境本身。根帮助按这五层分组，新的产品能力只能落在已有层里，不能再把一个动作
塞进另一个命令的选项中。

| 层 | 公开命令 | 边界 |
|---|---|---|
| 事实 | `status` · `logs` · `events` · `worktrees` | 当前项目的声明、实况与变化；本机见过的 checkout 身份 |
| 操作 | `up` · `restart` · `down` · `start` · `kill` | 按清单起停，或明确点名一个低级进程入口 |
| 代码与声明 | `git` · `init` | 只读代码上下文，以及这份 worktree 的清单 |
| 交接 | `workplace` · `agent` · `attn` | 工作记录，把会话交给外部 coding agent；可选的模型评估（默认关闭） |
| 基础设施 | `runtime` · `config` · `version` · `completion` · `doctor` · `feedback` | 后台采集器、安装配置、自检与可选的改进报告 |

`oberth --help` 末尾另有一节 **For agents**，给的是读法而不是命令：一次会话开头的三条读
（`status --json --brief`、`up --wait --json`、`logs <service> --once --json --run current
--first-error`）。它不新增命令，也不改变上面这张层表；`status`、`up`、`logs` 自己的
`--help` 里写着同一套读法的理由。

<a id="核心闭环"></a>
