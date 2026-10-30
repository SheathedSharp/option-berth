# 验证范围

当前工作流均记录实际源码身份；检查结论只对应运行中的 commit，不沿用旧库或本机的历史结果。
自动门槛分两层：`Engine verification` 在 PR 与 `main` 推送时于 macOS / Ubuntu 运行原生构建、vet、
全引擎单元测试、Mage 工作流回归与 Python 辅助测试；核心 race 与真实服务场景只在手动
`workflow_dispatch` 和推送 `v*` 注释标签时的 `Verified release` 中运行。站点改动由
`.github/workflows/site.yml` 在 PR 与 `main` 推送时验证。需要分项检查时可在 Actions 里手动
`workflow_dispatch` 对应工作流，发版前仍应跑本地 `scripts/release-*.sh` 预览与 `--execute` 验证。

| 工作流 | 触发 | 平台与内容 |
|---|---|---|
| Engine verification | PR、引擎相关 `main` 推送、手动 | 自动：macOS / Ubuntu 原生构建、vet、全引擎单元测试、Mage 工作流回归、Python 辅助测试；手动另跑核心 race 与真实服务场景。 |
| Git read verification | 手动 | macOS / Ubuntu：Git、worktree、归因与观察的重复测试和 race。 |
| Windows build verification | 发布标签（`Verified release` 调用） | Windows x64：原生构建、vet、CLI/Jev 构建、version JSON/帮助页、事实/持久化，以及隔离 API/无端口 worker 的启动、重复启动、采集器恢复和限定停止；Ubuntu：Windows arm64 交叉构建。 |

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
go test -race -count=1 -timeout=10m ./internal/claims ./internal/runs ./internal/spawn ./internal/daemon/... ./internal/git ./internal/groups ./internal/ports ./internal/scanner ./internal/servicefacts ./internal/state ./internal/store ./internal/killer
go test -tags integration -count=1 -timeout=8m ./internal/scenario/...
```

Mage 与品牌检查分别位于各自 PR 及 CI。构建、安装和服务起停是不同副作用，普通检查不操作日常用户服务。

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

**未执行**：Codex / OpenCode / Pi 的同场景验收；Windows / Linux 上的对应验收；多 worktree 并发。

## 客户端验证

macOS 客户端的验证范围不在这里：它和客户端代码一起在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
本文件只说明引擎的验证范围与平台边界；两边共用一条事实链，但各自的检查只证明各自的东西。
