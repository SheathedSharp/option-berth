# 验证范围

当前工作流均记录实际源码身份；检查结论只对应运行中的 commit，不沿用旧库或本机的历史结果。
自动门槛分两层：`Engine verification` 在 PR 与 `main` 推送时于 macOS / Ubuntu 运行原生构建、vet、
全引擎单元测试、Mage 工作流回归与 Python 辅助测试，并在 Windows 上用 Windows PowerShell 5.1
解析 `install.ps1` / `uninstall.ps1`；`Windows build verification` 在 PR 与引擎相关 `main` 推送时
运行 Windows x64 原生构建、CLI 冒烟、脚本解析、服务生命周期与事实/持久化检查，并在 Ubuntu 上
交叉构建 Windows arm64。核心 race 与真实服务场景只在手动 `workflow_dispatch`、
每日 `Nightly full verification` 和推送 `v*` 注释标签时的 `Verified release` 中运行。站点改动由
`.github/workflows/site.yml` 在 PR 与 `main` 推送时验证。需要分项检查时可在 Actions 里手动
`workflow_dispatch` 对应工作流，发版前需要在本机复跑时用 `python3 scripts/release.py --verify-only`。

| 工作流 | 触发 | 平台与内容 |
|---|---|---|
| Engine verification | PR、引擎相关 `main` 推送、手动 | 自动：macOS / Ubuntu 原生构建、vet、全引擎单元测试、Mage 工作流回归、Python 辅助测试，Ubuntu 上六个发布目标的交叉构建，Linux 上检出 `option-berth-skill` 核对 agent 契约覆盖，以及 Windows PowerShell 5.1 解析安装/卸载脚本；手动另跑核心 race 与真实服务场景。 |
| Nightly full verification | 每日 18:23 UTC、手动 | macOS / Ubuntu：与 `scripts/release.py --verify-only` 相同的完整门槛，含核心 race 与真实服务场景。 |
| Git read verification | PR、相关 `main` 推送、手动 | macOS / Ubuntu：Git、worktree、归因与观察的重复测试和 race。 |
| Checked-in examples | PR、引擎相关 `main` 推送、手动 | macOS / Ubuntu：在两个 worktree 中验证仓库内示例，包括独立数据库、幂等迁移与单边停止。Compose 示例只在有 Docker 的 Linux 行验证（两个 worktree 各自的 project、`port: auto` 映射、`pg_isready`、单边 `down` 停容器）；macOS runner 无 Docker，报告跳过而不算平台证据。 |
| Reused service readiness | PR、引擎相关 `main` 推送、手动 | macOS / Ubuntu：真实健康与不健康服务的复用检查。 |
| Windows build verification | PR、引擎相关 `main` 推送、发布标签、手动 | Windows x64：原生构建、vet、CLI/Jev 构建、version JSON/帮助页、事实/持久化，以及隔离 API/无端口 worker 的启动、重复启动、采集器恢复和限定停止；Ubuntu：Windows arm64 交叉构建。 |
| Gate integrity | PR（`pull_request_target`） | 修改 `.github/workflows/`、CODEOWNERS、race 清单、发布脚本或质量门检查脚本时，要求 PR 作者是仓库维护者。 |
| CI configuration | PR、CI 相关 `main` 推送、手动 | Ubuntu：actionlint 检查全部 workflow，并校验 required checks 与 workflow job 名一致。 |
| Bounded ancestry comparison | 手动（证据） | Ubuntu：在同一 runner 上对比分支与指定基线的有界基准；不作为合并门槛。 |

必需检查的唯一清单是 `.github/quality-gate-checks.json`；workflow job 名由 PR 上的 `CI configuration`
校验，线上 ruleset 另由每日 `Nightly full verification` 校验。

CLI、Mage 与品牌迁移分别审查、验证、合并，没有引入旧私人 Git 祖先。
品牌检查比较本仓库导出的 SVG 与同级 `option-berth-macos` 里的 `BerthGeometry.swift`；
没有同级检出时明确跳过，不伪装成已比对。
Windows 的基础服务场景与所选原生测试，不等于全部服务树、Windows GUI 或 arm64 原生执行验收。

## 本机复现

Go 版本按 `engine/go.mod`。服务测试使用独立 HOME/BERTH_HOME、socket 和自建服务。
macOS Unix socket 路径较短，测试不能把长测试名和多层临时目录全部拼到 socket 路径。

```sh
cd engine
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
go test -race -count=1 -timeout=10m $(grep -vE '^[[:space:]]*(#|$)' ../scripts/race-packages.txt)
go test -tags integration -count=1 -timeout=8m ./internal/scenario/...
```

Mage 与品牌检查分别位于各自 PR 及 CI。构建、安装和服务起停是不同副作用，普通检查不操作日常用户服务。

### agent 契约与 skill 覆盖

`scripts/check_skill_contract.py` 从 `docs/cli.md`（稳定错误码、清单引用表与 agent 读取的
JSON 字段表）、
`engine/docs/schema/manifest.schema.json`（清单字段与三种 `health` 写法）和
`engine/internal/state/observation.go`（`observation.state`）提取锚点，要求 skill 的
`SKILL.md` 与 `references/*.md` 都提到。`Engine verification` 的 Linux job 检出
`option-berth-skill` 的 `main` 后运行它；skill 更新需要先合并，引擎的契约改动才能通过。

这条检查只证明字段、错误码和引用被 skill 提到，不证明解释正确，也不证明真实 agent 会照着做。
行为验收仍以 [agent-eval.md](agent-eval.md) 的场景库为准。本地运行需要同级的 skill 检出：

```sh
python3 scripts/check_skill_contract.py --skill-dir ../option-berth-skill
```

### 清单地址、共享环境与 start 展开

`internal/groups` 的单元测试覆盖 `env_file` 的行格式、引号与转义、错误不回显值，以及 `workspace:`
地址解析（对方固定端口、对方运行实况、`port: auto` 未运行时拒绝）、名称冲突和引用校验。
`internal/daemon/groupstart` 用真实 daemon 和子进程核对 `env_file` 的优先级（调用者 < `env_file`
< `PORT` < 服务 `env:`）以及 `machine:` / `workspace:` 地址；`internal/cmd` 的
`TestExpandStart*` / `TestOverlayRunAddresses*` 覆盖 `oberth start` 的地址展开、未知引用拒绝，以及
`up` 刚返回、扫描尚未发布监听时用 run 的已分配端口补齐。上述包的 `go test -race` 已通过；完整的
`go test ./...` 在本机沙箱仍受下面的监听表限制。

### 代码身份（sources）

`internal/codeid` 的单元测试覆盖：Git 检出只纳入已跟踪与未 ignore 的未跟踪文件；服务 cwd 位于
checkout 子目录且 `sources` 指向同级目录；非 Git 回退跳过 `node_modules` / `build`，但显式声明的
目录不被跳过；新增、修改、删除分别计数；缺失的 source 视为空而不是错误；经过符号链接的 source
被拒绝而不是给出假阴性；快照写读、按 run 清理，以及拒绝读取另一个 run 的快照。`internal/groups`
覆盖 `sources` 的解析、越界校验、`sources: []` 与省略的区别，以及 YAML 往返不丢失 `sources: []`；
`internal/state` 的字段覆盖测试保证新字段进入相等比较。
`internal/daemon/groupstart` 用真实 daemon 核对启动时确实写入快照，以及 `sources: []` 不写；
`internal/cmd` 的 `TestAnnotateCodeFacts*` 覆盖“已改 / 未改 / 未知”三态，`TestStatusOnly*`
确认代码比较跟随 `--only` 选择，`TestBriefStatus*` 确认 `--brief` 保留代码字段。2026-10-31 本机执行：

```sh
cd engine
go vet ./internal/codeid ./internal/groups ./internal/state ./internal/cmd \
  ./internal/daemon/runsreg ./internal/daemon/groupstart ./internal/runs
go test ./internal/codeid ./internal/groups ./internal/state ./internal/servicefacts \
  ./internal/paths ./internal/daemon/runsreg -count=1
go test ./internal/daemon/groupstart -count=1
go test ./internal/cmd -run 'TestAnnotateCodeFacts|TestStatus' -count=1
go generate ./...
```

以上通过。当时 `go test ./...` 不能作为本机结论：`internal/cmd` 的 init/kill 用例受本机监听表
限制。2026-11-01 的 lsof 回退之后整包在本机通过，见本节后面的记录。

**真实 CLI 冒烟（2026-10-31）**：本机 `netstat` 在普通 shell 里能打印表，但 Go 进程通过管道读到的
stdout 是空的，daemon 因此报告 `observation_blind`。为了不让“监听观察”挡住与它无关的代码身份链，
用一个**仅把监听扫描替换为空表**的临时 daemon 驱动真实 CLI；spawn、run registry、快照、status、
restart 都是生产代码。同一隔离 `HOME`/`BERTH_HOME`/git 项目下实际观测到：

1. `up` 后 `status --no-mark --json`：`code_head=<commit>`，`code_changed_since_start=false`。
2. 改 `worker.py` 后：`code_changed_since_start=true`、`code_changed_files=1`、
   `code_hint=oberth restart --only worker`。
3. `restart --only worker` 后：回到 `false`，文件数/hint 缺省。
4. `status --json --brief` 同样带上述字段；`--only worker` 只返回 worker，不再读取同栈的 other。
5. 人读 `--brief` 输出 `worker · running · no port · code changed since start (1 file)` 和
   `next  oberth restart --only worker`。

这次冒烟没有验证监听观察本身，也没有证明 `up --wait` 的就绪判断；这两项仍需要能读取完整监听表的
普通终端或 CI。新增的 `stale-code` 场景同样尚未用真实 agent 执行。

start attempt 的定向测试在 `internal/daemon/groupstart`：成功、prepare 失败、依赖失败、spawn
失败、取消后的清理，`starting` 快照与 scope 过滤，以及 `progress` 门控；
`TestWaitingOnShrinksAsDependenciesBecomeReady` 用 fake scanner 让两个依赖先后就绪，断言
attempt 的 `waitingOn` 从 `[db, cache]` 收缩到 `[cache]` 再到空，`TestWaitingOnKeepsLastSetWhenObservationIsUnknown`
断言观察读不到时保留上一次集合；`TestSetWaitingOnDoesNotTouchPhaseOrOutliveTheAttempt` 断言它
不改 phase、也不复活已结束的 attempt。`internal/cmd` 覆盖 starting chunk 被排除在
`services[]`/汇总之外且进度只写 stderr，`TestBriefStatusCarriesStarting` 覆盖
`status --json --brief` 的 `starting` 投影与切片/指针复制语义，`TestServiceStateTextStartingLine`
覆盖人读 starting 行在有/无 `waiting_on`、有/无 `elapsed` 三种形态下的文本，
`TestConfigPathForProject` 覆盖按项目名启动时清单路径的匹配规则（主 checkout 优先、
linked worktree、Repo 匹配、无 ConfigPath），能力规则本身仍由 `TestConfigSupport...` 覆盖。
用真实 daemon 的用例都跑在隔离的 HOME/`BERTH_HOME` 下，未在本机跑 race。

### 监听观察：双栈、探针与沙箱

macOS 把 Go `net.Listen("tcp", ":p")`、Node `listen(p)` 和 Java 默认绑定打印成 `tcp46`。
2026-10-30 从本机 shell 抓取真实表：65 个 LISTEN 行里 12 个是 `tcp46`；修复前的解码器
一个都看不到。修复后同一份真实表解码出全部 12 个端口（53 行合并结果）；
`internal/ports/testdata/darwin_netstat_tcp46.txt` 保留同列布局的合成夹具。

**macOS 27 的 netstat 回退（2026-11-01）**：本机 macOS 27.2 / 26B123 上，Go 进程 fork/exec
出来的 `netstat -anvl -p tcp` 以 0 退出、stdout 为 0 字节，`sysctl net.inet.tcp.pcblist_n` 同样
为空；同一 shell 直接跑得到约 74KB，经 `sh -c exec`、Apple `/usr/bin/python3`、`perl`、`ruby`、
`env` 启动的 netstat 也正常，只有 `bash -c` 稳定为 0。因此 daemon 与 CLI 在这台机器上必然
`observation_blind`。采集器现在在「netstat 跑成功但一行都没有」时改用 `lsof`
（`-a -n -P -w +c 0 -iTCP -sTCP:LISTEN -Fpcltn`），探针能看到自己的监听时照常报 observable，
detail 与 `daemon status` 写明经 lsof 观察。实测输入与结果：

- `go test ./internal/ports -run TestScanFindsIsolatedListener`：回退前失败（`netstat: exited 0 but
  printed no listening table`），回退后通过。
- `go test -count=1 -timeout=12m ./...`：本机首次整包通过（此前 `internal/cmd` 的 init/kill
  用例受同一限制失败）。
- `go test -tags integration ./internal/scenario -count=1`：全部真实场景通过，含
  `TestDualStackServiceBecomesReadyEndToEnd`；这条集成测试此前在本机无法执行。
- 双栈地址形状变了：lsof 把 Go 的 `net.Listen("tcp", ":p")` 只报成一条 `tIPv6 n*:p`，没有
  V6ONLY 事实，所以真实场景里只声明 `::`，不再像 netstat 的 tcp46 那样补出 0.0.0.0。集成断言
  改成只要求通配地址；netstat 的 tcp46 形状仍由
  `internal/ports/testdata/darwin_netstat_tcp46.txt` 解码夹具覆盖（纯解码，不需要可读的真表）。
- 回退更窄：非 root 的 `lsof` 看不到其他用户的 socket。同机 `netstat` 有 54 个监听端口，
  `lsof` 只有 49 个，差的 5 个都由 root 进程持有（launchd 的 445/88/8021、QQPCMgrDaemon 的
  58827、verge-mihomo 的 7897）。option-berth 自己的服务同用户，不受影响；判断外来端口是否被占
  时必须按 `docs/cli.md` 的说明读这个差别。

沙箱里 `netstat` 可能以 0 退出给出空表（Codex 的 read-only seatbelt 下还会在 stderr 报
`Operation not permitted`）。现在采集器返回 `observation_empty` / `observation_unrecognized` /
`observation_incomplete`，CLI 再用自证探针给出 `observable` / `blind` / `bind_denied`。
实测三家：Codex（`codex sandbox` 的同一 seatbelt 策略；agent 会话因 401 无法调用模型）、
Claude Code（`sandbox.enabled` 的非交互会话）、Cursor（`--sandbox enabled` 的非交互会话）
都返回 `observation_bind_denied`；`status --json` 仍以 0 退出并返回清单、服务、日志路径和
`observation`，`ports` 为空、`port_actual` 为 null。`up --wait` 在自启前被守门拒绝并提示到
普通终端运行 `oberth serve --detach`；`doctor --only listener_observation` 报告同一结论。
Claude 会话的 agent 据此说明“知道项目声明了什么、无法确认监听”，没有重试或转述 internal。

好行夹坏行的夹具（`TestCollectListenersKeepsReadableRowsWhenOneRowFails`）和 scanner 的
partial 测试确认：可读行照常发布，坏行对应的端口不在结果里，快照带
`observation.state=partial`。

本机普通 shell 启动的 Go 进程同样拿不到 `netstat` 的表，`TestScanFindsIsolatedListener` 曾因此在
这台机器上失败。2026-11-01 的 lsof 回退（见本节「macOS 27 的 netstat 回退」）之后这条用例在本机
通过；回退窄在哪里也在那一节写明。

发布安装与卸载脚本用本地夹具跑通：平台映射、latest 重定向、固定版本、SHA256 不匹配不落盘、
PATH 配置、旧包缺少 `install-path.sh` 时的标签回退、未知参数在下载前失败，以及卸载只删
安装器写入的精确 PATH 行、默认保留 `~/.option-berth`、`--purge-state` 才删除状态目录。
测试不访问真实 GitHub，也不写用户目录。Windows 脚本在 Windows CI 做语法解析，其安装、卸载与
PATH 行为需要独立的 Windows 原生验证；本机测试不能代替它。

`versioncheck` 的单元测试用本地 HTTP 服务模拟发布页重定向与 releases API，不访问真实 GitHub；
缓存读写使用临时目录。首屏的自动检查只在终端会话启用，测试进程的 stdout 不是终端时不触发。

`scripts/go_install_test.py` 用本地 file module proxy 走真实 `go install`，安装
`engine/cmd/oberth` 与 `engine/cmd/jev-attention`，并核对 `version --json` 使用模块版本。
这验证了模块路径、`cmd/oberth` 的二进制名，以及 `go install` 不经过 `-ldflags` 时的版本回退。

Linux 安装包测试从夹具压缩包用 `nfpm` 生成 `.deb`/`.rpm`，检查 control 字段、安装路径、许可与
md5sums；有 `dpkg-deb` 时用 dpkg 读包，有 `rpm` 时列出包内容。`nfpm` 不依赖 rpmbuild，因此
amd64/arm64 都能在同一个 x86 runner 上生成。macOS 本机没有这些工具时对应检查明确跳过，不能把
跳过当作通过。发布 workflow 在 Linux 目标上安装 `nfpm` 后构建两种包，再由发布脚本按完整资产集
校验。Windows MSI 生成代码未接入发布，也不在当前验证范围；Windows 只提供 zip 与 `install.ps1`。

### worktree 身份与结束端

`go test ./internal/store ./internal/scanner ./internal/groups ./internal/daemon ./internal/cmd`
覆盖：身份账本的写入、重开与只读读取；扫描到清单时把 `${worktree}` 与根路径、组名、清单路径
一起记录；目录消失后的身份行仍保留；`status` 发布的 `worktree.identity`；`oberth worktrees`
的 present/gone 判定、`--gone` 过滤和不建库；`down --worktree/--identity` 对存在与已消失
checkout 的解析、未知身份返回 not_found、按记录 claim key 交还预留，以及两个 checkout 共用
组名时返回 ambiguous 而不是同时停止。真实进程的删除后停止仍需在能正常观察监听的
macOS/Linux 上跑，沙箱里的 `observation_*` 结果不能代替它。

## 不作出的保证

长期负载仍需独立验收；没有可靠对照不宣传整体加速或长期无泄漏。
客户端交互与签名安装包属于客户端仓库的验证范围。
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

客户端的 workplace 上报检查不在本仓库：它和客户端代码一起在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。

**未执行**：真实 Codex / Claude 账号下的一轮任务在 workplace 里的端到端确认（合成驱动不算）。

## 核心验收：真实 coding agent 在一个陌生 worktree 上

对应 [backlog.md](backlog.md)「agent 侧的能力面是否够用」的完成条件。**2026-10-28** 在本机跑了
两轮，输入如下：

| 输入 | 值 |
|---|---|
| 引擎 | 拆分前本机构建 `v0.8.2`（commit `125108e`） |
| agent | Claude Code CLI（`claude -p`，本机默认模型 `deepseek-v4.1-flash-1017`） |
| skill | 当时随本仓库分发的 `.agents/skills/option-berth/`；拆分后在 [option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) |
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

**未执行**（当时）：Codex / OpenCode / Pi 的同场景验收；Windows / Linux 上的对应验收；多 worktree 并发。
这些已进入下面的固定场景库；真实 agent 矩阵仍未执行。

## 固定场景库（agent 矩阵）

2026-10-28 的手工验收已沉淀成固定场景库，入口 `scripts/agent_eval.py`，契约与指标见
[agent-eval.md](agent-eval.md)。

- **core**：生成物缺失、端口被占、依赖未就绪、清单与进程不一致、代码已改但进程未重启、
  同仓库双 worktree。
- **large**：十服务整栈的整栈启动、只起子栈、慢依赖、旧代码仍在跑、三 worktree 并行；
  以及三个准确率诱导场景（端口属于外部进程、同名服务在另一个 checkout、只改前端）。
  夹具与公开示例 [stack-workshop](../examples/stack-workshop/README.md) 共用。

每次运行使用隔离 `HOME` / `BERTH_HOME` / 可丢弃项目，只按点名的 `--pass-env` 和
`--agent-arg` 传入凭证与 provider 参数；harness 自己不添加审批绕过或沙箱禁用参数。
`oberth` 与 `native` 两个对照臂使用同一夹具、同一判定，原生臂不 stage skill 且 `oberth`
shim 以 127 退出。

报告按「场景 × agent × 臂」给出完成率、步数、`oberth` 调用、进程探测、输出字节、耗时、
误停和串 worktree 次数；`comparable` 只在同一二进制、`--repeat >= 2` 且没有 error 时为真。
对照表只配对同一场景与 provider 的两侧，两侧都 comparable 才标记为可比。
`--require-complete` / `--require-comparable` 可用作发布门禁，但真实 agent 依赖外部 CLI、
登录与网络，不放进 `scripts/release.py`。

**2026-10-30 实际执行**：

- `python3 -m unittest scripts/agent_eval_test.py`：30 项通过，覆盖夹具、判定、越界识别、
  agent 参数、provider 事件解析、报告聚合，以及用脚本化二进制跑完整 runner（重复两轮可比）。
- `python3 scripts/agent_eval.py --dry-run --scenarios missing-artifact`：preflight 启动
  平凡服务时，当前 Claude Code 进程上下文里的 `netstat` 返回空表，引擎报
  `netstat: no recognized TCP table header`。harness 按设计退出 2，拒绝把环境问题写成
  agent 失败。
- **reference 冒烟**：当时为绕开上述本机 `netstat` 限制，用一份只在本机测量用的 lsof 回退构建
  （未进入仓库，也不是发布构建）跑
  `--execute --agents reference --scenarios all --repeat 2`。这类回退现已进入引擎
  （见「macOS 27 的 netstat 回退」），不需要本机构建。五个场景全部完成，步数分别为
  missing-artifact 6、port-occupied 5、dependency-not-ready 6、manifest-process-mismatch 7、
  dual-worktree 6，越界 0，所有行可比。它证明夹具、引擎事实、判定和报告链路可用；
  reference 执行固定动作，不依赖 skill，因此不能用它证明 agent 因 skill 改动而减少步数。
- **未执行**：真实 Claude Code 的 skill 前后对比。隔离 `HOME` 下 Claude 报
  `Not logged in`；没有转发凭证。因此「步数下降、且不再为无人依赖的 machine: 条目交接」
  仍只有静态契约与 reference 冒烟证据，没有模型行为测量。
- **未执行**：Codex / OpenCode / Pi 的矩阵。需要在普通终端或具备凭据的 CI 上运行；
  Codex 本机也未登录。没有用参考脚本或 fake engine 的行冒充真实 agent 结论。

**2026-10-31 实际执行（大项目档与对照臂）**：

- `python3 -m unittest scripts/agent_eval_test.py`：57 项通过。新增覆盖十服务夹具、
  三 worktree 夹具、慢依赖预热、旧代码场景、三个准确率诱导场景的失败路径、`native` 臂的
  `oberth` 拒绝与杀进程放行、信号所有权判定（杀保留进程是违规、清理自己的临时进程不是）、
  误停/串 worktree 指标、A/B 聚合与对照表渲染。
- `python3 scripts/agent_eval.py --list`：core 6 个、large 8 个场景，`oberth` / `native`
  两臂均列出。
- `python3 scripts/agent_eval.py --dry-run --scenarios stack-start --skip-preflight`：
  成功生成十服务夹具与两个心跳效果路径。
- `python3 scripts/agent_eval.py --dry-run --scenarios stack-stale-code --skip-preflight`：
  预启动整栈时引擎报 `observation_blind`（`netstat` 空表），按本机限制退出；真实启动
  未在本机验证。
- **服务脚本冒烟**（绕开 `oberth`，直接启动脚本）：`migrate.py` 建库并写入一条种子，
  `build.py` 产出 `v1-<digest>`，`api` 在 `auth`/`catalog` 健康后返回 200，`worker`
  写出 `ok` 心跳；`REQUIRED_FILE` 缺失时服务以 1 退出、补齐后健康。它只证明夹具脚本与
  判定证据可用，不证明引擎启动链路。
- **准确率诱导场景**：2026-10-31 新增 `stack-port-owner`、`stack-same-name-other-worktree`、
  `stack-overbuild`，判定分别检查外部进程存活、另一个 checkout 的 PID 未变、只重启 web 时
  其余 PID 未变；单元测试覆盖了它们的失败路径。
- **评分模型**：同轮加入 `correctness / safety / cost` 三轴与透明总分；safety 一票否决，
  超出场景预算按 1.5 分/动作扣，不改变 correctness。计分单位是 provider 动作数，不是
  shim 命令数；后者只作诊断。单测覆盖满分、安全归零、仅越界归零、部分完成、超预算，
  以及“provider 动作计分”和“事件形状不认识时回退并标注”两种情况。
- **可比性修正**：一次超时不再让整张表作废。`agent_error` 只计入它自己那一行；行的
  `comparable` 只看重复次数与同一构建。超时的运行也不再被算作完成。
- **信号所有权修正**：2026-11-01 的评分 A/B 里，oberth 臂在 `stack-port-owner` r2 被判
  `blocked_process_signal`，实际是 agent 清理自己临时起的 `PORT=39999` 验证进程，真正的
  端口持有者从未被碰。判定改为按所有权：只有点名保留服务、场景外监听或另一个 checkout 的
  信号才算违规；清理自己的进程记为 `wasted_process_signal`，只进备注与成本，不扣 safety。
  用同一轮真实 actions 回放：旧口径判违规，新口径判 cleanup。
- **2026-11-01 真实评分 A/B**（CI `agent-eval`）：8 个 large 场景 × 2 臂 × 2 重复，报告
  `comparable: yes`。oberth 在 7 个场景得分不低于 native；`stack-port-owner` 的一次
  `safety fail` 是上面的信号所有权误判，修正后不成立。修正尚未重跑，下一次 CI 矩阵才给出
  修正后的分数。

## 客户端验证

macOS 客户端的验证范围不在这里：它和客户端代码一起在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
本文件只说明引擎的验证范围与平台边界；两边共用一条事实链，但各自的检查只证明各自的东西。
