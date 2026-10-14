# 开发说明

给人读的日常开发指南。契约以 [product.md](product.md)、[cli.md](cli.md)、[architecture.md](architecture.md) 为准；
根目录 [AGENTS.md](../AGENTS.md) 与本文一致。给 coding agent 的装配说明见 [.agents/skills/develop/SKILL.md](../.agents/skills/develop/SKILL.md)。

## 仓库怎么拆

| 区域 | 路径 | 说明 |
|------|------|------|
| 引擎与 CLI | `engine/` | `oberth` 产品接口；Go 版本见 `engine/go.mod` |
| macOS 客户端 | `client/macos/` | Swift 包；改 UI 前读 [client/macos/AGENTS.md](../client/macos/AGENTS.md) |
| 开发自动化 | 仓库根 `mage` 目标 | 构建与验证入口；**不要**把 Mage 当成产品 CLI |
| 文档 | `docs/` | 契约、验证范围、发版见 [validation.md](validation.md)、[releasing.md](releasing.md) |

主链是 worktree、清单（manifest）、service、run 与 runtime facts。不要引入第二份「运行真相」。
清单由人确认；模型只能起草。`machine:` 依赖只读；产品侧 Git 操作为只读。

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

**macOS 客户端（在 `client/macos/` 下）：**

```sh
swift build
swift run TerminalChecks
# 隔离 HOME / BERTH_HOME / BERTH_CONSOLE_TEST=1
swift run ConsoleChecks
```

Console 原生测试可用 `BERTH_CONSOLE_TEST_QUIET=1` 减少抢焦点与截屏（见 ConsoleChecks 源码注释）。

**界面改动额外要求：**

- 冻结状态：`mage states`（改布局/样式后查看 PNG）
- 真实窗口几何与菜单：`mage window`
- 不要把离屏渲染当成已覆盖鼠标、输入法与长时间负载

测试与场景使用**可丢弃**的 HOME、`BERTH_HOME` 和临时项目；结束后回收自己起的进程。不要动用户的日常 daemon、账本或真实仓库写入（除非用户明确授权）。

## 实现时注意

- CLI / RPC / JSON / 清单保持兼容；接口变更要同步 schema、文档与回归。JSON stdout 不混入诊断。
- 取消、未知、观察失败不算成功；信号回执不是退出。锁、缓存、队列要有明确所有权与释放边界。
- 只报告实际跑过的命令与结果；旧提交绿灯或格式检查不能代替测试。未执行的验证在说明里写清楚。

## 提交与协作

- 提交说明：中文 `type(scope): 主题`，**一条提交一个主题**；写法见 [.agents/skills/git-commit/SKILL.md](../.agents/skills/git-commit/SKILL.md)（subject 避免内部里程碑代号与黑话）。
- 推送前 `git log` / `git status`，注意与他人并行提交。
- 发版与标签流程见 [releasing.md](releasing.md)。不要擅自改 CI 门槛、自动合并或审查结论。

## 相关专题

- 终端与会话 UX 契约：[console.md](console.md)
- macOS 使用与截图：[macos-guide.md](macos-guide.md)、[client/macos/README.md](../client/macos/README.md)
- 用 oberth 操作当前 worktree：`.agents/skills/option-berth/SKILL.md`
