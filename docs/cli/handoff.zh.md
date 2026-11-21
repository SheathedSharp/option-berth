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

任务上下文 JSON 接受 `services` 和 `run_ids` 评估过滤。服务选择只跟随声明服务；标为 `run_only` 的临时运行事件必须按精确 run ID 关联。过滤进入评估缓存身份；两者都省略时保留既有声明服务范围，不包含任意临时运行。

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

## 原生 Claude 项目 hooks

`agent claude-hook` 处理 Claude Code 官方 `UserPromptSubmit`、`Stop`、`StopFailure` 和 `SessionEnd` JSON 输入。适配器以 **macOS 上的 Claude Code 2.1.322** 和本地假 provider，完成原生非交互（`-p`）端到端验证；不将更早版本或 Windows 原生执行视为已验证/支持。它不能透明附着任意已经运行的 `claude`/`cld`；包装命令必须保留项目 hooks。

交互 TTY 和用户私有 `cld` 包装命令尚未验证。干净 HOME 的交互 PTY 尝试在任务执行前退出：即使 provider base URL 指向本地，Claude 首次启动的连通性检查仍访问 `api.anthropic.com`，被刻意拒绝外网的代理阻止。该尝试没有执行任务或 hook 反馈；不能从 `-p` 证据推导交互覆盖，也不能绕过项目 trust/首次设置来宣称通过。

每个项目只需启用一次，但应先审阅目标范围和只读评估选项：

```sh
oberth agent claude-hook --project "$PWD" --service api \
  --authorize inspect_logs,inspect_port,inspect_manifest --lease 10m --print-config
```

该命令只打印 JSON。审阅后把其中 `hooks` 合入所选项目的 `.claude/settings.json`，不要覆盖其他设置；按 Claude 正常的项目信任流程开启新的原生会话。命令不写设置、不安装全局 hook，也不更改 Attention 同意状态。保留 `UserPromptSubmit` 的 `onFailure: "block"`：若任务入口失败或超时，应阻止该 prompt，而不是让后续 Stop 消费上个任务的租约；该行为依赖已验证版本的官方协议。[示例配置](../../examples/claude-hooks/settings.example.json) 给出最小结构；可执行文件不在常规 PATH 时优先使用生成结果。已安装的 skill 一致不能证明 hooks 已执行。`oberth doctor --only claude_hooks` 只读检查项目可提交的 `.claude/settings.json`，不读 `settings.local.json` 或用户全局 Claude 配置，也不写入 hooks。

`--project` 必填，必须与 hook 输入解析到同一 worktree。`--service` 选声明服务；`--run` 选已登记的精确运行 ID，包括无端口 worker；两者都接受逗号分隔值。不认领同名陌生进程或替换代际的临时运行。`--authorize` 是固定只读评估选项的明确子集，不是工具或操作许可。范围在租约内固定；后来创建的临时运行需要在后续项目/任务配置中明确关联。不生成清单、不扫描 home、不启动缺失服务。已有 Runtime 必须认识所选项目；服务、项目或上下文不可用时返回 `runtime_context_unavailable`，不会转为全机扫描。hook 不读源码、git diff 或日志正文。任务确实需要服务时仍走已有授权的 `up`/`start`，健康的既有运行作为事实复用。

`UserPromptSubmit` 绑定规范 worktree、不可推导含义的会话身份与新的任务代际。只把所选 Runtime 事实加入任务，并保留观测覆盖：监听盲区或窄视图不会被解释成 `running:false`。精确任务运行读取已有完整退出事实，不受 status 展示最近五条的上限影响。忽略 `prompt`、`transcript_path`、助手答复和错误详情。评估摘要使用保守的固定开发任务标签，相关性细节少于人工提供的脱敏摘要；适配器不存档对话，也不用对话充当评估摘要。

范围、明确选项及既有 Attention 同意/配置齐备时，有界子观察器复用现有 runner、限定变化订阅、重连、缓存和质量账本。先过滤事件再请求模型；无合格事件不请求模型。`on` 本身不启动观察，也不是无限模型费用承诺。Attention 关闭时，所选 Runtime 事实仍可进入任务；缺许可、配置或选项另行说明。无范围显示 `scope_required`；纯编辑会话可设 `OBERTH_CLAUDE_TASK_MODE=edit`，关闭观察和上下文并显示 `pure_edit`。适配器不靠读 prompt 猜运行需求；否则项目明确选择的服务范围会保守地用于每个提交任务。

到 `Stop` 时，hook 取消并等待自己的观察器回收，再核同意状态、配置身份、范围、事件修订与新鲜度。相关只读建议与需请人选择/评估不可用的发现都能返回。通过官方顶层 `decision: "block"` / `reason` JSON，给同一原生 Claude 任务**至多一次**有界报告/检查反馈；`systemMessage` 说明本地状态。不注入按键、不偷读 transcript、不新增工具权限、不自动控制服务。先记录已消费再输出：崩溃可能丢一次反馈，但不会重放。`stop_hook_active` 和消费游标防循环；得到原生答复不等于发现已修好。

`StopFailure`、`SessionEnd` 只回收，不继续；新提交任务替换并等待旧代际回收。默认且最长不可续租时间为 `10m`，`--lease` 可缩短。丢失或被杀的观察器不会恢复。观察器租约到期时，若 Pending 仍然新鲜，`Stop` 仍可交付；同一会话稍后的 `UserPromptSubmit` 会在开启新代际之前，把上一代未消费发现写入新 prompt 上下文。过期的 Pending 仍会被丢弃。hook 命令预算与 `--print-config` 的 `timeout` 使用同一值，并且大于 Attention 客户端超时，以便 `Stop` 能够等待仍持有 `active.lock` 的评估。成功交付只写入生命周期遥测，不会伪造人工或 Attention feedback 标签。若用户中断没有提供 Stop/SessionEnd，靠下次 prompt 或租约到期回收，不宣称收到即时回调。清理不停止共享服务。`BERTH_HOME/attention/claude/` 只保存少量用户私有交付游标，不是另一份运行 registry。删除项目 hooks 不会同步通知已经持有的租约；可关闭 Attention 撤销评估，或让有界租约结束。不要求会话级或全局 hooks。



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
