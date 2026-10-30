# oberth CLI 契约

这份文档描述产品主线：一个 worktree 的清单、运行记录和运行实况、起停和日志，并行开发所需的
只读事件流，以及留给下一个接手的人或 agent 的工作记录。其余命令以 oberth <command> --help
为准，不在这里另建一套契约。

无参数运行 `oberth` 显示首屏：品牌标记与字标、一句定位、本机版本、三条最常用命令（status / up / down）
和到完整帮助的指引；它是指路牌不是手册，分组命令清单只在 `oberth --help`。版本行在发布版本检查
成功时带 `(latest)` 或 `(latest version vX.Y.Z)`；检查未知时只打印本机版本，不把“没查到”写成
“最新”。发现更新时，首屏底部给出对应平台的安装命令和 `oberth daemon stop`。标记由
`brand/build-option-berth.py` 从客户端几何源生成到 `engine/internal/cmd/mark_gen.go`，
不手写；字标是轮廓字，终端里用粗体文字表示。

标记按品牌自己的分工上两色：两条轨用墨色（深色终端用纸色），胶囊用陶土色。涂成一色时
胶囊会和两个轨帽并成三个一样的块，看起来像一张脸，所以这个分工是语义的一部分，不是配色
偏好。truecolor 下用 `#23262B` / `#F8F6F2` 与 `#A8543E` / `#DB9378`，`COLORFGBG` 报告
深色背景时取后一组；256 色终端取最接近的槽位（235 / 255 / 131 / 174）；8 色终端下轨保持
终端自身前景色，胶囊退化为 yellow。`NO_COLOR` 下不着色。

## 共通规则

- 命令在当前目录（或显式的路径 / 项目名）上工作。没有清单时，先运行 oberth init。
- 默认输出给人读；普通 `--json` 时 stdout 恰好只有一个 JSON 值。显式流式命令每行一个值，错误写 stderr。
- 跟随日志和其他流式命令使用 NDJSON：每行一个 JSON 值。
- 退出码：0 成功，1 运行失败，2 参数或选项错误，130 被 Ctrl-C 中断。
- JSON 错误形状为 {"error":{"code":"...","message":"...","hint":"..."}}。
  稳定的 code 包括 invalid_params、not_found、invalid_config、ambiguous、
  permission_denied、timeout、daemon_busy、observation_empty、
  observation_unrecognized、observation_incomplete、observation_blind、
  observation_bind_denied 和 internal。观察类 code 都不是“没有监听”：
  - observation_empty：采集命令以 0 退出但没有任何输出；由自证探针判定是真空表还是失明。
  - observation_unrecognized：有输出但没有可识别的表头，属于格式问题。
  - observation_incomplete：部分输出行无法解码。可读行照常发布，坏行对应的端口保持未知，
    `observation.state` 为 partial；命令整体失败时仍按失败处理。
  - observation_blind：探针能绑定本地端口，但同一采集器看不到自己的监听。
  - observation_bind_denied：探针连本地端口都不能绑定，从这里启动的服务也无法监听。
  hint 指向离开沙箱重试、升级 option-berth 或运行
  `oberth doctor --only listener_observation` 检查当前进程。
- --no-daemon 让读命令在本地直接采集；需要记录运行和起停的命令仍需要 daemon。
  daemon 不可用时，`status` 仍从本地只读历史显示最近的退出原因。

## 命令面

CLI 按 **agent 想做的事**分层，不按内部模块分：事实是「现在是什么状态」，操作是「让它跑起来或
停下来」，代码与声明是「读代码和清单」，交接是「留下或取回工作、把会话交给外部 agent」，
基础设施是运行环境本身。根帮助按这五层分组，新的产品能力只能落在已有层里，不能再把一个动作
塞进另一个命令的选项中。

| 层 | 公开命令 | 边界 |
|---|---|---|
| 事实 | `status` · `logs` · `events` · `worktrees` | 当前项目的声明、实况与变化；本机见过的 checkout 身份 |
| 操作 | `up` · `restart` · `down` · `start` · `kill` | 按清单起停，或明确点名一个低级进程入口 |
| 代码与声明 | `git` · `init` | 只读代码上下文，以及这份 worktree 的清单 |
| 交接 | `workplace` · `agent` | 工作记录，以及把会话交给外部 coding agent |
| 基础设施 | `daemon` · `serve` · `config` · `version` · `completion` · `doctor` | 后台采集器、安装配置与自检 |

## 核心闭环

### oberth status [path] [--json] [--no-mark]

读取一个 worktree 的完整现状：清单声明的服务、运行记录、监听实况、最近退出原因和 git
摘要。path 省略时使用当前目录。命令只在自己的状态文件里留下「上次看」标记，
第二次运行会在 changed 中列出事实变化；--no-mark 只读不移动标记。

服务的 `last_exit.reason` 会区分普通崩溃（`crashed`）和固定端口被其他进程占用
（`port_occupied`），也会标出命令尚未启动就失败的服务（`start_failed`），因此“启动后退出”、
“端口冲突”和“启动前失败”不会混成同一种状态。

人读输出按项目、服务、依赖、代码和失败原因排列，标签是英文的固定宽度列（services ·
depends on · listeners · code · failed · next · drafts · last look），按终端显示宽度对齐。
每个服务单独一行，同时给出声明端口与当前运行实况；服务状态词带颜色（running 绿、
失败红、常规停止 dim），在 NO_COLOR 下退化为纯文字。无端口 worker 会明确标为 no port，
`port: auto` 尚未分配时会显示 auto port unassigned。
只有清单没有声明的监听才单独列出。多个服务同时失败时，失败证据按退出记录逐条显示，并给出
对应的 `oberth logs <service> --once` 下一步。正常退出和被 `down` 停止的服务不进入失败区，
即使强制停止留下了非零信号退出码也一样。

`worktree.services[]` 还带有 `health_status`（`status`、`code`、`reason`、`observed_at`）。
`health` 是声明的规范形式：HTTP 路径，或 `tcp:<port>`，或 `command:<命令行>`；`prepare_inputs` 是
prepare 读取的文件列表，没有声明时为空数组。
运行中的服务同时带 `manifest_hash` 与 `runtime_spec_hash`；两者不一致时
`manifest_runtime_mismatch` 为 `true`，表示当前进程来自旧清单，应该用
`oberth restart --only <service>` 重新应用。hash 只覆盖命令、cwd、端口、health 和依赖等可执行声明，
不包含环境变量内容。

监听表不可信时（例如 agent 沙箱拒绝系统内省），`status` 仍然返回清单、服务、运行记录、
日志路径和最近退出；顶层带 `observation`（`state`、`code`、`detail`、`hint`、`checked_at`）。
完全失明时 `ports` 为空、`port_actual` 为 null；部分行不可读时 `ports` 只含可读行，
未读到的端口不在其中，`observation.state` 为 partial。`up --wait` 在观察不是 observable 时
立即返回观察类错误并保留刚启动的运行，不会等满超时再把它停掉；`logs <service>` 对托管服务
仍使用运行登记的日志路径，不依赖监听表。需要自启 daemon 的写命令会先跑自证探针，失明时
拒绝从当前进程创建 daemon，提示在普通终端运行 `oberth serve --detach`。

JSON 顶层固定包含：

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "identity": "wt_0123456789ab", "services": []},
  "ports": [],
  "git": {"snapshot": {}},
  "drafts": [],
  "exits": [],
  "changed": {"at": "..."}
}
~~~

`worktree.identity` 就是清单里 `${worktree}` 展开成的值。它由 checkout 的规范化根路径
派生，所以同一个 checkout 每次读取都相同；人或 agent 要用 `psql` 连这个 worktree 的库时，
直接读这个字段拼库名，不必自己算哈希。

changed 是第一次运行时缺省的可选字段。ports 仍是给脚本核对用的原始监听事实，不是
独立的产品对象，也不是整台机器的端口清单；exits 只保留最近的退出记录，详细输出用 logs。
`worktree.services[]` 的 `pid`、`run_id` 和 `started_at` 在服务由 option-berth 正在运行时提供，
无端口 worker 也能靠这些字段核对实际运行记录；`log_path` 始终由引擎给出。

当状态里出现确定的异常事件时，JSON 还会带 `attention_path`。它指向
`BERTH_HOME/attention/` 下的一个可重算、可过期的 agent 交接文件；没有当前异常时该字段缺省。
有异常时同时提供 `attention_revision`，它必须与文件中的 `state_revision` 相等，供 agent
在同一份 `status` 响应中核对文件是否仍对应当前事实。
核心文件是 `oberth.attention/v1`，包含 `event_id`、`state_revision`、`worktree_root`、
可选的 `branch`、`generated_at`、`fresh_until`、`first_seen`、`last_seen`、可选的 `recovered_at`、`event`、
可选的完整 `events` 数组、`evidence` 和固定 `options`，不包含 shell 命令或模型调用结果；
`event` 保留为第一个异常的兼容字段，多个同时异常必须以 `events` 为准；
可选的 attention adapter 可以在文件上补充 `assessment`，但 agent 仍须先重新读取 `status --json`，
核对 worktree、`attention_revision` 与 `state_revision`，以及 `fresh_until`，再向用户提出选择。
`assessment.context_id`（若存在）只是 adapter 对当前任务和授权选项 allow-list 的哈希缓存键，
不属于运行事实，也不授予执行权限。
这个文件是派生通知，不是新的运行事实来源；
Jev 不在 daemon 或 `status` 的控制路径里。

可选的 `jev-attention` 适配器不属于 `oberth` 子命令。构建后可以用
`bin/jev-attention`（`mage buildJev`），或在仓库根目录通过
`(cd engine && go run ./cmd/jev-attention)` 调用。agent 应使用一次调用完成状态读取、异常
验证、Jev 评估和 HIL 输出：

~~~sh
# 短任务直接给参数，不必落文件
jev-attention --auto --project "$PWD" \
  --task "keep the API healthy while the integration test runs" \
  --authorize inspect_logs,inspect_port,restart_service,continue_without_action,ask_user_for_context
~~~

上下文文件包含 `task` 和必填的 `authorized_options`，**由 agent 自己写**：机器上没有任何东西
替你生成它，路径也由 agent 选（并行的任务各用一个文件，不要共用一个 `/tmp` 路径）。
`--task` 与 `--authorize` 覆盖文件里的同名字段；两者都没有时 runner 直接报错，不会替 agent
猜一个授权集。适配器只向选定的 Jev provider 发送脱敏的事实摘要，
不会读取日志或起停服务。runner 输出 `clear`、`continue`、`human_required`、`unavailable` 或
`stale`；成功时把校验后的 `assessment` 原子写回同一文件，并在返回前复读状态。
没有可用的环境变量或 GUI 本地 Jev 配置、网络失败、响应无效、文件过期或状态修订变化时，事实文件保持不变。
macOS 设置页写入的 `~/.option-berth/jev.json`（隔离运行时为 `$BERTH_HOME/jev.json`）由
`jev-attention` 自动读取；显式 `OPENROUTER_API_KEY` / `TYPESAFE_API_KEY` 优先，`enabled: false`
会关闭本地配置。
`--auto`、`--session` 和 `--subscribe` 必须显式提供 `authorized_options`；artifact 的 `options` 只是候选集，
不会自动变成 agent 的授权。
需要在任务生命周期内持续发现异常时，agent 应使用 `jev-attention --session --interval 2s`（底层别名仍为
`--subscribe`），
它以 NDJSON 只输出新的 event/revision/assessment 变化，并在取消时结束。
完整参数和隐私边界见
[`jev-adapter.md`](jev-adapter.md)。

`jev-attention --auto` / `--subscribe` 会在 `$BERTH_HOME/quality/events.ndjson`
留下脱敏的本地质量观测。开发 agent 在真实交互后可用
`jev-attention --feedback <json>` 回写人工标签和动作结果，再用
`jev-attention --quality-report [--since 168h]` 查看标注覆盖率、assessment
可用率、路由原因与模型/选项分布、人工升级 precision/recall/F1、Jev 的
`needs_human` / `task_relevant` 概率误差、推荐命中率、动作成功率、解决率和发现延迟。
质量记录不属于 `status` 事实，也不改变任何执行权限；报告的 `sessions` 对象会说明会话、轮询、事件、
Jev 调用、缓存命中和无事件轮询数量；没有真实反馈时指标保持 `null`。

### oberth up [project] [--only name,...] [--wait|--ready] [--wait-timeout duration]
[--allow-outside-home] [--json]

按 oberth.yaml 的 depends_on 顺序启动服务。服务有 `prepare:` 时，先在同一 cwd 直接执行这个前置命令，
成功后才启动长驻的 `cmd:`；前置命令的输出写入该服务日志，失败时服务不会启动。
`prepare_inputs:` 声明该命令读取的输入文件（相对服务 cwd，例如 `[package.json, package-lock.json]`）；
上次 prepare 成功且这些文件内容未变时，本次 `up` 直接跳过 prepare，不再重复安装依赖。指纹只在上次
成功之后记录，输入缺失或读不到时一律重跑；缓存位于 `$BERTH_HOME/prepare`，删除它只会让下次启动更慢。
带端口的依赖等待监听，无端口的 worker 等待其运行记录变为活跃；
已经在运行的服务跳过；每个服务有自己的
日志文件和运行记录。无参数时使用当前目录最近的清单，也可以显式给项目名。

`health:` 有三种写法：HTTP 路径（`/healthz`，缺省形式）、TCP 连接（`health: {tcp: 5432}`，端口必须与
服务声明的端口一致）和命令（`health: {command: pg_isready -h 127.0.0.1 -p ${port}}`）。命令在服务
cwd 中执行，按 `cmd:` 的规则拆成 argv（不经过 shell），支持 `${port}`、`${url}`、
`${<service>.port}` 和 `${worktree}` 引用，退出码 0 视为健康。HTTP 和 TCP 只说明端口可访问；数据库这类端口先开、
协议后就绪的服务用命令式检查。健康结果与依赖等待使用的是同一个事实。

成功时人读输出逐服务报告并给出汇总；`--wait` 等待期间先输出一行 dim 提示再逐行出结果。
--json 输出一个包含 services 和汇总字段的值。每个
`services[]` 都有结构化的 `state`：`started`、`skipped` 或 `failed`，失败时还给出机器可分支的
`reason` 和可直接执行的 `hint`，原有 `error` 保留给人读。`hint` 只指向真实存在的证据：进程起来过
才给 `oberth logs <service> --once`，依赖没就绪时给**依赖**的日志，还没起进程就被拒绝（例如 worktree
在 home 之外）时给拒绝本身带的做法，没有可执行的下一步时不给。
等待判定只认**等待开始之后**记录的退出：`down` 留下的旧退出记录不会被 `up --wait` 读成本轮启动失败。
`--wait`（`--ready` 的别名）会在返回前等待本次选择的服务（包括已经在运行、未重新启动的服务）真正监听；声明了 `health:` 的服务还必须
通过对应的健康检查（命令式检查不要求服务自己监听）。无端口 worker 以运行记录活跃作为 ready。默认不等待，`--wait-timeout` 默认 30s；显式指定时必须为正值且同时启用 `--wait` 或 `--ready`，否则退出 2，不执行启动；
超时的服务状态为 `failed`、原因是 `ready_timeout`，命令退出码为 1。只有本次新启动的超时运行才进入停止清理；
已经存在的运行不会因这次等待失败而被停止或重启。`started` / `skipped` 汇总描述启动动作，
就绪判定失败另外出现在 `errors` 与逐服务 `state` 中。对本次新启动且被清理的运行，
引擎把同一个 `ready_timeout` 写入服务的最后退出事实，因此 `status`、桌面服务行、attention 和事件流
不会把它显示成普通的手动停止。健康事实带有 `status`、`code`、`reason` 与 `observed_at`，
依赖声明了 `health:` 时，后继服务会等待该依赖的健康状态为 `ok`。
依赖等待本身失败时，服务最后退出原因分别是 `dependency_timeout` 或 `dependency_not_ready`，
与启动失败一样会保留在状态、attention 和状态变更中。
任何服务启动失败都会以退出码 1 返回，失败服务仍保留其退出原因供 status 查看。
默认只允许在用户 home 目录内启动服务；明确使用 `--allow-outside-home` 才会放开这个限制。

例如一次部分成功的结果仍是一个 JSON 值：

~~~json
{"services":[
  {"service":"api","state":"started","pid":1234,"port":3000},
  {"service":"worker","state":"failed","reason":"start_failed",
   "error":"exec: \"nosuchcmd\": executable file not found in $PATH",
   "hint":"check the command and its working directory"}
],"started":["api"],"skipped":[],"errors":["worker"]}
~~~

### oberth restart [project] [--only name,...] [--force] [--allow-outside-home] [--json]

先停止再按清单重新启动项目服务。人读输出先报告停止结果，空一行后报告启动结果。
`--only` 只重启点名的服务，依赖和其他服务保持运行；
这类重启保留 `port: auto` 的 worktree 占用，让服务重新拿回原地址。`--force` 跳过优雅停止。
--json 返回一个值，其中 `stopped` 是停止结果，`services` 和汇总字段是重新启动结果。

### oberth down [project] [--worktree path | --identity wt_…] [--force] [--json]

停止该项目清单里的全部服务，动作后确认可释放时交还 `port: auto` 的预留。
无参数时使用当前目录的清单。`--force` 直接使用强制信号，不跳过运行身份或预留释放门禁。
成功时 `--json` 返回停止结果和实际释放数量。项目清单中声明的 `machine:` 依赖只读其
监听状态，`down` 不会停止它们。

`--worktree PATH` 按 checkout 根路径、`--identity wt_…` 按 `${worktree}` 身份停止。
两者都不能和项目名或清单路径同时使用。它们是 worktree 被删除或移动后的明确入口：
目录还在时 daemon 重新读取该清单；目录已不在时按身份账本里记录的组名停止仍属于它的
监听与运行，并交还该 worktree 记录的 `port: auto` 预留。找不到身份返回 not_found，
不会猜一个组。目标已经没有任何运行或预留时，显式身份仍然是一次成功的空停止。
`--identity` 的值来自 `status --json` 的 `worktree.identity` 或 `oberth worktrees`。

按项目名停止是组名作用域：同一仓库的不同 worktree 发布为 `<project>` 与
`<project>@<worktree>`，名字不同时互不影响。两个 checkout 真把同一组名占了（例如不同
仓库目录同名，或两个不同父目录下同名的 linked worktree）时，daemon 的组已经合并，
任何组作用域的停止都会同时打到两者；此时 `down <project>` 和显式身份都返回 ambiguous，
提示改用 `kill` 按端口或 pid 点名，而不是替调用者挑一个。

取消、停止不完整、动作后观察失败、仍有登记运行/监听或预留已被刷新时，不交还未经确认
的预留，返回现有 RPC 错误并提示检查 `status` 和日志后重试。已发送的信号不因这个错误
撤销；`not_found` 不单独证明端口已可释放。无端口运行的真实停止需要准确的出生身份。
底层 dry-run 和 scoped 停止不释放预留；`restart --only` 因此保留原分配。
这不承诺 OS 观察与账本写入原子，也不承诺任意旧版本同时写入同一账本。

### oberth worktrees [--gone] [--json]

只读列出本机 daemon 见过的 `${worktree}` 身份、派生它的 checkout 根路径、最近一次发布的
组名和该路径现在是否还存在。它是身份账本，不是运行事实：行在 checkout 删除或移动后保留，
`exists` 在每次读取时按文件系统重新判断，所以同一路径重新出现时身份也重新存在；
`last_seen` 只是最近一次写入账本（daemon 启动或组名变化）的时间，不是每次扫描的心跳。

`--gone` 只保留 checkout 已不存在的身份，供人或 agent 找到按旧身份留下的数据库和数据目录
（`app_${worktree}`、`.data/${worktree}`）。删除动作仍由人执行：命令不删除账本行、数据库、
数据目录或正在运行的进程；要停止仍在跑的服务用 `oberth down --identity <wt_…>`。

~~~json
{"worktrees":[
  {"identity":"wt_0123456789ab","root":"/work/demo","group":"demo","exists":true,"last_seen":"2026-10-30T09:00:00Z"},
  {"identity":"wt_abcdef012345","root":"/work/old","group":"demo@old","exists":false,"last_seen":"2026-10-28T18:00:00Z"}
]}
~~~

没有 daemon 或还没有账本时返回空列表，不会为了读这份事实启动 daemon 或新建数据库。
daemon 是账本的唯一写者：它在扫描到清单时记录身份，重启后按已记录的根路径补齐；
`status` 等读命令和 `down` 都不写账本。

### oberth logs <service|port> [--once] [--lines N] [--follow] [--ip address] [--json]

按清单里的服务名查看日志；端口形式继续可用。服务有监听端口时，命令读取该进程的日志；
没有端口的 worker 读取该次运行的服务日志文件。默认先输出最近几行并持续跟随；--once 只读
一次后退出。--ip 用来消除同一端口绑定多个地址时的歧义。--json --once 输出一个文档：

~~~json
{"source":"/path/to/service.log","lines":["..."],"truncated":false,"service":"api","run_id":"...","pid":1234,"status":"running"}
~~~

跟随模式使用 NDJSON，每行带有 `source`、`line` 以及可用的服务和运行元数据；一次性读取还会在
服务已退出时给出 `exit_code` 和 `reason`。找不到端口返回 not_found；多个绑定未指定 --ip 返回
ambiguous。

### oberth workplace

交接层里的一份**工作记录**。一个 worktree 下可以有多个 workplace，每个按时间顺序保存其中的
工作条目（block）：引擎跑过的命令，和交给 agent 的每一轮任务。它回答的是「在这里做过什么、
结果如何」——agent 可以读回来接手别人（或自己上一轮）的工作，也可以把引擎看不见的尝试自己
写进去。命令只读写这份记录，不判断、不启动、不恢复。

~~~text
oberth workplace list [--worktree PATH] [--json]
oberth workplace new [name] [--worktree PATH] [--json]
oberth workplace use <name|id> [--worktree PATH] [--json]
oberth workplace show [name|id] [--worktree PATH] [--limit N] [--json]
oberth workplace delete <name|id> [--worktree PATH] [--json]
oberth workplace record --kind shell|agent [flags] [--worktree PATH] [--json]
~~~

`--worktree` 缺省是当前目录；解析成 checkout 的根路径并归一符号链接，同一个 checkout 的
不同写法是同一份记录。既不是 checkout 也没有清单时返回 not_found。

- `list` 按创建顺序列出，`●` 标出当前 workplace；没有 workplace 不是错误，是空列表。
- `new` 建一个并设为当前；同名返回 conflict。
- `use` 切换当前 workplace —— 之后 `oberth start` 和 `oberth agent stream` 记录到它。
- `show` 不带参数读当前 workplace；`--limit N` 只取最新 N 条，`truncated` 标明记录还有更多。
- `delete` 删除一个 workplace 及其全部 block；删掉当前那个时，剩余里最新的接任，没有剩余就
  没有当前 workplace。
- 选择器依次按 精确名字 → 完整 id → id 前缀 → 名字前缀 匹配；同一层命中多个返回 ambiguous，
  不替调用者挑一个。

block 的 `--json` 字段：

~~~json
{"id":"…","workplace":"…","seq":1,"kind":"shell","source":"engine",
 "input":"npm test","status":"completed","run_id":"…","pid":1234,
 "log_path":"…","exit_code":0,"started_at":"…","ended_at":"…"}
~~~

- `kind`：`shell`（引擎跑的命令）或 `agent`（交给 agent 的一轮任务）。
- `source`：`engine`（引擎自己观察到的运行，run_id / pid / log_path / 退出码都是它采的）或
  `reported`（引擎没启动、由上报方写进来的条目）。只有 `reported` 的 block 带 `output` 文本；
  引擎的 block 指向 `log_path`，日志仍是输出的唯一一份。
- `status`：`running` · `completed` · `failed` · `interrupted`。没有观察到结束的条目保持
  running，不会被当成成功；被 `oberth` 或用户停下的运行是 interrupted，不看它留下的信号退出码。
- `seq` 是条目在所属 workplace 里的顺序，每个 workplace 最多保留 500 条，超出时丢弃最旧的。

记录不需要手动操作：

- `oberth start` 把这次运行写进当前 workplace；非附加（`-d`）的运行由 daemon 在记录它的退出时收尾。
- `oberth agent stream` 每一轮写一条：`turn.started` 开，`turn.completed` 按 provider 给的
  outcome 收尾（completed / interrupted / failed）。
- 引擎看不见的工作用 `oberth workplace record` 写进来：agent 自己的尝试（「我改了 X，测试仍然
  失败」）、前端自己的交互式 Shell、原生 agent。同一个 `--id` 再报一次是更新同一条而不是新增，
  所以一次工作的开始与结束可以任意顺序到达；重复的 `running` 不会覆盖已经记下的结果。

边界：`show` 读回的是做过什么，不是把当时的进程叫回来。daemon 不在时读命令仍从数据库直接读；
`new` / `use` / `delete` / `record` 需要一个 daemon（按既有规则自启动），而 `oberth start` 与
`oberth agent stream` 的记录不会为你去起一个 daemon —— 没有 daemon 就没有记录，在 stderr 说明一次。
清单里的服务起停（`up` / `down`）不写工作记录：那是声明的生命周期，不是临时的工作条目。

### oberth init

`init` 只做一件事：根据当前 worktree 的事实生成一份可审阅的 starter
`oberth.yaml`。监听端口和项目文件里的声明只作为注释，留给人决定。`--dry-run` 只预览，
`--json` 给机器读取。

~~~text
oberth init [--dry-run] [--json]
~~~

#### oberth init draft [agent] [--dry-run] [--json] [--progress]

让配置中的本机 agent 起草一份清单。option-berth 不判断、不联网，也不直接改
`oberth.yaml`：成功只产生一份草稿（默认归档在 `~/.option-berth/drafts/`），人读过后
再采纳。已有多个 agent 时，可用位置参数点名 `claude` 或 `codex`；不带参数时使用配置
或 PATH 上唯一可用的 agent。
`--progress` 输出 NDJSON 进度事件，适合客户端在 agent 工作期间显示当前阶段；最后一行是
`type: "done"` 的完整结果。

#### oberth init adopt [draft] [--replace|--merge] [--dry-run] [--diff] [--json]

把人选中的草稿写进当前 worktree 的 `oberth.yaml`。不带路径时采用该项目最新草稿；这一步
才会落盘。已有清单必须明确选择 `--replace` 或 `--merge`。`--dry-run` 只预览将要写入的
完整清单；再加 `--diff` 时，输出当前清单与采纳结果的只读 unified diff（JSON 输出放在
`diff` 字段），不写清单也不移动草稿。

## 各层其余命令

核心闭环之外的命令按层列在这里，契约同样是这份文档：

- oberth doctor：检查清单、daemon、CLI 路径、日志目录和当前进程能否看见监听，给出可操作的修复提示。
  `--only listener_observation` 绑定一个真实本地端口，再用与 `status` 相同的采集器确认能否看到它：
  报告 observable / blind / bind_denied；CLI 模式下同时附带 daemon 的自证结果，daemon 失明也会失败。
  `--only daemon_build_matches` 对比 CLI 与 daemon 的构建提交；同版本号不代表同构建。
  缺少提交身份或状态 RPC 失败会报告 warn，不会标记匹配，也不会自动重启 daemon。
- oberth events：供 agent 订阅本机 daemon 的状态变化。并行联调时 agent 应根据任务中明确给出的
  关系主动建立并消费订阅；人手执行此命令主要用于排障。`--worktree PATH_OR_ID` 会沿已有 Repo 关系包含同仓库
  的其他 worktree；`--repository REPO_ID` 是可重复的加法过滤器，`--workspace NAME` 加入显式的临时联调关系；
  首条 NDJSON 直接是 `{"type":"state.snapshot",...}`，后续每行直接是 `{"type":"state.changed",...}`。
  workspace 首次建立实时订阅时必须同时提供 worktree 或 repository，后续订阅者可以只带 workspace；
  `--once` 只读快照，不登记 workspace 成员。
  RPC 的 filtered `state.subscribe` 使用 `filter: {worktrees, repositories, workspace}`，并且必须把
  `events` 设为 `true`；只取一次快照时使用 `state.snapshot`。
  快照和变更只包含 worktree、服务、端口的运行事实摘要，不包含命令、工作目录、日志或环境变量；
  需要细节时回到对应 worktree 执行 `oberth status --json`。客户端应保存快照或变更中的 `seq`，
  断线重连时把最后应用的序号作为 `after_seq` 传给 `state.subscribe`；daemon 能覆盖时会先返回该序号的
  基准快照，再按顺序补发增量，窗口已过期或状态链不连续时则返回当前完整快照。`oberth events` 会在传输断开或队列溢出后
  自动带着最后的 `seq` 重连；workspace 成员变化仍要以新快照为准，队列窗口过期时也会回到完整快照；
  成员变化会以 `changed: ["workspace_changed"]` 提醒现有订阅者。若连接到旧 daemon，先执行
  `oberth daemon restart` 以取得 `state.scope` 能力。
- oberth kill <port|pid>...：点名停止监听者；--all 是明确的整批操作，仍会确认。
  人读的结果行用读者词汇：`stopped (SIGTERM)` / `stopped (SIGKILL)` / `stopped container`，
  不直接打印信号名；`down` 停无端口 run 时显示服务名（清单里的名字），不是进程表里的解释器名。
  被释放的地址行写作 `released <url>`。`--json` 的 `method` 字段仍是 wire 枚举
  （sigterm/sigkill/docker_stop/none），人读与机器读各用各的词汇表。
- oberth git [path]：在 status 之外按需读取代码摘要、文件列表或 diff；`oberth git graph [path] --json` 返回完整提交拓扑、parent IDs、refs 与 worktree。`--limit N`（N > 0）仍可截断，并在 JSON 里把 `truncated` 标为 true、`limit` 标为 N；`--limit 0` 与省略该参数一样表示完整历史，此时 `limit` 为 0、`truncated` 为 false。Graph 明确标出 `observed_head`，仍保持只读，不 fetch、不刷新 index。`oberth git diff [path] --commit <hex> [--file <repo-relative-path>] --json` 审查指定 commit 相对第一个 parent 的 patch；root commit 相对空树，merge 不使用 combined diff。commit 只接受 7–64 位十六进制 object ID，响应带 `commit` 与 `base` 字段。

`start` 只运行 `oberth start [flags] -- <command> [args...]` 这一种低级入口；日常项目服务
使用 `up` / `down`。`kill` 以端口或 pid 为主语，`--all` 是仍需明确确认的整批例外；旧的
group/session 选择器只为迁移保留，不出现在公开帮助中。

## 基础设施命令

- oberth daemon status|path|stop|restart|log|schema：管理或查看后台采集器。`daemon status --json`
  带 `observation`（`state`、`code`、`detail`、`hint`、`checked_at`），表示 daemon 自己能否看见监听；
  `daemon.hello` 也带同一份结果，客户端不能把失明 daemon 的空端口表当成事实。
- `oberth -v` / `oberth --version`：简洁地打印当前 CLI 版本；`oberth config init|path|edit`、`oberth serve`、`oberth version`、`oberth completion`：配置与运行环境入口。
- `oberth version --json` 输出当前构建的 `version`、`commit`、`built` 和 `platform`，供安装检查脚本确认实际运行的构建。
- `oberth version --check` 读取最新发布标签，报告本机是否落后；`--check --json` 在原来的字段上增加
  `latest`。它只读发布信息，不下载、不安装，也不替换正在运行的二进制。首屏的版本状态复用同一份
  检查结果，成功时缓存 5 分钟、失败时缓存 30 分钟；只有终端会话会自动检查，单次最多等待 1 秒，
  `BERTH_NO_UPDATE_CHECK=1` 可关闭。GitHub 不可达时 `--check` 以非零退出并说明原因，
  首屏只显示本机版本。检查结果缓存在 `BERTH_HOME/update-check.json`
  （默认 `~/.option-berth/update-check.json`），只为首屏复用，不是运行事实。

`oberth version --json` 的 stdout 是一个 JSON 值：

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64"}
~~~

`--check --json` 在这个对象上增加 `latest`：

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64",
 "latest":{"version":"v0.1.1","checked_at":"<UTC timestamp>","update_available":true}}
~~~

`latest.version` 是发布源报告的最新标签；只有本机版本严格更旧时 `update_available` 才为 `true`，
本机版本更新（例如从 `main` 构建）或无法比较（例如 `dev`）都为 `false`。

## 清单和状态的边界

- 清单由人写；agent 只能起草，`oberth init adopt` 才采纳。
- option-berth 只读 git，不提交、不暂存、不切分支。
- machine: 是项目依赖的只读引用；up / down 不会替它起停。
- `worktrees` 的账本只记「身份曾在哪个路径被看到」和最近一次组名；不记服务是否运行，
  也不自动删除 checkout 消失后的数据。
- 监听端口、pid、退出码和日志是 Run 的运行实况。产品不提供整机端口雷达、机器服务注册表、模型
  判断、MCP 服务或自动安装 skill。

### 清单中的引用

`prepare:`、`cmd:` 和 `env:` 的值可以引用端口和 checkout 身份，daemon 在启动服务前展开：

| 引用 | 展开为 |
|---|---|
| `${port}` / `${url}` | 本服务自己的端口 / `http://localhost:<port>` |
| `${<service>.port}` / `${<service>.url}` | 同一清单里另一个服务的端口 / URL |
| `${worktree}` | 当前 checkout 的稳定身份，形如 `wt_0123456789ab` |

`${worktree}` 由清单所在 checkout 的规范化根路径派生：同一个 worktree 每次启动都相同，
主 checkout 和每个 linked worktree 各不相同。它是标识符而不是路径，适合拼进数据库名
（`app_${worktree}`）或相对数据目录（`.data/${worktree}`）。移动 checkout 目录会得到新的
身份，旧数据不会被搬走。当前 checkout 的值直接读 `oberth status --json` 的
`worktree.identity`；本机见过哪些身份、哪些 checkout 已经不在了，用 `oberth worktrees`。
删除数据仍然由人完成，引擎只报告身份与路径的对应关系。

只有上表五种形状是引用；其他 `${...}`（例如 `${HOME}`、`${FOO:-x}`）原样保留。
`${worktree}` 不指向服务，不需要也不产生 `depends_on`；未声明的服务、没有端口的端口引用
仍按清单校验拒绝。可运行示例见 [`examples/worktree-data`](../examples/worktree-data/README.md)。

### 清单解析的歧义拒绝

`oberth.yaml` 只接受一个 YAML 文档；第二个文档（包括空文档）会报错，而不是被忽略。
同一服务不能重复声明 `port`，包括 `auto` 与数字混写；错误带 YAML 行号。
单文档、注释、正常数字端口和 `port: auto` 保持兼容。

### 清单 schema 与未知字段严格模式

清单的 JSON Schema 发布在 `engine/docs/schema/manifest.schema.json`，由 `internal/groups` 的声明
类型生成；`go generate ./...` 同时更新它与协议 schema。schema 拒绝未知键（顶层 `x-` 扩展与
`$schema` 键除外），端口只接受 1–65535 或 `auto`。`oberth init` 与 `oberth init draft` 写出的文件
首行带 `# yaml-language-server: $schema=...` modeline，编辑器据此在起草和采纳之前标出拼错的键；
手写清单可以自行加同一行。

默认解析仍忽略未知键以保持兼容。设 `BERTH_STRICT_MANIFEST=1`（也接受 `true`、`yes`、`on`）后，
未知键变成带行列位置的加载错误，覆盖 CLI、daemon 和 `oberth init adopt` 的所有清单读取路径；
顶层 `x-` 与 `$schema` 仍然是合法元数据。

`doctor --only project_config` 在默认模式下以 warn 报告被忽略的清单键及行列位置，不回显字段值；
`env` 自定义键与顶层 `x-` 扩展不被误报。不改写清单、不执行命令，也不为补充报告元数据连接 daemon。

### oberth agent: external coding-agent handoff

`agent list --json` lists OpenCode, Codex, Claude Code, DeepSeek Harness and Pi without executing them.
`agent plan codex --worktree <path> --json` returns an exact executable/argv/cwd plan; it does not launch,
log in, install hooks, modify configuration, or send a model request. `--prompt-stdin` is plan-only and
avoids putting the initial prompt in shell history. The resulting JSON can contain that prompt: keep it local.
`agent run codex --worktree <path>` explicitly replaces the CLI with the native agent on macOS/Linux.
The agent keeps its own permissions, authentication, tools and conversation history; no approval bypass is added.

Native prompts are supported for OpenCode, Codex, Claude Code and Pi. DeepSeek uses `dsh --profile tui`
for an already installed custom native profile; it is not a shipped-profile guarantee. Use `--mode task`
with a prompt to select its documented headless profile. That one-shot task is not a resumed native conversation.
No implicit “resume last” crosses worktree boundaries. The selected cwd is not an operating-system sandbox.
Windows currently supports plan/list, not native process handoff. Merely finding a binary does not validate login
or a particular installed version; a provider failure remains a provider failure, never a silent shell fallback.


### oberth agent stream

`oberth agent stream <provider> --worktree <path> [--resume-file <path>]` 启动结构化桥进程：
stdin 每行一条命令、stdout 每行一个事件，均为带 `"v":1` 的 NDJSON；stderr 只写诊断。
契约见 `engine/docs/schema/agent-stream.schema.json` 与 [console-plan.md](https://github.com/SheathedSharp/option-berth-macos/blob/main/docs/console-plan.md) 第 4.6 节。
`agent list --json` 的 `"stream": true|false` 表示本构建是否包含该 provider 的流式适配器（Codex、Claude Code、OpenCode ACP、Pi RPC）。
生产环境需已安装对应 CLI；自动化测试可设 `BERTH_AGENT_STREAM_FAKE=<provider>` 使用纯 NDJSON 假驱动，或对
`go test` 注入 `LookPath` 指向 `engine/internal/agentstream/testdata/fake*` 替身。
本构建含 Codex `app-server`、Claude `stream-json`、OpenCode `acp` 与 Pi `--mode rpc` live 桥；未安装 CLI 时首轮 `turn.start` 返回 `unsupported`。

### Explicit native continuation / 显式原生续接

`oberth agent plan <provider> --worktree /workspace/project --resume-file /absolute/session-file --json`
produces a structured continuation plan; `agent run` accepts the same file option and hands the
terminal to the provider. There is no implicit `--last`, partial ID search, shell interpolation,
automatic login, conversation import, or approval bypass.

Supported metadata: Codex JSONL `session_meta` header; Claude Code JSONL records containing
`sessionId`, `cwd`, and a semantic `version`; Pi session-format v3 UUID headers; OpenCode JSON
exports with `info.id`, `info.directory`, and `info.version`. Metadata reads are bounded to 256 KiB;
transcript messages are not copied into plans or another conversation database. The recorded
worktree must resolve to the same directory as `--worktree`. Plan `resume` reports the exact ID,
source metadata version, source file, worktree, and `approval_owner: provider_native`.

The person must select an original provider session file or a trusted OpenCode export. This checks
that file's metadata, not the authenticity of a user-edited export or the currently installed CLI's
version. The external CLI remains responsible for finding the ID, enforcing permissions and
reading its actual conversation. Unknown formats, task-mode continuation and DeepSeek file-based
continuation fail closed. This is a structured **launch** contract, not a unified live event/approval
protocol; those capabilities remain in backlog.

Provider contracts: [OpenCode CLI](https://opencode.ai/docs/cli/),
[Codex CLI](https://developers.openai.com/codex/cli/reference/),
[Claude Code CLI](https://code.claude.com/docs/en/cli-reference), and
[Pi CLI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md).
