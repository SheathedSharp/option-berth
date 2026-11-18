# 交接

[English](handoff.md) | 中文

工作记录、可选的模型评估，以及把会话交给外部编程助手。这些命令记录或转交工作，不启动也不停止服务。

## oberth workplace

交接层的工作记录：一个 worktree 下做过什么、结果如何，供人和 agent 读回来接手。
完整契约（子命令、block 字段、记录的产生与边界）在 [工作记录](../workplace.zh.md)。

## oberth attn

可选的模型评估就是 `oberth attn` 命令；授权模型与哪些内容会离开本机见 [Attention](../attention.zh.md)。
agent 应使用一次调用完成状态读取、异常验证、评估和 HIL 输出：

~~~sh
# for short tasks pass arguments directly, no need to write a file
oberth attn assess --project "$PWD" \
  --task "keep the API healthy while the integration test runs" \
  --authorize inspect_logs,inspect_port,restart_service,continue_without_action,ask_user_for_context
~~~

`oberth attn config` 是唯一写凭据的动词。无参数在终端运行时，它用方向键依次问供应商、
模型、密钥，每个列表末尾都有自定义行：目录没有收录的端点或模型也能配。preset 只是填空
便利，不是供应商限制：`openrouter` 填入 `https://openrouter.ai/api/alpha/decisions`，默认
`typesafe/jev-1.14`，列表带上该端点当前提供的决策模型（TypeSafe Jev、OpenAI GPT-6 Luna
Decisions、Google Gemini Decision 1、Microsoft Decision 1、Perplexity Decider、Cloudflare
Clef、LiquidAI d1.1、Upstage Solar Decide、Morph Reflex）；`typesafe` 填入
`https://api.typesafe.ai/v1/systemone`，可选 `jev-latest` 与 `jev-preview`。脚本与 CI 仍走
参数形式：直接给 endpoint 与 model，或用 `--preset NAME`，密钥从 `--key-stdin`、
`--key-env NAME`、终端隐藏输入读取，或保留已存密钥。两种形式都发一次合成的脱敏探测，
**只有探测成功才写** `attn.json`；探测失败时已有配置保持不动。`config` 改的是凭据，不是同意。
`oberth attn on` 与 `oberth attn off [--forget]` 只翻转一个 `enabled` 布尔值，不联网。
`oberth attn test` 用当前有效配置探测一次。`oberth attn status` 报告解析结果、不可用时的
固定原因，以及本地质量账本摘要。`--json` 输出的是决策文档：同意与配置字段，加上一个只回答
「是否有任务在观测、是否有事情发生」的 `quality`——`activity`、`records_read`、
`assessment_records`、`model_calls`、`cache_hits`、`feedback_records`、`unique_cases`、
`labeled_cases`、`unlabeled_cases` 和 `sessions`。`--quality` 把该对象换成下文描述的完整
质量报告；`--since` 与 `--quality-project` 收窄的正是这份报告，因此它们隐含 `--quality`。
计数为零也留在文档里，不省略。`oberth doctor` 增加一行离线状态；人读 `status` 在未启用且当前
有异常时，每两周提示一次，`BERTH_NO_TIPS=1` 可关闭。

`oberth attn assess` 是一次性 runner。`--dry-run` 打印当前事件将要发送的脱敏请求体但不发送；
`--follow` 让同一循环在任务生命周期内持续运行，以 NDJSON 输出 `subscribed`、仅变化的手递
和取消时的 `stopped`。`oberth attn feedback` 记录人实际做出的决定，`oberth attn preflight`
从新鲜的状态快照派生一个有界的动作请求。只有一个 `oberth` 二进制，它们都不是独立可执行文件。

上下文文件包含 `task` 和必填的 `authorized_options`，**由 agent 自己写**：机器上没有任何东西
替你生成它，路径也由 agent 选（并行的任务各用一个文件，不要共用一个 `/tmp` 路径）。
`--task` 与 `--authorize` 覆盖文件里的同名字段；两者都没有时 runner 直接报错，不会替 agent
猜一个授权集。适配器只向选定的 endpoint 发送脱敏的事实摘要，
不会读取日志或起停服务。runner 输出 `clear`、`continue`、`human_required`、`unavailable` 或
`stale`；成功时把校验后的 `assessment` 原子写回同一文件，并在返回前复读状态。
没有可用的环境 endpoint、model 或 key、网络失败、响应无效、文件过期或状态修订变化时，事实文件保持不变。
`oberth attn config` 写入的 `~/.option-berth/attn.json`（隔离运行时为 `$BERTH_HOME/attn.json`）由
`oberth attn` 自动读取，并且是唯一的持久同意：`enabled: true` 开启，`enabled: false` 即使环境
里有密钥也保持关闭。显式 `BERTH_ATTN_ENDPOINT` / `BERTH_ATTN_MODEL` / `BERTH_ATTN_KEY` 只提供这三个
字段，不开启评估；`BERTH_ATTN=1` 只为当前进程开启。每次不可用都带固定原因码（`no_key`、`disabled`、
`invalid_config`、`unauthorized`、`rate_limited`、`timeout`、`network`、`provider_error`、
`bad_response`、`stale`），而不是 provider 的错误原文。
`assess` 与 `assess --follow` 必须显式提供 `authorized_options`；artifact 的 `options` 只是候选集，
不会自动变成 agent 的授权。
完整参数和隐私边界见
[`attn-adapter.md`](../agent/attn-adapter.md)。

`oberth attn assess` / `assess --follow` 会在 `$BERTH_HOME/quality/events.ndjson`
留下脱敏的本地质量观测。开发 agent 在真实交互后可用
`oberth attn feedback --file <json>` 回写人工标签和动作结果，再用
`oberth attn status --json --quality [--since 168h] [--quality-project <name>]` 查看 `quality`
中的标注覆盖率、assessment 可用率、路由原因与模型/选项分布、人工升级 precision/recall/F1、
模型的 `needs_human` / `task_relevant` 概率误差、推荐命中率、动作成功率、解决率和发现延迟。
质量记录不属于 `status` 事实，也不改变任何执行权限；报告的 `sessions` 对象会说明会话、轮询、事件、
模型调用、缓存命中和无事件轮询数量，它同样在默认摘要里；没有真实反馈时指标保持 `null`。

`on` 表示允许评估，不表示已有任务会话运行。开发 skill 可以在相关运行任务中，明确当前
worktree 的规范路径、脱敏任务摘要和固定评估选项后，接入一个现有 runner。它必须复用自己
拥有的任务句柄，任务结束时回收，不能建立全局监听，也不能授予执行副作用的权限。runner
在每轮读取前重新检查同意及供应商身份；关闭评估，或修改 endpoint、model、凭据，都会在下次
请求前结束该任务会话，原因是 `disabled` 或 `configuration_changed`。没有合资格异常就不请求模型。

`assess --follow` 在可用时复用已有作用域变化流，按任务的精确工作树过滤并对游标去重。相关变化触发状态和 artifact 刷新，不直接触发模型请求。重连后补取快照；连接正常时也按 `max(10s, --interval)` 校准。`--interval` 控制本地同意检查和回退轮询，不代表模型调用周期。后台服务缺失、版本较旧或断线时使用有界状态轮询，不会为此启动 runtime service。同一任务不需要再建独立 events 观察器。正常状态仍无 provider 请求，评估后的新鲜度核验仍然保留。

`quality.model_calls` 和 `quality.cache_hits` 统计已记录的传输尝试及缓存复用，包含单次评估
和尚未结束的会话，不再叠加生命周期汇总。校验失败、状态读取失败不算模型调用；本地错误会
记录，但不保存私密错误原文。旧会话累计数只在全时段或完整覆盖会话的窗口中复用；
`legacy_counts_unallocated: true` 表示旧累计数无法分配到部分时间窗，此时显示的是下界。
`--quality-project` 可以传项目名（可能合并同名目录）或准确的 worktree 根路径、身份标识。

`quality.activity` 单独说明观测：`state` 为 `no_session`（该目录、时间窗、过滤条件下没有观测
记录）、`observing`（任务完成过一轮观测且短租约未过期）、`failed`（最近观测失败）或
`recent`（只有历史记录，没有新鲜租约）。还包含 `observing_sessions`、`last_observed_at`、
`last_status`、`reason` 和 `no_event`。观测租约依轮询间隔持续15至60秒，仅证明最近完成过
观测，不证明进程仍存活。历史 `subscribed` 绝不代表当前活跃；即便 NDJSON 没有变化，每轮
仍写本地 `observation` 或 `assessment`。无事件观测不增加 `assessment_records`。账本仍按
上限轮转，所以没有记录不等于供应商账单中没有请求，也不能保证从未请求过。

## oberth agent: external coding-agent handoff

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

## oberth agent stream

`oberth agent stream <provider> --worktree <path> [--resume-file <path>]` 启动结构化桥进程：
stdin 每行一条命令、stdout 每行一个事件，均为带 `"v":1` 的 NDJSON；stderr 只写诊断。
契约见 `engine/docs/schema/agent-stream.schema.json`。
`agent list --json` 的 `"stream": true|false` 表示本构建是否包含该 provider 的流式适配器（Codex、Claude Code、OpenCode ACP、Pi RPC）。
生产环境需已安装对应 CLI；自动化测试可设 `BERTH_AGENT_STREAM_FAKE=<provider>` 使用纯 NDJSON 假驱动，或对
`go test` 注入 `LookPath` 指向 `engine/internal/agentstream/testdata/fake*` 替身。
本构建含 Codex `app-server`、Claude `stream-json`、OpenCode `acp` 与 Pi `--mode rpc` live 桥；未安装 CLI 时首轮 `turn.start` 返回 `unsupported`。

## Explicit native continuation / 显式原生续接

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
