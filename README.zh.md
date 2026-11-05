<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg">
    <img src="brand/option-berth-lockup-light.svg" alt="option-berth — 各自成泊" width="420">
  </picture>
</p>
<p align="center"><strong>服务起没起来，不用再猜。</strong><br>Git worktree 的运行事实层：一份 checkout 该跑什么、是不是真在跑、没跑为什么。</p>
<p align="center"><a href="README.md">English</a> · 简体中文</p>

<p align="center"><a href="https://oberth.zayju.de/">产品官网</a> · <a href="https://oberth.zayju.de/guide/">指南</a> · <a href="docs/product.zh.md">产品定义</a> · <a href="docs/cli.zh.md">CLI</a> · <a href="https://github.com/SheathedSharp/option-berth/releases/latest">最新版本</a> · <a href="https://oberth.zayju.de/llms.txt">llms.txt</a></p>

## 为什么需要 option-berth

worktree 解决了并行写代码，没解决并行跑服务。同时开着四份 checkout、每份里还有 agent 在写代码时，
任何一个「服务还在不在」的问题都只能靠猜：这个服务属于哪份 checkout？它真的健康吗？没有端口的
worker 是否还活着？停掉当前项目会不会影响另一个分支？答案散在进程表、端口列表、几个日志文件和
某个人的记忆里。

`oberth` 把它们放进同一条链：**worktree → manifest → service → run → 运行事实**。你确认一份
checkout 应该跑什么；引擎在边界内启动、观察和停止；人和 coding agent 读同一份事实，而不是各自
重建系统状态。

## 它带来什么

| 你要做的事 | option-berth 如何帮助 |
| --- | --- |
| 同时运行多个 worktree | 独立的运行归属与自动端口，让分支之间不互相干扰 |
| 确认服务可用 | 区分进程、监听与健康；`up --wait` 等待实际就绪 |
| 排查失败 | 直接读到服务的日志、退出结果与诊断，而不是重新拼一条系统命令 |
| 观察无端口 worker | 用运行记录回答，不把「没有端口」当成「没有运行」 |
| 让脚本或 agent 读取事实 | 同一个 CLI 输出结构化 JSON；内部没有第二套模型或 agent 推理系统 |
| 把工作交给下一轮 | `oberth workplace` 留下在这里跑过的命令与任务，以及它们如何结束 |

停止不会越过另一份 checkout。checkout 目录被删除后，`worktrees --gone` 列出它留下的身份，
`down --worktree` 仍能停止并交还 `port: auto` 预留；数据是否删除由人决定。

**不是全机端口雷达，不是操作系统沙箱，也不是另一个 AI IDE。** `machine:` 依赖只读，项目起停
不会接管它们；worktree 隔离指的是运行归属与生命周期边界。

## 第一次跑通

仓库自带 [Hello Worktree](examples/hello-worktree/README.md)：一个只监听本机的 API 和一个无端口
worker，只需 Python 3 标准库，不需要模型账号、容器或 pip 依赖。
[Worktree Data](examples/worktree-data/README.md) 用 `${worktree}` 给每份 worktree 各自的数据库；
[Stack Workshop](examples/stack-workshop/README.md) 是一份十服务整栈：慢启动的 auth、要构建的前端、
要迁移的数据库，以及只读的 `machine:` 依赖。

```sh
# 已安装 oberth 后，从仓库根目录开始
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
oberth down --json
```

接入自己的项目时，先运行 `oberth init`，再审阅并编辑生成的 `oberth.yaml`。也可以让已有 agent
起草，但 **draft 不等于授权**：只有你执行 `init adopt` 后才落盘。完整字段、自动端口、依赖与
健康检查见 [CLI 使用说明](docs/cli.zh.md)。

## 安装

macOS / Linux：一条命令解析最新发布、按平台下载、核对 SHA256，并装到 `~/.local/bin`。

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows（PowerShell）：

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

Homebrew、`go install`、Linux `.deb`/`.rpm`、固定版本、卸载、升级与各平台边界见
[安装与卸载](docs/install.zh.md)。安装脚本只下载并校验发布包：不启动服务，也不写 Git。

直接运行 `oberth` 会显示本机版本；有更新时给出本平台的升级命令。也可以随时运行
`oberth version --check` 主动检查，它只读发布信息，不下载、不安装。

完整服务生命周期在 **macOS 与 Linux** 上支持。Windows 有原生构建、冒烟检查及部分事实与持久化
测试，不能视为完整生命周期支持。

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

## 与 coding agent 和编排工具协作

引擎提供显式的启动适配，不实现模型、工具循环或会话推理。agent 怎么学会用这些命令，写在独立的
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill) 仓库里：

```sh
npx skills add SheathedSharp/option-berth-skill -g
oberth agent plan codex --worktree "$PWD" --json
```

Conductor、Cursor、Claude Code、Codex 等工具负责创建工作区；在它们自己的 setup / run / cleanup
脚本或外层 wrapper 里调用 `oberth`：

```sh
oberth up --wait                           # 新工作区的 setup
oberth status --json                       # 脚本或 agent 读取运行事实
oberth down --worktree /path/to/worktree   # 清理；checkout 已删除也能按身份停止
```

产品不安装 hooks、不注册 MCP、不改这些工具的配置。各工具的具体写法、版本前提，以及常见框架需要
的端口参数（例如 Vite 要写 `--port ${port} --strictPort`），见
[接入编排工具与开发服务器](docs/integrations.zh.md)。

macOS 原生界面是独立产品
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)：它读取本 CLI 的事实，
不保存第二份运行状态。两个产品与 skill 只通过 CLI 及其 `--json` 契约相连。

## 文档

| 你需要什么 | 去哪里 |
| --- | --- |
| 产品定义、明确不做什么 | [docs/product.zh.md](docs/product.zh.md) |
| CLI、JSON 字段、清单、错误码 | [docs/cli.zh.md](docs/cli.zh.md) |
| 安装、升级、卸载 | [docs/install.zh.md](docs/install.zh.md) |
| 编排工具与开发服务器 | [docs/integrations.zh.md](docs/integrations.zh.md) |
| 工作记录与交接 | [docs/workplace.zh.md](docs/workplace.zh.md) |
| 实现边界 | [docs/agent/architecture.md](docs/agent/architecture.md) |
| 每项检查证明什么 | [docs/agent/validation.md](docs/agent/validation.md) |
| 构建、测试与发布本仓库 | [docs/agent/develop.md](docs/agent/develop.md)、[docs/agent/releasing.md](docs/agent/releasing.md) |

## 贡献

一个主题一个 PR，说明问题、实现、TODO、测试与兼容性。不要提交私人配置、凭证、运行日志或真实项目
截图。动手前请先读本仓库的约定 [AGENTS.md](AGENTS.md)。

```sh
mage test
mage vet
python3 -m unittest discover -s scripts -p '*_test.py'
```

## 许可证

由 **SheathedSharp** 与项目贡献者维护。项目代码适用 [MIT License](LICENSE)；第三方代码及素材
保留各自许可和版权，见 [第三方声明](THIRD_PARTY_NOTICES.md)。外部 coding agent 的名称仅说明
兼容性，不表示其开发者为本项目背书。
