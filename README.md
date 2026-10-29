<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth — 各自成泊" width="420">
  </picture>
</p>
<p align="center"><strong>每份代码，各自成泊。</strong><br>面向 Git worktree 的本地服务运行事实与生命周期引擎。</p>
<p align="center">简体中文 · <a href="README.en.md">English</a></p>

<p align="center"><a href="https://sheathedsharp.github.io/option-berth/">产品官网</a> · <a href="https://github.com/SheathedSharp/option-berth/releases/latest">下载最新引擎</a></p>

## 三个产品

option-berth 拆成三个独立产品，各自有仓库、版本与发布节奏：

| 产品 | 仓库 | 它做什么 |
| --- | --- | --- |
| **引擎**（本仓库） | [SheathedSharp/option-berth](https://github.com/SheathedSharp/option-berth) | `oberth`：声明、观察、起停一个 worktree 的服务，输出人和 agent 都能读的事实 |
| **客户端** | [SheathedSharp/option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) | macOS 原生界面：服务、只读 Git 与终端/agent 会话，呈现同一份事实 |
| **给 coding agent 的指引** | [SheathedSharp/option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) | 教会 agent 读同一份事实、把服务跑起来、留下并取回工作记录 |

三者只通过 CLI 与 `--json` 契约相连：客户端和 skill 都不自带第二份运行状态。
开发和依赖安装时，把三个仓库放在同一个目录下即可（见下面「一起用」）。

本仓库的 Release 只包含引擎 CLI（macOS/Linux/Windows 的 amd64 与 arm64 压缩包及
`SHA256SUMS`）。引擎版本从 `1.0.0` 起；拆分前的 `v0.2.0`–`v0.8.2` 属于合并产品，
已不在本仓库发布。客户端与 skill 各自在自己的仓库发版。

## 为什么需要 option-berth

同时开发几个分支，或让 coding agent 在多个 worktree 中工作时，难点不止是代码：
哪个服务属于哪份 checkout？它真的健康了吗？一个 worker 没有监听端口，是否还在运行？
停止当前项目，会不会影响另一个分支？

option-berth 把**服务声明、实际运行、日志、Git 上下文与工作记录**放到同一条工作流中。
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
| 接手别人（或上一轮）的工作 | `oberth workplace` 记录做过的命令与任务，结果如何都留在 worktree 上 |

**不是全机端口雷达，也不是另一个 AI IDE。** `machine:` 依赖只读；项目起停不会接管它们。
worktree 隔离指运行归属和生命周期边界，不是操作系统安全沙箱。
界面不在这个仓库：它是独立的 [macOS 客户端](https://github.com/SheathedSharp/option-berth-macos)。

## 安装

macOS / Linux：一条命令解析最新发布、按平台下载、核对 SHA256，并装到 `~/.local/bin`。

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows（PowerShell）：

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

固定版本用 `--version v1.0.0`（Windows 为 `-Version v1.0.0`），换安装目录用 `--dir` / `-Dir`，
不想修改 PATH 用 `--no-modify-path` / `-NoModifyPath`。脚本只下载并校验 Release 压缩包，
不启动服务、不写 Git；也可以先审阅 [install.sh](install.sh) / [install.ps1](install.ps1)，
或直接下载 [Releases](https://github.com/SheathedSharp/option-berth/releases) 压缩包。

卸载：

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh
```

默认保留 `~/.option-berth` 运行状态，加 `--purge-state` 才会删除。完整安装、升级、卸载与
手动包说明见 [安装与卸载](docs/install.md)。

完整服务场景面向 **macOS / Linux**。Windows 已有原生构建、冒烟及部分事实与持久化测试，
不能视为完整生命周期支持。

### 从源码构建

引擎使用 [Go 1.25 或后续兼容版本](engine/go.mod) 和 Mage。正式发布说明见 [发布流程](docs/releasing.md)。

```sh
git clone https://github.com/SheathedSharp/option-berth.git
cd option-berth
# macOS 的构建工具；其他系统使用对应的 Go / Mage 安装方式
brew install go mage
mage buildEngine
./bin/oberth version --json
```

构建不会安装软件或启动日常服务。确认需要安装后执行 `mage install`；它会写入安装目录并配置 PATH。
用 `NO_MODIFY_PATH=1 mage install` 可跳过 PATH 修改。

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
可选 [Jev adapter](docs/jev-adapter.md) 不进入引擎的运行事实与控制链路，缺少模型配置也能使用核心功能。

## 与 coding agent 协作

引擎提供显式的启动适配，不实现模型、工具循环或会话推理：

```sh
oberth agent list --json
oberth agent plan codex --worktree "$PWD" --json
oberth agent run codex --worktree "$PWD"
```

agent 怎么学会用这些命令，写在独立的
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) 仓库里。
装它是 agent 自己的事：本仓库的命令不会自动安装 hooks、skills 或 MCP 配置。

## 一起用

三个仓库互不复制代码，按同一份 CLI 契约协作。开发或试用时放在同级目录：

```sh
git clone https://github.com/SheathedSharp/option-berth.git
git clone https://github.com/SheathedSharp/option-berth-macos.git
git clone https://github.com/SheathedSharp/option-berth-skill.git
```

客户端用 `BERTH_BIN` 指向任意一份 `oberth`；skill 用软链装进你所用 agent 的技能目录。

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
第三方代码及素材保留各自许可和版权，见 [第三方声明](THIRD_PARTY_NOTICES.md)。
外部 coding agent 的名称仅说明兼容性，不表示其开发者为本项目背书。
