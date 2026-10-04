# oberth CLI 契约

这份文档描述产品主线：一个 worktree 的清单、运行记录和运行实况、起停和日志，以及并行开发
所需的只读事件流。其余命令以 oberth <command> --help 为准，不在这里另建一套契约。

无参数运行 `oberth` 显示首屏：logo、一句定位、三条最常用命令（status / up / down）
和到完整帮助的指引；它是指路牌不是手册，分组命令清单只在 `oberth --help`。

## 共通规则

- 命令在当前目录（或显式的路径 / 项目名）上工作。没有清单时，先运行 oberth init。
- 默认输出给人读；普通 `--json` 时 stdout 恰好只有一个 JSON 值。显式流式命令每行一个值，错误写 stderr。
- 跟随日志和其他流式命令使用 NDJSON：每行一个 JSON 值。
- 退出码：0 成功，1 运行失败，2 参数或选项错误，130 被 Ctrl-C 中断。
- JSON 错误形状为 {"error":{"code":"...","message":"...","hint":"..."}}。
  稳定的 code 包括 invalid_params、not_found、invalid_config、ambiguous、
  permission_denied、timeout、daemon_busy 和 internal。
- --no-daemon 让读命令在本地直接采集；需要记录运行和起停的命令仍需要 daemon。
  daemon 不可用时，`status` 仍从本地只读历史显示最近的退出原因。

## 命令面

CLI 只有三层。核心层围绕当前 worktree 的清单和运行闭环；支撑层只提供只读代码上下文
和明确点名的进程控制；基础设施层负责 daemon、配置、自检和 shell 接入。根帮助按这三层
分组，新的产品能力只能落在已有层里，不能再把一个动作塞进另一个命令的选项中。

| 层 | 公开命令 | 边界 |
|---|---|---|
| Worktree lifecycle | `status` · `init` · `up` · `restart` · `down` · `logs` | 当前项目的声明、实况和服务起停 |
| Inspection and explicit controls | `events` · `git` · `doctor` · `start` · `kill` | 订阅明确关系的状态变化、只读代码、自检，或明确点名一个低级进程入口 |
| Runtime and setup | `daemon` · `serve` · `config` · `version` · `completion` | 后台采集器、安装配置和基础设施 |

## 核心五条

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
运行中的服务同时带 `manifest_hash` 与 `runtime_spec_hash`；两者不一致时
`manifest_runtime_mismatch` 为 `true`，表示当前进程来自旧清单，应该用
`oberth restart --only <service>` 重新应用。hash 只覆盖命令、cwd、端口、health 和依赖等可执行声明，
不包含环境变量内容。

JSON 顶层固定包含：

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "services": []},
  "ports": [],
  "git": {"snapshot": {}},
  "drafts": [],
  "exits": [],
  "changed": {"at": "..."}
}
~~~

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
jev-attention --auto --project "$PWD" --task-context /tmp/oberth-task-context.json
~~~

上下文文件包含 `task` 和必填的 `authorized_options`；适配器只向选定的 Jev provider 发送脱敏的事实摘要，
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
成功后才启动长驻的 `cmd:`；前置命令的输出写入该服务日志，失败时服务不会启动。带端口的依赖等待监听，无端口的 worker 等待其运行记录变为活跃；
已经在运行的服务跳过；每个服务有自己的
日志文件和运行记录。无参数时使用当前目录最近的清单，也可以显式给项目名。

成功时人读输出逐服务报告并给出汇总；`--wait` 等待期间先输出一行 dim 提示再逐行出结果。
--json 输出一个包含 services 和汇总字段的值。每个
`services[]` 都有结构化的 `state`：`started`、`skipped` 或 `failed`，失败时还给出机器可分支的
`reason` 和可直接执行的 `hint`（通常是 `oberth logs <service> --once`），原有 `error` 保留给人读。
等待判定只认**等待开始之后**记录的退出：`down` 留下的旧退出记录不会被 `up --wait` 读成本轮启动失败。
`--wait`（`--ready` 的别名）会在返回前等待本次选择的服务（包括已经在运行、未重新启动的服务）真正监听；声明了 `health:` 的服务还必须
通过健康检查。无端口 worker 以运行记录活跃作为 ready。默认不等待，`--wait-timeout` 默认 30s；显式指定时必须为正值且同时启用 `--wait` 或 `--ready`，否则退出 2，不执行启动；
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
   "error":"command not found","hint":"oberth logs worker --once"}
],"started":["api"],"skipped":[],"errors":["worker"]}
~~~

### oberth restart [project] [--only name,...] [--force] [--allow-outside-home] [--json]

先停止再按清单重新启动项目服务。人读输出先报告停止结果，空一行后报告启动结果。
`--only` 只重启点名的服务，依赖和其他服务保持运行；
这类重启保留 `port: auto` 的 worktree 占用，让服务重新拿回原地址。`--force` 跳过优雅停止。
--json 返回一个值，其中 `stopped` 是停止结果，`services` 和汇总字段是重新启动结果。

### oberth down [project] [--force] [--json]

停止该项目清单里的全部服务，动作后确认可释放时交还 `port: auto` 的预留。
无参数时使用当前目录的清单。`--force` 直接使用强制信号，不跳过运行身份或预留释放门禁。
成功时 `--json` 返回停止结果和实际释放数量。项目清单中声明的 `machine:` 依赖只读其
监听状态，`down` 不会停止它们。

取消、停止不完整、动作后观察失败、仍有登记运行/监听或预留已被刷新时，不交还未经确认
的预留，返回现有 RPC 错误并提示检查 `status` 和日志后重试。已发送的信号不因这个错误
撤销；`not_found` 不单独证明端口已可释放。无端口运行的真实停止需要准确的出生身份。
底层 dry-run 和 scoped 停止不释放预留；`restart --only` 因此保留原分配。
这不承诺 OS 观察与账本写入原子，也不承诺任意旧版本同时写入同一账本。

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

## 支撑命令

这些命令服务于核心闭环，保持可用但不再扩展产品模型：

- oberth doctor：检查清单、daemon、CLI 路径和日志目录，给出可操作的修复提示。
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
- oberth git [path]：在 status 之外按需读取代码摘要、文件列表或 diff。

`start` 只运行 `oberth start [flags] -- <command> [args...]` 这一种低级入口；日常项目服务
使用 `up` / `down`。`kill` 以端口或 pid 为主语，`--all` 是仍需明确确认的整批例外；旧的
group/session 选择器只为迁移保留，不出现在公开帮助中。

## 基础设施命令

- oberth daemon status|path|stop|restart|log|schema：管理或查看后台采集器。
- `oberth -v` / `oberth --version`：简洁地打印当前 CLI 版本；`oberth config init|path|edit`、`oberth serve`、`oberth version`、`oberth completion`：配置与运行环境入口。
- `oberth version --json` 输出当前构建的 `version`、`commit`、`built` 和 `platform`，供安装检查脚本确认实际运行的构建。

`oberth version --json` 的 stdout 是一个 JSON 值：

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64"}
~~~

## 清单和状态的边界

- 清单由人写；agent 只能起草，`oberth init adopt` 才采纳。
- option-berth 只读 git，不提交、不暂存、不切分支。
- machine: 是项目依赖的只读引用；up / down 不会替它起停。
- 监听端口、pid、退出码和日志是 Run 的运行实况。产品不提供整机端口雷达、机器服务注册表、模型
  判断、MCP 服务或自动安装 skill。

### 清单解析的歧义拒绝

`oberth.yaml` 只接受一个 YAML 文档；第二个文档（包括空文档）会报错，而不是被忽略。
同一服务不能重复声明 `port`，包括 `auto` 与数字混写；错误带 YAML 行号。
单文档、注释、正常数字端口和 `port: auto` 保持兼容。此规则不是完整的未知字段严格校验。

`doctor --only project_config` 可额外报告被忽略的清单键及行列位置（warn），不回显字段值；
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
