<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth — 各自成泊" width="420">
  </picture>
</p>
<p align="center"><strong>每份代码，各自成泊。</strong><br>面向 Git worktree 的本地开发运行工作台。</p>
<p align="center">简体中文 · <a href="README.en.md">English</a></p>

## 为什么需要 option-berth

同时开发几个分支，或让 coding agent 在多个 worktree 中工作时，难点不止是代码：
哪个服务属于哪份 checkout？它真的健康了吗？一个 worker 没有监听端口，是否还在运行？
停止当前项目，会不会影响另一个分支？

option-berth 把**服务声明、实际运行、日志、Git 上下文与终端会话**放到同一条工作流中。
你确认项目应该运行什么；`oberth` 负责启动、观察和有边界地停止。
人和外部 coding agent 读取相同的事实，不需要各自猜测系统状态。

## 它带来的改变

| 你要做的事 | option-berth 如何帮助 |
| --- | --- |
| 同时运行多个 worktree | 自动端口与独立运行归属，减少分支之间的干扰 |
| 确认服务可用 | 区分进程、监听和健康；`up --wait` 等待实际就绪 |
| 排查失败 | 从服务进入日志、退出结果与诊断，而不是重新拼接系统命令 |
| 使用无端口 worker | 通过运行记录观察，不把“没有端口”当成“没有运行” |
| 与 coding agent 协作 | 相同 CLI 提供结构化 JSON；不内置另一套模型或 agent 推理系统 |
| 使用原生桌面界面 | macOS 客户端呈现同一份服务事实与只读 Git 信息 |

**不是全机端口雷达，也不是另一个 AI IDE。** `machine:` 依赖只读；项目起停不会接管它们。
worktree 隔离指运行归属和生命周期边界，不是操作系统安全沙箱。

## 安装与构建

版本发布包见 [GitHub Releases](https://github.com/SheathedSharp/option-berth/releases)；也可以从源码构建。引擎使用 [Go 1.25 或后续兼容版本](engine/go.mod) 和 Mage；
macOS 客户端需要系统开发工具。正式发布说明见 [发布流程](docs/releasing.md)。

```sh
git clone https://github.com/SheathedSharp/option-berth.git
cd option-berth
# macOS 的构建工具；其他系统使用对应的 Go / Mage 安装方式
brew install go mage
mage buildEngine
./bin/oberth version --json
```

构建不会安装软件或启动日常服务。确认需要安装后执行 `mage install`；它会写入安装目录、
配置 PATH，并在 macOS 安装桌面客户端。用 `NO_MODIFY_PATH=1 mage install` 可跳过 PATH 修改。

引擎的完整服务场景面向 **macOS / Linux**。Windows 已有原生构建、冒烟及部分事实与持久化测试，
不能视为完整生命周期支持。桌面客户端目前是 **macOS 14+ / Apple Silicon**。

## 第一次跑通

仓库自带 [Hello Worktree](examples/hello-worktree/README.md)：一个只监听本机的 API 和一个
无端口 worker。只需 Python 3 标准库，不需要模型账号、容器或 pip 依赖。

```sh
# 已安装 oberth 后，从仓库根目录开始
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
oberth down --json
```

接入自己的项目时，先运行 `oberth init`，再审阅并编辑生成的 `oberth.yaml`。
也可以让已有 agent 起草，但 **draft 不等于授权**：只有你确认并执行 `init adopt` 后才落盘。
完整字段、自动端口、依赖和健康检查见 [CLI 使用说明](docs/cli.md)。

## 每天使用的几条命令

```sh
oberth status --json            # 当前 worktree 的声明与运行事实
oberth doctor                  # 安装、清单与后台服务诊断
oberth up --wait                # 启动并等待就绪
oberth restart --only api       # 只重启点名服务
oberth logs api --once          # 读取服务日志
oberth down                    # 停止当前项目并安全处理端口预留
```

`oberth git` 提供只读 Git 上下文；需要跨项目观察时，使用带明确 worktree 筛选的 `oberth events`。
命令不会自动安装其他工具的 hooks、skills 或 MCP 配置。可选 [Jev adapter](docs/jev-adapter.md)
不进入引擎的运行事实与控制链路，缺少模型配置也能使用核心功能。

## 终端与 coding agent

macOS 工作区中的 **Terminal / Agent session** 区分 shell 与外部 agent 会话，
共用当前 worktree 的服务事实和只读 Git 上下文。会话固定归属于创建时的 worktree，
切换项目不会把原会话迁移到另一份代码中。

自行安装并登录 **OpenCode、Codex、Claude Code、DeepSeek Harness 或 Pi** 后，
在 agent 消息框按 **⌘Enter** 发送首条消息并创建新会话；**⇧⌘Enter** 进入当前活动会话的原生界面。
普通 Enter 换行，后续对话和权限确认交给 agent 自己。再次发送会建立新会话，不自动跨 worktree 恢复历史对话。

```sh
oberth agent list --json
oberth agent plan codex --worktree "$PWD" --json
oberth agent run codex --worktree "$PWD"
```

**⇧⌘P** 打开统一命令面板；**⌘1/2/3** 切换服务、只读 Git 与终端。工作区支持同一 worktree 的
递归分屏与独立终端窗口；顶部可访问会话、恢复和检查更新。Agent 的“续接…”要求选择原始会话
文件并核对 worktree，不使用最近对话。可明确允许保存布局元数据，但草稿、终端输出和旧进程
不会自动恢复。命令块集成对新建 zsh 显式启用，不修改用户 shell 配置。

DeepSeek 的消息入口使用 headless，原生入口要求已有 tui profile；两种模式不是同一对话的无缝切换。
option-berth 不实现另一套 agent 推理系统、不保存模型凭证、不自动安装插件，也不添加绕过审批或沙箱的参数。
完整交互与验证边界见 [客户端说明](client/macos/README.md)。

发布包内的 macOS App 带有匹配版本的引擎，但目前只有 **ad-hoc 签名，没有 Developer ID 或 Apple 公证**。
下载后核对 SHA256SUMS；不要把它当成已公证安装包。使用终端不替代 `oberth down`，关闭会话不会自动停止项目服务。

## 版本与贡献

版本为 **X1.X2.X3**：X1 表示协议更新，X2 表示功能更新，X3 表示缺陷修复；
增加较高位时清零较低位。发布脚本与验证规则以 [发布流程](docs/releasing.md) 为准。

贡献请提交一个主题明确的 PR，说明问题、实现、TODO、测试及兼容性；
不要提交私人配置、凭证、运行日志或真实项目截图。开发约定见 [AGENTS.md](AGENTS.md)。

```sh
mage test
mage vet
python3 -m unittest discover -s scripts -p '*_test.py'
```

项目核心围绕 worktree、manifest、service、run 和运行事实自主设计。
[产品边界](docs/product.md) 与 [架构说明](docs/architecture.md) 记录当前契约，
历史来源在 [UPSTREAM](engine/UPSTREAM.md) 中如实保留。

## 许可证

由 **SheathedSharp** 与项目贡献者维护。项目代码适用 [MIT License](LICENSE)；
第三方代码、字体及素材保留各自许可和版权，见 [第三方声明](THIRD_PARTY_NOTICES.md)。
外部 coding agent 的名称仅说明兼容性，不表示其开发者为本项目背书。
