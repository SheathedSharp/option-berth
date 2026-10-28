# 验证范围

当前工作流均记录实际源码身份；检查结论只对应运行中的 commit，不沿用旧库或本机的历史结果。
GitHub Actions 的自动验证仅在推送 `v*` 注释标签时由 `Verified release` 触发；日常 PR 与 `main` 推送不再启动 CI。需要分项检查时可在 Actions 里手动 `workflow_dispatch` 对应工作流，发版前仍应跑本地 `scripts/release-*.sh` 预览与 `--execute` 验证。

| 工作流 | 平台与内容 |
|---|---|
| Engine verification | macOS / Ubuntu：原生构建、vet、全引擎单元、核心 race、真实服务场景、Mage 工作流回归、Python 辅助测试。 |
| Git read verification | macOS / Ubuntu：Git、worktree、归因与观察的重复测试和 race。 |
| Windows build verification | Windows x64：原生构建、vet、CLI/Jev 构建、version JSON/帮助页、事实/持久化，以及隔离 API/无端口 worker 的启动、重复启动、采集器恢复和限定停止；Ubuntu：Windows arm64 交叉构建。 |
| Client and brand verification | macOS：共享几何源与13个SVG一致性、8项生成器测试、原生客户端/ICNS构建、PTY/输入框/会话导航回归及随包OFL检查。 |

CLI、Mage 与品牌迁移分别审查、验证、合并，没有引入旧私人 Git 祖先。
Windows 的基础服务场景与所选原生测试，不等于全部服务树、Windows GUI 或 arm64 原生执行验收。

## 本机复现

Go 版本按 `engine/go.mod`。服务测试使用独立 HOME/BERTH_HOME、socket 和自建服务。
macOS Unix socket 路径较短，测试不能把长测试名和多层临时目录全部拼到 socket 路径。

```sh
cd engine
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
go test -race -count=1 -timeout=10m ./internal/claims ./internal/runs ./internal/spawn ./internal/daemon/... ./internal/git ./internal/groups ./internal/ports ./internal/scanner ./internal/servicefacts ./internal/state ./internal/store ./internal/killer
go test -tags integration -count=1 -timeout=8m ./internal/scenario/...
```

Mage 与品牌检查分别位于各自PR及CI。冻结呈现与真实Dock、菜单或交互验收分开。
构建、截图、安装和服务起停是不同副作用，普通检查不操作日常用户服务。

## 不作出的保证

GUI全部交互、签名安装包和长期负载仍需独立验收；没有可靠对照不宣传整体加速或长期无泄漏。
自动密钥扫描、许可证输入保留和CI成功各自有边界，不能替代安全审查或完整分发审核。

## 工作记录

`go test ./internal/workplace/ ./internal/store/ ./internal/daemon/runsreg/ ./internal/cmd/`
（`-race` 同上）覆盖：

- workplace 的建、列、切换、删除与选择器：同名冲突、`ambiguous` 不替调用者挑一个、删掉当前那个
  之后由剩余里最新的接任、删空之后不再有当前 workplace。
- block 的写入规则：每个 workplace 内 `seq` 递增并有上限，超出丢最旧的；同一个 id 的第二次观察
  是更新；已经记下的结果不被迟到的 `running` 覆盖，空字段不清空已写内容。
- Run 退出收尾 block：`runsreg` 在记录退出与启动失败时都会通知（在注册表锁之外），
  `internal/workplace` 用 run_id 收尾对应 block，且只收尾仍是 running 的那些。
- CLI 端到端：真实 daemon socket 与自建 git worktree 下跑 `list` / `new` / `use` / `show` /
  `delete` / `record`，核对 JSON 文档；`oberth start -- /bin/echo hello` 核对写入的 run_id、pid、
  退出码；`BERTH_AGENT_STREAM_FAKE=codex` 下 `oberth agent stream` 核对一轮 turn 的 prompt 与
  outcome；daemon 不可达时读命令仍从同一份数据库读，写命令被拒绝。
- 非附加运行的两端：`go test -tags integration -run TestDetachedRunIsRecorded ./internal/daemon/`
  用真实二进制与真实 daemon 跑 `oberth start -d`，核对记录先出现且是 running、随后由 daemon
  收成 completed（并核对退出码、结束时间与它指向的日志确实存在）。

客户端不再保留第二份记录：`ClientChecks` 的 workplace 检查用合成 `oberth` 走完整条路径 ——
`oberth workplace list/show` 读回、`workplace record` 上报自己交互式 PTY 与原生 agent 的条目、
同一个 id 再报是更新、状态映射，以及输出经 `--output-file` 而不是 argv（2026-10-28）。
它不启动 daemon，因此不等于真实 daemon 下的上报验收。

**未执行**：真实 Codex / Claude 账号下的一轮任务在 workplace 里的端到端确认（合成驱动不算）；
macOS 客户端对**真实 daemon** 的上报路径（上面的检查用合成 CLI）。

## 核心验收：真实 coding agent 在一个陌生 worktree 上

对应 [backlog.md](backlog.md)「agent 侧的能力面是否够用」的完成条件。**2026-10-28** 在本机跑了
两轮，输入如下：

| 输入 | 值 |
|---|---|
| 引擎 | 本机构建 `v0.8.2`（commit `125108e`） |
| agent | Claude Code CLI（`claude -p`，本机默认模型 `deepseek-v4.1-flash-1017`） |
| skill | 随仓库分发的 `.agents/skills/option-berth/`，装进被测项目的 `.claude/skills/` |
| 环境 | 独立 `BERTH_HOME`；可丢弃项目 `ledger-api`（git 仓库 + linked worktree `feature/rate-limits`） |
| 任务 | 「服务没在跑，跑起来并确认它真的在运行；起不来就查清原因解决」——不提任何 oberth 命令 |
| 场景 | 清单已声明 `api`（`port: 8137`、`health: /health`）与只读 `machine:` 依赖 `ledger-db`（55432）；服务因生成物 `config/endpoints.json` 缺失（已 gitignore）退出 1 |

两轮都**完成**了任务：服务起在 `127.0.0.1:8137`，四个路由实测 200 / 200 / 200 / 404，`status`
报 `running` 且健康 `ok 200`，被跟踪文件零改动。第二轮的核心闭环只有 6 步：
`status` → `up`（失败）→ `logs`（给出确切原因）→ 重建生成物 → `up` → 实测确认。
`oberth.yaml` 两轮都没被改：agent 提出加 `prepare:` 的建议，把采纳留给用户。

卡在两处，两轮都出现，缺陷记录在 [backlog.md](backlog.md)：

1. 第一次 `status` 就发布 `attention_path`（`machine_unavailable`，指向 `ledger-db`）。skill 要求
   先处理这个交接，agent 为此花 8 步做 artifact 校验、找 `jev.json`、写 task context、arm 会话——
   比核心闭环本身还多。该依赖没有任何服务 `depends_on` 它，`up` / `down` 也不操作 `machine:` 条目。
2. 第一轮项目在 `$HOME` 之外，`up` 直接拒绝：错误只说「在 home 之外」，hint 指向一个**不存在**的
   日志；agent 靠 `oberth up --help` 才找到 `--allow-outside-home`。

**未执行**：Codex / OpenCode / Pi 的同场景验收；Windows / Linux 上的对应验收；多 worktree 并发。

## 终端工作区的分层验证

`TerminalChecks` 使用独立目录和自有子进程检查 cwd/PWD、退出回执、1 MiB 输出、24 次会话
回收、工作区草稿/模式/选中项保留、重命名约束、项目移除后会话可达性，以及混合停止状态下
只升级已有 stopping 会话。原生 NSWindow/NSSplitView 分别验证分隔尺寸、每栏焦点与输入、
终端历史查找和字号变化，不读取或修改用户剪贴板。

`AgentChecks` 默认使用合成 planner；设置 `BERTH_AGENT_TEST_BINARY` 后接入实际 oberth，
对照五个 provider 的 native/task 参数计划，再用临时 provider 替身验证原生 PTY 交接。
这不是登录五个真实模型账号，也不等于验收所有 provider 版本。

`go test ./internal/agentstream/...`（含 `-race`）与 `go test ./internal/cmd/ -run Agent` 校验桥 CLI 接线；模块拉取失败时可设 `GOPROXY=https://goproxy.cn,direct`。NDJSON 夹具符合 `engine/docs/schema/agent-stream.schema.json`；
`BERTH_AGENT_STREAM_FAKE=<provider>` 覆盖纯 NDJSON 假驱动；`testdata/fakecodex` / `testdata/fakeclaude` / `testdata/fakeopencode` / `testdata/fakepi` 经
`Options.LookPath` 覆盖 live 握手、两轮、busy、审批、未知请求 `notice`、关闭时中断与进程回收。
`oberth agent stream` 的 CLI 接线由 `internal/cmd` 的 `Agent` 测试抽查。真机 Codex / Claude 手测、输出上限截断
与完整项映射仍在 [console-plan.md](https://github.com/SheathedSharp/option-berth-macos/blob/main/docs/console-plan.md) 阶段 2 完成标准内；未安装 CLI 且未注入 lookPath 时首轮
`turn.start` 返回 `unsupported` 事件。

打开 worktree 会话时，目录存在且还没有 Shell，会启动一个交互式 Shell。从该 Shell 开始的外部 Agent 在返回 Shell 后仍是同一个 PTY，可以再次进入；`ConfigurationChecks` 覆盖这条路径。目录不存在时不创建进程。

**控制台原生回归（建议发版前本机一轮）**：也可从仓库根目录执行 `bash scripts/run_console_validation.sh`（本地少抢焦点：`QUIET=1 bash scripts/run_console_validation.sh`；含隔离 `ClientChecks`：加 `--with-client`）。

```sh
cd client/macos
swift run WorkspaceChecks
swift run AgentChecks
swift run TerminalChecks
isolation=$(mktemp -d)
mkdir -p "$isolation/home/.option-berth"
trap 'rm -rf "$isolation"' EXIT
HOME="$isolation/home" CFFIXED_USER_HOME="$isolation/home" BERTH_HOME="$isolation/home/.option-berth" \
  SHELL=/bin/zsh ZDOTDIR="$isolation/home" BERTH_CONSOLE_TEST=1 swift run ConsoleChecks
cd ../../engine && GOPROXY=https://goproxy.cn,direct go test ./internal/agentstream/... -race -count=1
cd .. && python3 scripts/check_client_configuration.py
```

本地少抢焦点时可加 `BERTH_CONSOLE_TEST_QUIET=1`（与 GitHub `console.yml` 无 quiet 的 CI 路径互补）。

**客户端原生回归（`ClientChecks`，与 `.github/workflows/client.yml` 一致）**：

```sh
binary_dir=$(swift build --package-path client/macos --show-bin-path)
isolation=$(mktemp -d)
mkdir -p "$isolation/home" "$isolation/berth"
trap 'rm -rf "$isolation"' EXIT
HOME="$isolation/home" CFFIXED_USER_HOME="$isolation/home" BERTH_HOME="$isolation/berth" \
  "$binary_dir/ClientChecks"
```

未设置隔离 `BERTH_HOME` 时，`GettingStartedChecks` 等会主动失败；不要裸跑 `swift run ClientChecks` 当作发版证据。

本机 **2026-10-15** 已跑通上文「控制台原生回归」命令块与隔离 `ClientChecks`（`WorkspaceChecks`、`TerminalChecks`、`AgentChecks`、`ConsoleChecks`、`go test ./internal/agentstream/... -race`、`check_client_configuration.py` 绿；`fish` 集成因未安装为 SKIP）。同日刷新 `mage states` PNG。**不包含**下方手测表与 PNG 人工目检。

`AgentChecks` 含阶段 0 纯函数：`ComposerRouterChecks`、`SlashParserChecks`、`AgentStreamReducerChecks`、`AgentRowProjectorChecks`、`agentSessionFileGuardChecks` 等。

`StreamConsoleChecks`（由 `ConsoleChecks` 调用）用合成 `oberth agent stream` 脚本与 `BERTH_STREAM_FIXTURE_LOG` 记录 stdin，
覆盖同一桥两轮（桥 PID 跨轮存活）、`/interrupt`→`turn.interrupt`、审批期间入队与详情「撤回」、`request.respond` 字节、Esc 不回答、⇧⌘↩ 第二会话、`session.close` 结束、桥自然退出（`session.ended`）后 ⌘↩ 续接并拉起新桥、`/native` 经 `agent plan --resume-file` 开原生 PTY、运行中结构化会话占用同一会话文件时 `/resume`（`BERTH_TEST_RESUME_FILE`）拒绝且不拉起原生 PTY，与 `BERTH_STREAM_UNSUPPORTED` 提示；不是真机 Codex/Claude。`AgentSessionFileGuard` 路径归一与冲突判定由 `AgentChecks`/`agentSessionFileGuardChecks` 覆盖。Shell→Agent 附件与 `ComposerAttachmentFormat` 由 `AgentChecks`/`composerAttachmentChecks` 覆盖；结构化流后台通知事件解析由 `consoleAgentAttentionChecks` 覆盖；`StreamConsoleChecks` 含详情「放到输入条」与 `berthOpenGitPath`；`ConfigurationChecks` 含 zsh 命令块「发给 Agent」与 `console.composerAttachment`；`streamShellAttachmentAgentSendCheck` 验证附件并入结构化首轮 `turn.start`；系统通知投递无专用 ConsoleChecks。引擎 `go test ./internal/agentstream/... -race` 在模块可拉取时执行（网络受限时可设 `GOPROXY=https://goproxy.cn,direct` 等）。
启用 Shell 集成且处于提示符时，`ConfigurationChecks` 还检查输入条聚焦时终端光标透明、在终端表面打字会桥接到输入条草稿（不含 ⌘/⌃ 修饰键）、↩ 启动前台命令后 `shellForegroundBusy` 与光标可见、向 PTY 发送 ETX 结束命令后回到提示符并恢复透明光标、**Tab** 将单行草稿与 HT 送入 Shell PTY 并从提示符行缓冲回读补全结果到草稿（`WorkspaceChecks` 覆盖 `ShellPromptLineCapture` 启发式与隔离 **bash** OSC 集成；**fish** 在 runner 装有可执行文件时执行，否则跳过）。
`11-console-split` 冻结图表达当前递归窗格模型；原生分隔控件和终端内容由独立原生检查验证。

**冻结呈现（`mage states`）**：2026-10-15 在本机多次执行 `mage states`（最近一次在会话文件冲突与 `/resume` 可见性修复之后），已写出 `client/macos/.cache/states/` 下含 `10-console-slash`、`10-console-no-provider`、`10-console-native-agent`、`10-console-stream-detail`、`10-console-stream-mixed`、`11-console-split` 等 PNG。自动化仅证明渲染管线成功；**全量逐张人工翻看**仍属发版前检查（手测表「`mage states` 全量目检」行），不能替代 `ConsoleChecks` 或真机手测。

**主窗口几何（`mage window`）**：2026-10-15 已写出 `client/macos/.cache/window.png`（空 worktree 主界面 + 菜单栏快照）。不覆盖会话工作台内混合 PTY/结构化流交互；后者见 `ConsoleChecks` 与手测表。
完整窗口的鼠标菜单、系统输入法、辅助功能及长时间负载仍需继续真实使用验收。
不要将“页面切换保留内存状态”写成“应用重启恢复进程与会话”。

### 控制台手测（截至 2026-10-15：**未执行**）

以下项在 [console-plan.md](https://github.com/SheathedSharp/option-berth-macos/blob/main/docs/console-plan.md) 阶段 1–3 完成标准中要求，**尚无自动化替代**；发版前须在本机执行并记录结果，或继续保持「未执行」：

| 项 | 说明 |
|---|---|
| 真机 Codex | 两轮对话、一次允许、一次拒绝、一次中断、结束后续接、`/native` |
| 真机 Claude Code | 同上 |
| 中文 IME | 组合中 ↩ / ⌘↩ 不提交；提示符处开始的组合转入输入条 |
| 复杂提示符 | powerlevel10k / starship 下 Shell 集成默认开启后的表现 |
| 系统通知 | 开启 `consoleAgentNotifications` 后，后台收到审批/完成通知（可选手测） |

#### 建议步骤（真机 Codex / Claude Code 共用）

在已登录 provider 的 worktree **会话**页：`settings.json` 设好 `defaultAgent`，Shell 集成默认开。每步失败即停，记入下表备注。

1. 打开会话 → 确认仅**一条**底部输入条，Shell 在左、Agent 栏在右；未选 Agent 时 ⌘↩ 提示选择。
2. **两轮** ⌘↩：第二轮应落在同一结构化会话（非新建），右栏见轮次与输出。
3. 触发需审批的命令 → 在右栏 **允许** 一次；再触发 → **拒绝** 一次（输入条 ↩/⌘↩ 不得代替按钮）。
4. 长轮次中 `/interrupt` 或详情「中断」→ provider 确认中断后状态恢复可发下一轮。
5. 结束会话（详情「结束」或 `/close` 并确认）→ ⌘↩ **续接**（同会话文件）并再发一轮。
6. 空闲或结束后 `/native` 或栏内「在原生终端中继续」→ 结构化桥关闭、主画布为原生 PTY；同文件勿与运行中结构化会话双开（应提示拒绝）。
7. **IME**：组合输入过程中 ↩ 与 ⌘↩ 不提交；在 Shell **提示符**处开始的组合应进入输入条草稿。
8. **复杂提示符**（p10k 或 starship）：前台命令时键盘在 PTY，结束后回到输入条；⌃C 可达 Shell。
9. **通知（可选）**：`consoleAgentNotifications: true`，应用后台时审批/轮次完成有系统通知，回前台 Dock 角标清除。
10. **`mage states` 目检**：逐张查看 `client/macos/.cache/states/` 下 `10-console-*`、`11-console-split`（可先 `mage states` 再目检）。

记录表 **构建/commit** 建议填 `git rev-parse --short HEAD`（例如发版候选 `db57292` 一带）。

#### 手测记录（执行后由人填写）

完成任一项后，把本节标题里的 **未执行** 改为最近手测日期；未测行保持 **未执行**。失败项写清复现步骤，不要冒充通过。

| 项 | 日期 | 构建/commit | 结果 | 备注 |
|---|---|---|---|---|
| 真机 Codex | | | 未执行 | 两轮；允许+拒绝；中断；续接；`/native` |
| 真机 Claude Code | | | 未执行 | 同上 |
| 中文 IME | | | 未执行 | 组合中 ↩/⌘↩；提示符处组合进输入条 |
| 复杂提示符 | | | 未执行 | p10k 或 starship；`shellIntegration` 默认开 |
| 系统通知 | | | 未执行 | 可选 |
| `mage states` 全量目检 | | | 未执行 | 路径：`client/macos/.cache/states/` |

## 有限 CLI 回收回归

`CLIIOTests.swift` 由 Client workflow 和发版检查直接编译运行，使用自身可执行文件作为
合成 CLI，不启动用户服务或真实 provider。覆盖双流压力、输出/进度帧上限、UTF-8、隐私
诊断、信号退出、拒绝 TERM、关闭管道后挂起、直接子进程改变进程组、后代持有/关闭管道、
主动脱组的有界夹具、预取消、非法输入与时限、工作目录，以及 12 次跨线程取消后的直接
子进程 reap 和文件描述符稳定性。后代退出用进程状态验证，不把 kill 返回值当作退出证明。

```sh
swiftc client/macos/Sources/CLI.swift client/macos/Sources/DaemonLaunch.swift \
  client/macos/Tests/CLIIOTests.swift -o /tmp/oberth-cli-io-tests
/tmp/oberth-cli-io-tests
```

这些是有限合成回归，不是所有 provider、内核不可杀状态或长期资源趋势的验收。

### Git 请求与草稿窗口

`GitCancellationTests` 将真实 GitStore、队列、解码器和 CLI 与合成自执行夹具连接，验证
切换 worktree、切换文件、移除选择以及 Store 销毁四条路径。下一请求发布前验证旧直接
子进程已被回收；取消错误不能覆盖当前页面，已丢弃的排队读取不能再启动。

`DraftRunStateTests` 的 14 项检查覆盖候选不等于成功、失败/取消、旧代际、一次性结果、
空/非法候选、错误后零退出和 YAML 字节保真。

`DraftSheetInteractionTests` 编译实际 SwiftUI 草稿窗口、CLI 与品牌组件，使用独立 HOME、
CFFIXED_USER_HOME、BERTH_HOME 和合成命令，在固定 600×440 的自有 NSWindow 内发送
原生鼠标事件。覆盖成功/失败/取消/视图消失四种场景，验证待退出时写入按钮禁用、成功后
编辑器回填，以及子进程真实回收。事件仅发送给夹具窗口，不操作全局鼠标或用户剪贴板。
该检查需要可创建 AppKit 窗口的 macOS 会话，已接入 Client workflow 和发版 verify。

冻结状态图不代替上述原生事件检查。这些检查不声称覆盖所有输入法、系统辅助功能、
真实 provider 账号、崩溃恢复或长期负载。

## macOS 功能入口与样式整合

`ClientChecks` 编译实际 BerthClient，在隔离的原生窗口中验证命令查询/Return、快捷键冲突与
原生按键保留、递归窗格/焦点/分隔、同一 PTY 独立窗口归还、恢复确认与显式新 Shell，以及
更新请求的代际/取消/错误隔离。更新测试用可控异步载入器，不发起联网查询。工作区导航检查
保留 `project:` / `ports:` 旧参数兼容，但统一映射到服务页，不保留重复内部页面分支。

命令块元数据仅在协议状态改变时发布；普通终端输出不再逐块比较整个历史数组。
`TerminalChecks` 重跑工作区移除、混合窗格关闭、24次PTY回收和原生输入；旧双栏状态已移除，
测试直接验证当前布局模型。项目移除后仍有会话时，布局快照继续保留其独立 worktree 入口。

冻结渲染覆盖 paper 1060×720、midnight 900×640 及最小窗口 760×520，各13张状态图，
查看代表性的 agent、服务、Git 与递归分屏图。ImageRenderer 不承载原生菜单/拖放视图；
冻结路径只画相同内容和几何，不启动真实服务或使用个人偏好。原生渲染与真实输入路径分开验收。

### 复审补充的状态边界

`RecoveryChecks` 验证关闭恢复时剪除已丢弃历史入口的布局引用、保留当前注册会话，并可重新
启用保存；恢复后重新订阅新的工作区对象。自动保存用例先排空启动时排队的保存，再编辑
provider，避免把旧排队任务误认为监听仍有效。这些用例不通过旧 PID 启动或停止进程。

`ChromeChecks` 将部分恢复的完整布局与可呈现终端集合分开，调用实际窗格动作共用的
`applyLayoutChange` 入口验证：普通焦点变化保留未打开的历史引用，未知新增引用原子拒绝，
只有显式单窗格操作可以丢弃其他布局引用；同时在原生窗口验证现有终端仍可获得焦点。
该用例不依赖固定鼠标坐标，也不声称覆盖全部鼠标拖放行为。

`WorkspaceChecks` 的实际 zsh 夹具还检查失败与成功命令之后既有提示钩子继续执行，并收到
原始退出码；不凭猜测修改正常的 shell hook 行为。分发回归验证混合 ad-hoc/notarized 资产
在任何网络写入前被拒绝，不把模拟公证结果或文件名当作真实 Apple 公证验收。


### 发布候选的原生验证台

CommandChecks、GitReviewChecks、ConsoleChecks 与 PersonalizationChecks 必须在同一候选提交运行。
前者记录各原生交互阶段，并对测试菜单跟踪的取消设五秒硬边界；不重发业务按键。
详情拖动使用真实宿主布局，鼠标事件保持窗口本地坐标，经 NSApplication.sendEvent 派发到测试自有窗口，
避免重新排队后的缩放/多显示器重投影；方向、目标宽度、固定范围与压缩迟滞的原有断言全部保留。
重复运行先建立完整测试宽度偏好。窗口合成只捕获测试拥有的终端窗口，使用 CoreGraphics 将原始图像色彩空间
转为明确的 sRGB RGBA 字节，保持九个背景样点和同一 PTY 断言；不以 NSCalibratedRGB 转换替代采集图像的色彩配置。
无法获得本机前台焦点时，不修改系统隐私授权；可对目标提交打 `v*` 注释 tag，由 Verified release 在 runner 上执行完整预检（会进入发版链路，失败时不应重用该 tag）。
