# 开发说明

给人读的日常开发指南：怎么构建、怎么跑、改完跑什么。契约以 [product.md](product.md)、
[cli.md](cli.md)、[architecture.md](architecture.md) 为准；agent 需要先知道的硬边界在根目录 [AGENTS.md](../AGENTS.md)，
本文不重复那部分。

## 仓库怎么拆

| 区域 | 路径 | 说明 |
|------|------|------|
| 引擎与 CLI | `engine/` | `oberth` 产品接口；Go 版本见 `engine/go.mod` |
| macOS 客户端 | `client/macos/` | Swift 包；改 UI 前读 [client/macos/AGENTS.md](../client/macos/AGENTS.md) |
| 开发自动化 | 仓库根 `mage` 目标 | 构建与验证入口；**不要**把 Mage 当成产品 CLI |
| 文档 | `docs/` | 引擎与产品契约、验证范围、发版见 [validation.md](validation.md)、[releasing.md](releasing.md) |
| 客户端文档 | `client/macos/docs/` | 工作台契约与计划、安装与首次使用、个性化配置、Git 审查 |

实现边界与不可违反的约束见根目录 [AGENTS.md](../AGENTS.md)，本文只讲怎么动这些代码。

## 构建与运行

在仓库根目录：

```sh
mage build          # 引擎 + 客户端 + Jev adapter
mage buildEngine    # 仅引擎 → ./bin/oberth
mage buildClient    # 仅 macOS 客户端 → client/macos/build/OptionBerth.app
mage runCLI "status --json"
mage runDaemon      # 用刚构建的 CLI 起 daemon（隔离用途）
mage stop
```

仅改客户端时也可：

```sh
cd client/macos
VERSION=0.1.0 ./build.sh
./build.sh --run
```

`mage install` 会写入安装目录并改 PATH（可用 `NO_MODIFY_PATH=1` 跳过）；**未经明确授权不要**对用户日常环境执行安装或发布。

## 验证（改什么跑什么）

细则与边界见 [validation.md](validation.md)。常用入口：

**引擎（在 `engine/` 下）：**

```sh
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
```

若 `proxy.golang.org` 拉模块超时，在同一 shell 前缀 `GOPROXY=https://goproxy.cn,direct`（或你环境可用的镜像）再执行上述命令；超时只表示网络，不代表代码不能编译。

**macOS 客户端（在 `client/macos/` 下）：**

```sh
swift build
swift run TerminalChecks
```

完整控制台与 `ClientChecks` 隔离命令见 [validation.md](validation.md)「控制台原生回归」「客户端原生回归」；仓库根目录可一键跑 `bash scripts/run_console_validation.sh`（`QUIET=1` 少抢焦点，`--with-client` 含隔离 `ClientChecks`）。**不要**裸跑 `swift run ConsoleChecks` / `swift run ClientChecks`（须按文档设置 `HOME`、`BERTH_HOME` 等）。Console 也可单独加 `BERTH_CONSOLE_TEST_QUIET=1`（见 `console.yml` 与无 quiet 的 CI 路径互补）。

**界面改动额外要求：**

- 冻结状态：`mage states`（改布局/样式后查看 PNG）
- 真实窗口几何与菜单：`mage window`
- 不要把离屏渲染当成已覆盖鼠标、输入法与长时间负载

隔离环境与授权边界见 [AGENTS.md](../AGENTS.md)「验证」；接口兼容和
「取消/未知不算成功」这类实现约束见 [AGENTS.md](../AGENTS.md)「接口」「一条事实链」。

## 提交与协作

本仓额外要求：subject 用产品语言，不写本仓内部的里程碑代号（`M0`–`M3`、阶段名），
也不用「闭环」「补齐」这类指向不明的词。`scope` 与改动的树一致（`client`、`engine`、`docs`、`ci`…）。

- 推送前 `git log` / `git status`，重读目标 HEAD，注意与他人并行提交。
- 发版与标签流程见 [releasing.md](releasing.md)，版本与标签的约定只在那里维护。

## 相关专题

- 终端与会话 UX 契约：[console.md](../client/macos/docs/console.md)；实施计划与发版前缺口：[console-plan.md](../client/macos/docs/console-plan.md)
- macOS 使用与截图：[macos-guide.md](../client/macos/docs/macos-guide.md)、[client/macos/README.md](../client/macos/README.md)
- 用 oberth 操作当前 worktree：`.agents/skills/option-berth/SKILL.md`
