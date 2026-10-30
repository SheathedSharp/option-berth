# 开发说明

给人读的日常开发指南：怎么构建、怎么跑、改完跑什么。契约以 [product.md](product.md)、
[cli.md](cli.md)、[architecture.md](architecture.md) 为准；agent 需要先知道的硬边界在根目录 [AGENTS.md](../AGENTS.md)，
本文不重复那部分。

## 仓库怎么拆

| 区域 | 路径 | 说明 |
|------|------|------|
| 引擎与 CLI | `engine/` | `oberth` 产品接口；Go 版本见 `engine/go.mod` |
| 开发自动化 | 仓库根 `mage` 目标 | 构建与验证入口；**不要**把 Mage 当成产品 CLI |
| 文档 | `docs/` | 引擎与产品契约、验证范围、发版见 [validation.md](validation.md)、[releasing.md](releasing.md) |

界面与给 agent 的指引不在这个仓库，它们是独立产品：
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)（macOS 客户端）与
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)（coding agent 的 skill）。
三者只通过 CLI 与 `--json` 契约相连。

实现边界与不可违反的约束见根目录 [AGENTS.md](../AGENTS.md)，本文只讲怎么动这些代码。

## 构建与运行

在仓库根目录：

```sh
mage build          # 引擎 + Jev adapter
mage buildEngine    # 仅引擎 → ./bin/oberth
mage runCLI "status --json"
mage runDaemon      # 用刚构建的 CLI 起 daemon（隔离用途）
mage stop
mage schema         # 重新生成 protocol 与 manifest JSON Schema
```

`mage install` 会写入安装目录并改 PATH（可用 `NO_MODIFY_PATH=1` 跳过）；**未经明确授权不要**对用户日常环境执行安装或发布。

客户端与 skill 各自在自己的仓库里构建和验证，见它们各自的 README 与 AGENTS.md。

## 验证（改什么跑什么）

细则与边界见 [validation.md](validation.md)。常用入口（在 `engine/` 下）：

```sh
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
```

PR 与 `main` 推送会自动在 macOS/Linux 上跑这三项，以及 Mage 与 Python 辅助测试，并在 Windows
上运行原生构建与脚本解析；Linux job 还会用 `CGO_ENABLED=0` 交叉构建六个发布目标。核心 race、
真实服务场景等完整门槛仍按 [validation.md](validation.md) 在每日、手动或发版流程中执行。
CI 配置改动还会跑 actionlint 与必需检查一致性检查，清单在 `.github/quality-gate-checks.json`。

若 `proxy.golang.org` 拉模块超时，在同一 shell 前缀 `GOPROXY=https://goproxy.cn`（或你环境可用的镜像）再执行上述命令。只设 `GOPROXY` 即可，Go 会通过代理的 `/sumdb/` 端点核对校验和，不需要 `GOSUMDB=off`；也不要追加 `,direct`，代理未命中时会回落到直连 GitHub 并长时间卡住。超时只表示网络，不代表代码不能编译。

脚本、发布与品牌生成：

```sh
python3 -m unittest discover -s scripts -p '*_test.py'
python3 brand/build-option-berth.py --check
python3 scripts/agent_eval.py --list       # 固定 agent 场景库
python3 scripts/agent_eval.py --dry-run    # 只做 preflight 与夹具，不启动 agent
```

真实 agent 矩阵需要在普通终端（不在 coding agent 沙箱内）或具备凭据的 CI 上跑。
本仓的手动 CI 入口是 `.github/workflows/agent-eval.yml`，它挂在需要 reviewer 批准的
`agent-eval` environment 上；指标、模型与 Codex bridge 的边界见
[agent-eval.md](agent-eval.md)。

隔离环境与授权边界见 [AGENTS.md](../AGENTS.md)「验证」；接口兼容和
「取消/未知不算成功」这类实现约束见 [AGENTS.md](../AGENTS.md)「接口」「一条事实链」。

## 提交与协作

本仓额外要求：subject 用产品语言，不写本仓内部的里程碑代号（`M0`–`M3`、阶段名），
也不用「闭环」「补齐」这类指向不明的词。`scope` 与改动的树一致（`engine`、`docs`、`ci`、`brand`…）。

- 推送前 `git log` / `git status`，重读目标 HEAD，注意与他人并行提交。
- 合并走远端 PR：分支推到 `origin` 后开 PR，等 `Engine verification` 通过并经用户确认后
  用 merge commit 合并；不在本地直接合入 `main`。发布由 `scripts/release-*.sh` 原子推送，
  见 [releasing.md](releasing.md)。
- 发版与标签流程见 [releasing.md](releasing.md)，版本与标签的约定只在那里维护。

## 相关专题

- CLI 与 RPC 契约：[cli.md](cli.md)；产品边界：[product.md](product.md)；实现与事实链：[architecture.md](architecture.md)
- 可选 attention adapter：[jev-adapter.md](jev-adapter.md)
- 客户端：[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)
- coding agent 的 skill：[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)
