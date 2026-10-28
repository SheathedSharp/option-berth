---
name: develop
description: >-
  option-berth 仓库开发流程与边界。在实现功能、修 bug、跑构建/测试、改 CLI 或 macOS 客户端、
  或不确定该读哪份文档/如何验证时使用。给人读的完整说明见 docs/develop.md。
metadata:
  version: "1.0.0"
  source: "github.com/SheathedSharp/option-berth"
---

# develop（option-berth）

## 先读什么

1. 任务涉及的**契约**： [docs/product.md](../../../docs/product.md)、[docs/cli.md](../../../docs/cli.md)、[docs/architecture.md](../../../docs/architecture.md)
2. 人的开发指南（叙事与索引）： [docs/develop.md](../../../docs/develop.md)
3. 根 [AGENTS.md](../../../AGENTS.md)（与 develop 一致，偏约束）
4. 改 **macOS UI** 前必读： [client/macos/AGENTS.md](../../../client/macos/AGENTS.md)
5. 写 **git commit** 前必读： [git-commit skill](../git-commit/SKILL.md)
6. 用 **oberth** 查/控服务： [option-berth skill](../option-berth/SKILL.md)

## 硬边界（违反即错）

- 产品接口是 **`oberth`**（`engine/`）；Mage 仅开发工具，不混进产品语义。
- 一份运行真相：worktree + manifest + service + run + runtime facts；不另造状态源。
- 清单 **人确认**；模型只起草。`machine:` 依赖只读；产品 Git **只读**。
- 测试/场景用**隔离** `HOME` / `BERTH_HOME` 与可丢弃项目；**不要**动用户日常 daemon、账本、真实 Git 写入，除非用户明确授权。
- 未实际执行的检查**不要**声称通过；GUI 改动需区分冻结图 vs 真实交互（见 validation）。

## 改代码后最小验证

| 改动区域 | 至少执行 |
|----------|----------|
| `engine/` | `cd engine && go build ./... && go test -count=1 ./…`（触及包的范围） |
| `client/macos/` | `cd client/macos && swift build`；终端/会话相关加 `swift run TerminalChecks`；Console 相关加隔离 `ConsoleChecks`（见 [docs/validation.md](../../../docs/validation.md)） |
| 界面布局/样式 | `mage states` 并查看生成 PNG；菜单/窗口另用 `mage window` |
| CLI/JSON/清单 | 同步 schema、docs、回归；stdout JSON 无诊断混入 |

构建客户端：`mage buildClient` → `client/macos/build/OptionBerth.app`。

## 交付

- **分支：动手改代码前先建分支，不要直接在 `main` 或别人的迭代分支上写。**
  名字用 `<type>/<scope>-<主题>`：`type` 取 git-commit skill 那张表（`feat` / `fix` /
  `perf` / `refactor` / `test` / `docs` / `chore`），`scope` 与 subject 的 scope 一致，
  主题用短横线连接的英文小写。例：`perf/client-publish-coalesce`、
  `fix/client-console-status-control`。一条主题一条分支；同一主题的多条 commit 留在同一分支上。
  已经身处一条迭代分支时（如 `ui/*`），先确认这条分支是否就是要交付的那条 —— 是就别套娃再开，
  不是就从它切出上述命名的分支，并把已有改动一起带过去。
- 提交：中文 `type(scope): 主题`，一主题一 commit；subject 用**产品语言**，不用 M0/M1 等内部代号（见 git-commit skill）。
- 仅用户明确要求时 `git commit` / `git push` / 安装 / 发版。**建分支不算提交**，可以在动手时先建。
- 发版见 [docs/releasing.md](../../../docs/releasing.md)。

## 专题文档（按需）

- 会话/终端 UX：[docs/console.md](../../../docs/console.md)
- 验证范围与测试含义：[docs/validation.md](../../../docs/validation.md)
- 客户端 README（构建、states、window）：[client/macos/README.md](../../../client/macos/README.md)
