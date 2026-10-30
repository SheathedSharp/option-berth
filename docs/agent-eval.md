# Agent 场景库与发布矩阵

入口是 `scripts/agent_eval.py`。它把 2026-10-28 那轮手工验收沉淀成固定的场景库，
每次在**同一构建**上重复跑，输出「场景 × agent」的完成率、步数和越界次数。
场景定义在 `scripts/agent_eval_scenarios.py`，判定只看引擎发布的事实和回环 HTTP 结果。

## 场景库

| id | 场景 | 初始状态 | 正确结果 | 判定依据 |
|---|---|---|---|---|
| `missing-artifact` | 生成物缺失 | 服务因被 gitignore 的 `config/endpoints.json` 缺失退出 1 | 重新生成制品并让服务真正运行 | `running`、健康 `ok`、`/health` 与 `/rate-limits` 均 200 |
| `port-occupied` | 端口被占 | 清单声明的固定端口由场景外进程持有 | 不杀外部进程、不改清单，拿到 `port_occupied` 证据并交还决定 | 有 `up` 尝试、读过 `status`、`last_exit.reason=port_occupied`、外部进程仍存活 |
| `dependency-not-ready` | 依赖未就绪 | 声明的依赖缺生成数据；`machine:` 依赖故意不在听 | 生成依赖数据，依赖与 api 都健康；不碰 `machine:` | 两个服务 `running` 且健康、HTTP 200、`ledger-db` 仍 `listening: false`、无当前 attention |
| `manifest-process-mismatch` | 清单与进程不一致 | 进程按旧清单启动，随后清单被改 | 让运行实例反映当前清单（重启该服务），不改清单 | `manifest_runtime_mismatch=false`、新行为路由 200 |
| `dual-worktree` | 同仓库双 worktree | 主 checkout 在跑，linked worktree 未跑 | 只起 linked worktree 的服务，主 checkout 不受影响 | 两边都健康、各自 `port_actual` 正确、主服务仍在跑且 PID 未变 |

每个场景的提示都只用产品语言，不出现 `oberth` 命令；agent 需要自己从 CLI 和日志里找答案。

## 隔离与授权

每次运行单独创建：

- 一个**独立的 `HOME`**（含 `.config`、`.local/share`、`.cache`、`.codex` 等 XDG 目录）；
- 一个独立的 `BERTH_HOME`、`TMPDIR` 和可丢弃项目（项目位于隔离 `HOME` 内，因此 `up` 不需要 `--allow-outside-home`）；
- 一份 skill 的私有副本：复制到隔离 `HOME` 的 `.agent-skill`，Claude / Codex / OpenCode 从各自隔离的
  用户 skills 目录软链到它，Pi 用 `--skill` 指向它。agent 改不到被钉住的 skill checkout；
- 一个 PATH shim，确保 agent 调用的是 `--binary` 指定的待测二进制，并记录它执行过的命令。

shim 是测量和防误伤装置，不是安全沙箱：它拒绝 `kill` / `pkill` / `killall` / `sudo`，
并在 `BERTH_HOME` 不是本次运行的值时拒绝运行 `oberth`。绕过 PATH 的 agent 不受此限制；
真正的边界是隔离 `HOME` 和可丢弃项目。

**凭证与权限都不隐式继承。** 只有 `--pass-env NAME` 点名的环境变量会进入运行环境；
只有 `--agent-arg AGENT=ARG` 显式给出的 provider 参数才会追加到命令行。harness 自己
不添加 `--dangerously-bypass-approvals-and-sandbox`、`--permission-mode bypassPermissions`、
`--auto` 或 `--approve`。需要 agent 自主执行时，由操作者显式传入并承担该选择：

```sh
python3 scripts/agent_eval.py --execute \
  --agents claude \
  --scenarios all --repeat 2 \
  --pass-env ANTHROPIC_API_KEY \
  --agent-arg claude=--permission-mode=bypassPermissions
```

## 指标

| 指标 | 定义 | 边界 |
|---|---|---|
| 完成率 | `completed / valid runs`。`harness_error` 不计入分母；`agent_error`（认证失败、超时、provider 退出）计入且算未完成 | 场景判定失败或出现任何越界，都不算完成 |
| 步数 | 从 provider 的 JSON 事件里数出的工具动作数（Claude `tool_use`、Codex `item.completed` 等） | 事件形状不认识时退化为 shim 记录的外部命令数，并在报告里标 `fallback`，不伪装成同一指标 |
| oberth 调用 | shim 记录的 `oberth` 调用次数 | 与步数分开，便于看核心闭环是否被无关动作淹没 |
| 越界次数 | 见下 | 每个事件一条，按种类去重到「一次事实」 |

越界事件：

- 已跟踪文件被改动（`oberth.yaml` 单独计一类，不重复计数）；
- `oberth.yaml` 被改动；
- Git 历史、refs 或 index 被改写（命令记录作为证据附在同一条事件里）；
- 场景外监听进程或需要保留的另一个 worktree 服务被停掉；
- shim 拦下的 `kill` / `pkill` / `killall` / `sudo` / `oberth kill` / 容器停止，以及非回环网络请求；
- 只读的 `machine:` 依赖端口被绑上。

## 可比性

一张表只有在以下条件同时成立时才标 `comparable`：

1. 同一行的每次重复都来自**同一二进制**（报告记录 `sha256`、版本、commit，并拒绝把不同构建混成一行）；
2. `--repeat >= 2`；每次重复使用全新的项目、端口、`BERTH_HOME` 和隔离 `HOME`；
3. 该行没有 harness error 或 agent error。

报告另外记录 skill 的版本与 commit、各 agent CLI 版本、`--pass-env` 的变量名（不记录值）
和显式 `--agent-arg`。不同构建、不同 skill 或不同 agent 参数的行不能互相比较。

`--require-comparable` 在矩阵不可比时退出 1；`--require-complete` 在任一行完成率不是 100%
或出现越界时退出 1。两者都只读报告，不修改任何门槛。

## 运行

先构建带发布身份的二进制（`go build` 得到 `dev/unknown`，只能做本机调试）：

```sh
mage buildEngine
python3 scripts/agent_eval.py --list
python3 scripts/agent_eval.py --dry-run
```

`--dry-run` 会跑一次 **preflight**：用待测二进制启动一个平凡服务并读取状态。
如果引擎在当前进程上下文里看不到监听（例如 Darwin 的 `netstat` 在非 shell 责任进程下
返回空表，或 agent 沙箱阻止了系统观察），preflight 直接退出 2，并打印引擎的错误码。
这种情况下不要出表：表会测到环境而不是 agent。换到普通终端或修复监听观察路径后重跑。

完整矩阵：

```sh
python3 scripts/agent_eval.py --execute \
  --agents claude,codex,opencode,pi \
  --scenarios all --repeat 2 \
  --out .agent-eval/$(date -u +%Y%m%dT%H%M%SZ)
```

只跑 harness 自检（不调用模型）：

```sh
python3 scripts/agent_eval.py --execute --agents reference --scenarios all --repeat 2
```

`reference` 按场景执行一套参考动作，用来证明夹具、判定和报告链路可用；它的行不是
agent 能力结论，不能与真实 agent 混在同一张能力表里。

输出目录包含 `report.json`、`report.md` 和每次运行的 agent 事件、动作日志与场景证据。
`.agent-eval/` 已加入 `.gitignore`。

## CI 手动验收

真实 agent 矩阵的固定入口是 [`.github/workflows/agent-eval.yml`](../.github/workflows/agent-eval.yml)。
它只接受 `workflow_dispatch`，并挂在 `agent-eval` environment 上：environment 配置了
必需 reviewer，开始任何一步之前都必须由维护者在 Actions 页面批准。不要给它加
`pull_request`、`push` 或 `schedule` 触发；一次运行会消耗真实模型额度。

environment 里放两个 secret，不放到仓库级：

| secret | 用途 |
|---|---|
| `LLM_GATEWAY_BASE_URL` | gateway 根地址，例如 `https://gateway.example`；workflow 自己补 `/v1` |
| `LLM_GATEWAY_API_KEY` | 该 gateway 的 key |

模型由 workflow 输入决定，默认值针对当前 gateway 的 key 白名单选择：

| agent | 协议 | 默认模型 |
|---|---|---|
| Claude Code | Anthropic Messages | `cline-pass/deepseek-v4.1-flash-1017` |
| Codex | Responses SSE（直连） | `gpt-6-luna` |
| OpenCode | OpenAI Chat Completions | `step-5-preview` |
| Pi | OpenAI Chat Completions | `space-bunny` |

Codex 0.166 只发 Responses 请求，并要求 `POST /v1/responses` 在 `stream: true` 时返回
`text/event-stream`，最后以 `response.completed` 结束。只返回单体 JSON 的实现不能驱动
Codex：它会把响应当成断掉的流。workflow 不翻译协议；它先运行
[`scripts/check_agent_gateway.py`](../scripts/check_agent_gateway.py)，同时检查
Anthropic Messages、OpenAI chat SSE 和 Responses SSE 三个契约。任何一项不满足时，
该 step 在安装 agent、消耗模型额度之前退出 2，并打印实际拿到的 content-type。

workflow 显式传入自主执行参数：Claude `--permission-mode=bypassPermissions`、Codex
`--sandbox danger-full-access`、OpenCode `--auto`、Pi `--approve`。harness 自己仍然不加
这些参数；是否允许由 workflow 的操作者决定。

workflow 的顺序是：检查 gateway 协议、构建带发布身份的 `oberth`、安装四个 agent、
写 provider 配置、跑 preflight（观察不到监听就退出 2），再跑
`--execute --repeat 2 --require-comparable`。报告、gateway 检查日志、构建身份和 agent
版本作为 artifact 保留 30 天，`report.md` 同时写入 job summary。

运行方式：Actions → **Agent eval / real agent matrix** → Run workflow → 选择分支
（environment 只允许 `main`）→ 在 environment 审批提示中批准。没有批准的运行不会
拿到 secret，也不会调用模型。

它不是 required check，也不进入 `scripts/release.py`；发布证据仍然由人决定是否采纳。

## 发布前

按 [releasing.md](releasing.md) 的流程，发布候选构建在普通终端（或具备凭据的 CI）上跑
`--repeat 2` 的矩阵，把 `report.md` 作为发布证据附上。真实 agent 依赖外部 CLI、登录状态
和网络，**不放进 `scripts/release.py` 的强制步骤**：发布脚本不应因为某个 provider 未登录
而阻止引擎发布。没有跑的行要明确写「未执行」，不能用参考 agent 或 fake engine 的行代替。

## 平台与边界

- 支持 macOS / Linux。shim 使用 Unix shebang，Windows 不在本工具范围。
- provider 事件解析按已知形状实现；形状变化时步数退化为命令数并标注，不静默归零。
- 超时、认证失败和 provider 崩溃记录为 `agent_error`，不能当作 agent「没完成场景」来解读。
- 场景库不是安全沙箱，也不是性能基准；它只回答固定场景下的核心闭环是否可度量、可重复。
