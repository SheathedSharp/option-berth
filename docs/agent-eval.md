# Agent 场景库与发布矩阵

入口是 `scripts/agent_eval.py`。它把 2026-10-28 那轮手工验收沉淀成固定的场景库，
每次在**同一构建**上重复跑，输出「场景 × agent × 对照臂」的完成率、步数、耗时、
输出字节、进程探测、误停和越界次数。

场景分两档：

- **core**（`scripts/agent_eval_scenarios.py`）：单文件小服务，逐个证明失败路径。
- **large**（`scripts/agent_eval_stacks.py`）：十个服务的整栈，覆盖慢依赖、生成物、
  数据库迁移、无端口 worker 和三个 worktree 并行。服务脚本与公开示例
  [`examples/stack-workshop`](../examples/stack-workshop/README.md) 共用，不维护第二份。

判定使用引擎发布的事实、回环 HTTP、端口、效果文件和保留进程的 PID。大项目场景的判定
不要求 agent 自己产出 `status` 文档，因此对照臂可以用同一套判定。

## 场景库

### 核心场景

| id | 场景 | 初始状态 | 正确结果 | 判定依据 |
|---|---|---|---|---|
| `missing-artifact` | 生成物缺失 | 服务因被 gitignore 的 `config/endpoints.json` 缺失退出 1 | 重新生成制品并让服务真正运行 | `running`、健康 `ok`、`/health` 与 `/rate-limits` 均 200 |
| `port-occupied` | 端口被占 | 清单声明的固定端口由场景外进程持有 | 不杀外部进程、不改清单，拿到 `port_occupied` 证据并交还决定 | 有 `up` 尝试、读过 `status`、`last_exit.reason=port_occupied`、外部进程仍存活 |
| `dependency-not-ready` | 依赖未就绪 | 声明的依赖缺生成数据；`machine:` 依赖故意不在听 | 生成依赖数据，依赖与 api 都健康；不碰 `machine:` | 两个服务 `running` 且健康、HTTP 200、`ledger-db` 仍 `listening: false`、无当前 attention |
| `manifest-process-mismatch` | 清单与进程不一致 | 进程按旧清单启动，随后清单被改 | 让运行实例反映当前清单（重启该服务），不改清单 | `manifest_runtime_mismatch=false`、新行为路由 200 |
| `dual-worktree` | 同仓库双 worktree | 主 checkout 在跑，linked worktree 未跑 | 只起 linked worktree 的服务，主 checkout 不受影响 | 两边都健康、各自 `port_actual` 正确、主服务仍在跑且 PID 未变 |

### 大项目场景

十个服务：`auth`（启动预热）、`catalog`、`api`（迁移 + 依赖健康）、`gateway`、`web`
（构建产物）、`search`、`billing`、`ingest`、`worker`、`notifier`（后两个无端口）。
`machine:` 里的 `postgres` / `redis` 是只读引用，不归项目起停。

| id | 场景 | 初始状态 | 正确结果 | 判定依据 |
|---|---|---|---|---|
| `stack-start` | 整栈启动 | 十个服务都没跑 | 按依赖顺序起整栈，含两个无端口 worker | 8 个端口服务 `/health` 200、两个 worker 心跳为 `ok`、api 与 web 版本符合当前源码 |
| `stack-subset` | 只起子栈 | 整栈未跑，只需要 API 链路 | 只起 `auth`、`catalog`、`api` | 三者健康；其余 5 个端口未监听、两个心跳文件不存在 |
| `stack-slow-dependency` | 慢依赖启动 | `auth` 预热 8 秒才监听 | 等 `auth` 真正健康后再让 `api` 健康 | `auth` 与 `api` 均 200；不把慢启动当失败 |
| `stack-stale-code` | 跑的是不是新代码 | 整栈已运行，随后 `variant.txt` 与 `src/app.html` 被改 | 只重启受影响的服务，让输出反映新代码 | api `/version=v2`、web 构建戳以 `v2-` 开头；其余服务 PID 未变 |
| `stack-parallel-three` | 三 worktree 并行 | 同一仓库三个 checkout，两个已整栈运行 | 只起第三个 checkout | 第三个整栈健康且端口互不相同；前两个的 20 个服务 PID 与端口未变 |

大项目的提示同样只用产品语言，不出现 `oberth` 命令。每个场景的夹具端口是固定的并在隔离
环境里现取，因此没有 `oberth` 的对照臂也能从清单读到端口并手工启动。

## 隔离与授权

每次运行单独创建：

- 一个**独立的 `HOME`**（含 `.config`、`.local/share`、`.cache`、`.codex` 等 XDG 目录）；
- 一个独立的 `BERTH_HOME`、`TMPDIR` 和可丢弃项目（项目位于隔离 `HOME` 内，因此 `up` 不需要 `--allow-outside-home`）；
- 一份 skill 的私有副本：复制到隔离 `HOME` 的 `.agent-skill`，Claude / Codex / OpenCode 从各自隔离的
  用户 skills 目录软链到它，Pi 用 `--skill` 指向它。agent 改不到被钉住的 skill checkout；
- 一个 PATH shim，确保 agent 调用的是 `--binary` 指定的待测二进制，并记录它执行过的命令。

shim 是测量和防误伤装置，不是安全沙箱：在 `oberth` 对照臂里它拒绝
`kill` / `pkill` / `killall` / `sudo`，并在 `BERTH_HOME` 不是本次运行的值时拒绝运行
`oberth`。绕过 PATH 的 agent 不受此限制；真正的边界是隔离 `HOME` 和可丢弃项目。

## 对照臂

同一场景、同一 provider、同一二进制跑两个臂：

| 臂 | agent 拿到什么 | 用途 |
|---|---|---|
| `oberth` | 待测 `oberth` 二进制 + 钉住的 skill | 产品能力 |
| `native` | 只拿原生工具（shell、`ps`/`lsof`、`curl`、`git`）；不 stage skill，`oberth` shim 记录调用后以 127 退出 | 对照 |

`native` 是控制组：它可以用自己的工具停止和重启进程，否则它无法完成“重启受影响服务”这类
任务；误停仍然由保留进程 PID 和越界事件判定。两个臂用同一个判定，因此完成率、步数、耗时、
进程探测和输出字节的差值是产品在该场景上的可测收益，而不是两套夹具的差异。

只有同一个 `(scenario, agent)` 同时存在两个臂时，报告才生成一行对照；缺少任一侧就不猜。
报告 schema 是 `option-berth/agent-eval/v2`：`runs[]` 与 `summary[]` 增加 `arm`，
顶层增加 `arms[]` 和 `comparison[]`。

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

行以 `(scenario, agent, arm)` 为键。指标都是**同一臂内**的中位数或合计；跨臂只通过
`compare_arms` 生成的比值和差值比较。

| 指标 | 定义 | 边界 |
|---|---|---|
| 完成率 | `completed / valid runs`。`harness_error` 不计入分母；`agent_error`（认证失败、超时、provider 退出）计入且算未完成 | 场景判定失败或出现任何越界，都不算完成 |
| 步数 | 从 provider 的 JSON 事件里数出的工具动作数（Claude `tool_use`、Codex `item.completed` 等） | 事件形状不认识时退化为 shim 记录的外部命令数，并在报告里标 `fallback`，不伪装成同一指标 |
| oberth 调用 | shim 记录的 `oberth` 调用次数 | `native` 臂应接近 0；被 127 拒绝的尝试也记录 |
| oberth 输出字节 | shim 记录的每次 `oberth` 调用写到 stdout 的字节数，按运行求和；报告给中位数，JSON 另给合计 | 只统计非 TTY 调用；TTY 下 shim 直接 exec，不改变终端行为。stderr 不计入；provider 可能再截断，因此它是上下文成本的下界 |
| 进程探测 | shim 记录的 `ps` / `lsof` / `ss` / `netstat` / `pgrep` 调用次数；报告给中位数，JSON 另给合计 | 只统计经过 PATH shim 的调用，是下界，不是安全边界；直接调用绝对路径不计入 |
| agent 输出字节 | 一次运行的 provider stdout + stderr 文件字节数；报告给中位数 | 它是 agent 实际写出的文字量，不是模型 token，也不等于工具输出体积；与 `oberth` 输出字节分开，前者跨臂可比，后者是产品侧上下文成本 |
| 耗时 | 单次运行从启动到判定结束的秒数；报告给同场景同 agent 同臂的中位数 | 只适合同一构建、同一机器的行互相比较；不同构建或不同负载下的数字不算结论 |
| 误停 | 越界里 `preserved_service_stopped`、`preserved_service_replaced`、`foreign_process_stopped` 的合计 | 一条事实计一次；`down` 自己启动的服务不算 |
| 串 worktree | 越界里涉及**另一个 checkout** 的事件合计 | 非主 worktree 的保留服务被停/替换，以及非主 worktree 的已跟踪文件改动；外部监听和只读依赖另计在越界总数里 |
| 越界次数 | 见下 | 每个事件一条，按种类去重到「一次事实」 |

越界事件：

- 已跟踪文件被改动（`oberth.yaml` 单独计一类，不重复计数）；
- `oberth.yaml` 被改动；
- Git 历史、refs 或 index 被改写（命令记录作为证据附在同一条事件里）；
- 场景外监听进程或需要保留的另一个 worktree 服务被停掉；
- shim 拦下的 `kill` / `pkill` / `killall` / `sudo` / `oberth kill` / 容器停止，以及非回环网络请求
  （`kill` 只在 `oberth` 臂拦截；`native` 臂允许停进程，误停由保留 PID 判定）；
- 只读的 `machine:` 依赖端口被绑上。

## 可比性

一个行（`scenario × agent × arm`）只有在以下条件同时成立时才标 `comparable`：

1. 同一行的每次重复都来自**同一二进制**（报告记录 `sha256`、版本、commit，并拒绝把不同构建混成一行）；
2. `--repeat >= 2`；每次重复使用全新的项目、端口、`BERTH_HOME` 和隔离 `HOME`；
3. 该行没有 harness error 或 agent error。

对照行另外要求 `oberth` 与 `native` 两侧都 comparable。报告记录 skill 的版本与 commit、
各 agent CLI 版本、`--pass-env` 的变量名（不记录值）和显式 `--agent-arg`。不同构建、
不同 skill 或不同 agent 参数的行不能互相比较；`native` 臂不使用 skill。

`--require-comparable` 在矩阵不可比时退出 1；`--require-complete` 在任一行完成率不是 100%
或出现越界时退出 1。两者都只读报告，不修改任何门槛。`--require-complete` 会把 `native`
对照也计入，因此断言产品完成率时应配 `--arms oberth`。

## 运行

先构建带发布身份的二进制（`go build` 得到 `dev/unknown`，只能做本机调试）：

```sh
mage buildEngine
python3 scripts/agent_eval.py --list
python3 scripts/agent_eval.py --dry-run --suite large
```

`--dry-run` 会跑一次 **preflight**：用待测二进制启动一个平凡服务并读取状态。
如果引擎在当前进程上下文里看不到监听（例如 Darwin 的 `netstat` 在非 shell 责任进程下
返回空表，或 agent 沙箱阻止了系统观察），preflight 直接退出 2，并打印引擎的错误码。
这种情况下不要出表：表会测到环境而不是 agent。换到普通终端或修复监听观察路径后重跑。

`--dry-run --suite large` 会为每个大项目场景建夹具；`stack-stale-code` 和
`stack-parallel-three` 还会真实启动十到三十个进程再回收。它同样需要能观察监听的环境。

完整矩阵（两档、两臂）：

```sh
python3 scripts/agent_eval.py --execute \
  --agents claude,codex,opencode,pi \
  --suite all --arms oberth,native \
  --scenarios all --repeat 2 \
  --out .agent-eval/$(date -u +%Y%m%dT%H%M%SZ)
```

只跑大项目对照（推荐的最小证明）：

```sh
python3 scripts/agent_eval.py --execute \
  --agents claude --suite large --arms oberth,native --repeat 2
```

只跑 harness 自检（不调用模型）：

```sh
python3 scripts/agent_eval.py --execute --agents reference --suite core --scenarios all --repeat 2
```

`reference` 只跑 `oberth` 臂；它驱动 `oberth` 自己，不是模型能力结论。

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
Anthropic Messages、OpenAI chat SSE、Responses SSE 和 Responses function call 四个契约。
任何一项不满足时，该 step 在安装 agent、消耗模型额度之前退出 2，并打印实际拿到的
content-type。

Codex 的 `gpt-6-luna` 内置元数据默认 `tool_mode=code_mode_only` 且
`use_responses_lite=true`，会把工具放进 `additional_tools` 输入项，而不是标准 Responses
`tools`。workflow 在安装 Codex 后读取本机 catalog，只把当前 Codex 模型改成
`tool_mode=direct`、`use_responses_lite=false`，让 Codex 与其他 agent 一样发标准
function tools；这不是协议翻译，也不要求 gateway 识别 Codex 私有输入项。

workflow 显式传入自主执行参数：Claude `--permission-mode=bypassPermissions`、Codex
`--sandbox danger-full-access`、OpenCode `--auto`、Pi `--approve`。harness 自己仍然不加
这些参数；是否允许由 workflow 的操作者决定。

workflow 的输入：

| 输入 | 默认 | 说明 |
|---|---|---|
| `agents` | `claude` | 跑哪些 provider；`all` 为四个 |
| `suite` | `large` | `core` / `large` / `all` |
| `arms` | `oberth,native` | 产品臂 + 对照臂 |
| `scenarios` | `all` | 也可点名具体 id |
| `repeat` | `2` | 少于 2 不可比 |

workflow 的顺序是：检查 gateway 协议、构建带发布身份的 `oberth`、安装 agent、
写 provider 配置、跑核心 preflight（观察不到监听就退出 2）、跑大项目夹具 preflight
（真实启动整栈，失败在消耗模型额度之前暴露），再跑
`--execute --suite … --arms … --repeat 2 --require-comparable`。报告、gateway 检查日志、
构建身份和 agent 版本作为 artifact 保留 30 天，`report.md` 同时写入 job summary；
其中包含「oberth vs native」对照表。

运行方式：Actions → **Agent eval / real agent matrix** → Run workflow → 选择分支
（environment 只允许 `main`）→ 在 environment 审批提示中批准。没有批准的运行不会
拿到 secret，也不会调用模型。

它不是 required check，也不进入 `scripts/release.py`；发布证据仍然由人决定是否采纳。

## 发布前

按 [releasing.md](releasing.md) 的流程，发布候选构建在普通终端（或具备凭据的 CI）上跑
`--suite large --arms oberth,native --repeat 2` 的矩阵，把 `report.md`（含对照表）作为
发布证据附上。真实 agent 依赖外部 CLI、登录状态和网络，**不放进 `scripts/release.py`
的强制步骤**：发布脚本不应因为某个 provider 未登录而阻止引擎发布。没有跑的行要明确写
「未执行」，不能用参考 agent 或 fake engine 的行代替。

## 平台与边界

- 支持 macOS / Linux。shim 使用 Unix shebang，Windows 不在本工具范围。
- 大项目夹具同样只用 Python 3 标准库；没有 Docker、JVM 或 Node 依赖。真实 JVM / Node
  构建 / compose Postgres+Redis 是独立的手动档，不进入当前常驻矩阵。
- 大项目夹具会启动十到三十个真实进程，必须在能观察监听的环境运行；沙箱里
  `--dry-run --suite large` 会在 preflight 或首个真实启动处退出 2，这是环境限制，不是
  夹具失败。
- provider 事件解析按已知形状实现；形状变化时步数退化为命令数并标注，不静默归零。
- 超时、认证失败和 provider 崩溃记录为 `agent_error`，不能当作 agent「没完成场景」来解读。
- 场景库不是安全沙箱，也不是性能基准；它只回答固定场景下的核心闭环和对照差异是否可度量、
  可重复。
