# Attention：提示，以及可选的模型分诊

[English](attention.md) | 中文

Attention 分两层。Attention Notes 是关于工作树的确定性事实：始终产生、存在本机，不需要密钥、网络或授权就能读。它上面还有一层可选的模型分诊，判断某条提示是否影响当前任务、是否需要人来选择。两层都不负责确定进程真相、不运行命令、不修服务，也不授予权限。不开启任何一层，`status`、`up`、`down` 仍然可用。

## 第一层：确定性提示

每一次 Runtime World 收敛都会产出提示。它们按一个小生命周期流转——`open`、`triaged`、`delivered`、`resolved` 或 `suppressed`——工作树恢复健康后自行解决。Runtime Service 以仅本人可读的权限（`0600`、原子改名、跨进程文件锁）写到 `BERTH_HOME/attention/notes/<worktree_id>.json`，别的进程读到的是被派生出来的那份结果，不是第二份判断。

```sh
oberth attn notes
oberth attn notes --all --json
```

`notes` 读取 Runtime Service 已落盘的提示；还没有宿主落盘时，它从 `oberth runtime inspect` 现场派生。`--all` 加上已解决的历史，`--project` 读取别的工作树，`--json` 打印提示文档（`oberth.attn.notes/v1`）。存在提示时，`oberth attn status` 会带上 `notes` 摘要。

## 第二层：可选的模型分诊

在得到同意后，一份脱敏的因果数据包会发给配置好的服务商：哈希化的工作树身份、有长度限制的分支与任务文本、诊断里的根因、漂移和循环、占用端口那类进程的种类——绝不含 PID 或命令行——以及一行有界、已脱敏路径与密钥的错误行。

| 模式 | 分诊可以额外给出什么 |
| --- | --- |
| `strict` | 只有事实和证据，不给行动建议 |
| `assist` | 紧急度、`needs_human`、`task_relevant`、有界摘要，以及一条固定的推荐选项 |
| `reconcile` | 同上，并在所有安全门都成立时给出 `auto_reconcile_eligible` |

`reconcile` 只把提示标成可本地重启，而且必须同时满足：推荐的安全等级是 `local_restart`、`needs_human` 不超过 0.25、置信度不低于 0.80、服务不在崩溃循环里、端口没有被外部进程占着。被标记仍然不是动作：这条路径不会杀不是 oberth 启动的进程，不会写清单，也不会扩大授权。

## 先查看，再决定是否启用

下面两条命令只读本地状态，都不会连接服务商。

```sh
oberth attn status
oberth attn assess --dry-run
```

`status` 报告实际生效的配置和同意状态，不显示密钥。`assess --dry-run` 打印将要发送的那份脱敏请求，你可以据此决定运行信息到底要不要离开本机。有脱敏不等于可以不读自己的任务文本。

`on` 和 doctor 里通过的那一行只说明允许评估，并不表示已有会话在观测、发现已经回到你的编程助手，也不表示有人同意那些建议。`oberth attn status` 把这些分开报告：近期有活动时的上次评估状态、项目里已提交的 `.claude/settings.json` 是否启用了 oberth 的 hooks、以及这个工作树的交付租约是否已被消费。`--quality` 把同一本账用普通文本写出来。模型调用次数和缓存命中都不能证明 Attention 帮上了忙。

## 配置和同意是两步

`oberth attn config` 是唯一写凭据的动词，它不是同意。在终端无参数运行，它用方向键依次问服务商、模型、密钥，每个列表末尾都有自定义行，目录没有收录的端点或模型也能配。preset 只填一个约定，不是限制。

| 预设 | 协议 | 端点 |
| --- | --- | --- |
| `openrouter` | `decisions` | `https://openrouter.ai/api/alpha/decisions` |
| `typesafe` | `decisions` | `https://api.typesafe.ai/v1/systemone` |
| `openai` | `openai_chat` | 服务商的 chat completions 端点 |
| `anthropic` | `anthropic` | 服务商的 messages 端点 |
| `ollama` | `openai_chat` | 回环地址 `http://127.0.0.1:11434/v1/chat/completions` |

`--protocol`（或 `BERTH_ATTN_PROTOCOL`）选择协议，默认是 `decisions`。远程端点仍然需要密钥，没有密钥的答案是 `no_key`。Ollama 预设是例外：回环端点不需要密钥，并报告 `key_source: "loopback"`，本地模型因此不用在任何地方存放密钥。

```sh
oberth attn config
oberth attn on
```

`config` 发一次合成探测，只有探测成功才写 `attn.json`；失败时已有配置保持不变。探测不含工作树信息，但仍会连接服务商，也可能产生费用。随后 `on` 翻转唯一的同意布尔值；模式取自 `config` 与 `on` 上的 `--mode`（或 `BERTH_ATTN_MODE`），默认 `assist`。`BERTH_ATTN_KEY`、`BERTH_ATTN_ENDPOINT` 和 `BERTH_ATTN_MODEL` 只提供字段，永远不表示同意。显式的进程级开关 `BERTH_ATTN=1` 与持久化 `enabled` 的优先级见[适配器参考](agent/attn-adapter.md#an-environment-key-is-a-secret-not-consent)。

不要把密钥放进命令参数、Git、任务上下文 JSON、服务日志或反馈里。无人值守配置请读 `attn config --help` 中的标准输入与环境变量形式，不要把密钥复制进 shell 历史。模型名或端点预设只是填写便利，不保证你的账号有访问权。

## 哪些信息可能离开本机

符合条件的请求只带第二层列出的那些字段，别的一概没有：没有完整日志、没有环境变量的值、没有密钥、没有无关工作树，也没有端口占用者的身份。`include_error_line` 默认为真，会发送一行脱敏错误；在 `attn.json` 里设为 false 就完全抑制错误行。请检查实际的 dry-run 输出，不要凭想象举例判断。

任务上下文必须授权固定选项词表中的一个子集。提示里的选项只是候选，不是授权；模型返回值只有落在词表内才会被接受——它不能生成 shell 命令，也不能扩大权限范围。涉及副作用的建议仍要由人决定。

## 在 Claude Code 中途交付

`oberth agent claude-hook` 响应 `PostToolUse`，与 `UserPromptSubmit`、`Stop`、`StopFailure` 和 `SessionEnd` 并列。`PostToolUse` 是任务中途那条路径：它不占交付租约，跳过任务边界的密钥派生和同意检查，在锁内读取这个工作树的提示文件，并输出 `hookSpecificOutput` 里至多 500 个字符的 `additionalContext`。每对 `(note, world_revision)` 只交付一次；处在 `OBERTH_CLAUDE_TASK_MODE=edit` 下或工作树没有提示文件时保持沉默。

省略 `--service` 和 `--run` 且工作树里有一份有效的 `oberth.yaml` 时，hook 自动收窄到清单声明的服务；显式选择器总是优先，没有清单时仍返回 `scope_required`。`--print-config` 输出的 `PostToolUse` 条目不带 `onFailure`，所以磁盘再慢也挡不住一次工具调用。请审阅打印出的 JSON 并自行合并进项目的 `.claude/settings.json`；没有任何命令替你写这个文件。完整 hook 合同见 [CLI 参考](cli/handoff.zh.md#原生-claude-项目-hooks)。

## 失败不代表可以越权继续

缺密钥、拒绝请求、限流、网络故障、超时、答复格式错误或修订过期，都不会妨碍只读事实这条路径，助手可以说明缺少建议的原因，必要时直接问人。分诊跑在一个小的异步池上：两个 worker、按工作树合并、对过期修订做比较交换，连续三次传输失败后熔断 60 秒。修订已经变化的提示不会被更旧的答案覆盖。

概率没有经过校准的保证，也不是安全承诺。事件连接只安排符合条件的事实读取，不是全局监听器，也不保证每个事件都调用模型。实现、固定原因码与质量记录见[适配器参考](agent/attn-adapter.md)。

## 关闭

下面两条是不同选择：第一条关闭分诊，第二条同时忘记已保存的密钥。它们都不会连接服务商。

```sh
oberth attn off
oberth attn off --forget
```

关闭分诊会保留确定性提示和它们依据的运行事实，也不改变任何服务启停。本地质量记录不会自动上传；准备分享前，先看适配器参考中的内容范围。
