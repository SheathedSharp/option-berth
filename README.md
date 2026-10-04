# option-berth

**本地 Git worktree 的服务运行泊位。** 命令是 `oberth`：在同一套 CLI 契约中查看服务声明、
运行事实和日志，并执行明确授权的启动、重启与停止。人和 coding agent 使用相同的事实来源。

开发预览版，当前重点支持 macOS 与 Linux 引擎。使用前查看 [验证范围](docs/validation.md)
和 [已知边界](docs/architecture.md)。许可采用 [MIT](LICENSE)，不提供稳定性担保。

## 安装

Go 版本以 [engine/go.mod](engine/go.mod) 为准，当前要求 Go 1.25.0 或兼容的后续版本。
开发构建使用 Mage；macOS 客户端还需要 Xcode Command Line Tools。

```bash
git clone https://github.com/SheathedSharp/option-berth.git
cd option-berth
# macOS：安装构建工具；其他平台使用相应的 Mage 安装方式
brew install mage
mage install
oberth doctor
```

`mage install` 会安装 CLI、可选 Jev adapter，
并在 macOS 安装客户端；它还会配置 PATH。不要把安装步骤当成只读操作。
不希望改动 shell 配置时使用 `NO_MODIFY_PATH=1`，并自行将安装目录加入 PATH。
安装后用 `oberth version --json` 核对实际执行的构建，避免读取 PATH 上的旧版本。

构建目标 `build / engine / jev / client` 只生成产物；`run / runApp / runDaemon / stop`
明确起停，`stop` 不隐式构建。`shot / states / window` 只使用已经构建的客户端；缺少客户端时
明确提示先构建，不写安装目录。`mage shot -scope=services:example-project` 可限定截图范围。

包含空格、反斜杠或空参数的开发调用使用精确 JSON argv，例如
`mage runCLI '["doctor","--project","项目 with spaces","--json"]'`。
简单调用 `mage runCLI "version --json"` 保持兼容；不会执行 shell 展开，歧义引号会在构建前报错。

三个模块也可以分别构建，不依赖 Mage：

```bash
(cd engine && go build -o ../bin/oberth .)
(cd engine && go build -o ../bin/jev-attention ./cmd/jev-attention)
(cd client/macos && ./build.sh)
```

只构建引擎可运行 `mage buildEngine`；客户端的构建和平台要求见
[client/macos/README.md](client/macos/README.md)。

## 第一次接入项目

在目标 worktree 中创建并确认 `oberth.yaml`。下面仅是运行本地 Python HTTP 服务的示例，
不代表任何真实部署，也不应替代项目维护者对启动命令的确认。

```yaml
name: example-project
services:
  - name: api
    cmd: python3 -m http.server 18080 --bind 127.0.0.1
    port: 18080
```

在该清单所在目录执行：

```bash
oberth status --json
oberth up --wait --json
oberth logs api --once --json
oberth restart --only api --json
oberth down --json
```

`prepare` 可用于启动前的准备工作，成功后才启动服务；`machine:` 依赖仅显示监听事实，
不会被 `up` 或 `down` 起停。完整字段和命令语义见 [CLI 契约](docs/cli.md)。

agent 可以通过 `oberth init draft codex --progress --json` 起草清单，但草稿不是授权。
使用 `init adopt` 前必须检查并确认草稿路径、启动命令和变更内容；具体用法以 CLI 契约为准。

## 给 Codex 安装 skill

skill 的唯一源是 [.agents/skills/option-berth/](.agents/skills/option-berth/)。
需要安装整个目录，包括 `references/` 和 `agents/`，不能只下载 `SKILL.md`。
从仓库根目录显式执行以下命令，会创建或更新全局安装副本：

```bash
mkdir -p "$HOME/.agents/skills/option-berth"
cp -R .agents/skills/option-berth/. "$HOME/.agents/skills/option-berth/"
```

已有本地定制时先自行保留差异。仅供一个项目使用时，将完整目录安装到该项目的
`.agents/skills/option-berth/`。安装副本不应另行维护一套产品契约。

日常任务从目标 worktree 的 `oberth status --json` 开始，再按需要读取日志或执行用户授权
的起停操作；操作后重新读状态。产品 CLI 的 Git 能力只读，不替用户提交、暂存或切换分支。

## 并行 worktree 联调

`status` 回答当前 worktree。需要跨项目观察时，用任务中明确给出的路径建立只读订阅：

```bash
oberth events --worktree /path/to/project-a --worktree /path/to/project-b
```

路径是占位示例，必须替换为任务实际授权的 worktree。事件订阅不能根据进程名、端口或
分支名称猜测项目关系，也不能自动执行控制操作。收到重新同步要求后先重新取快照；
任务结束时回收自己建立的订阅。筛选、workspace 和恢复语义见 skill 与 CLI 文档。

## 启用 Jev 增强

Jev 是可选的 attention adapter：只读取代码发布的异常，提供固定选项的升级建议，
不采集运行事实、不生成任意 shell 命令，也不能替用户授权执行。

macOS 客户端可在设置中配置 Jev；凭证保存在用户本地，不应进入仓库、项目清单或公开日志。
完整配置、任务级订阅、脱敏范围及人工反馈方式见 [docs/jev-adapter.md](docs/jev-adapter.md)。
Jev 不可用时仍可直接依据 `oberth` 的事实和日志工作。

## 运行原理与边界

```text
worktree → manifest → service → run → runtime facts
                 ↑                         ↓
             用户确认清单             CLI / 客户端 / agent
```

声明说明应该运行什么，运行事实说明实际发生什么；不能把声明、旧缓存或模型建议当成
已观察到的成功。服务身份、采集新鲜度、并发发布和持久化边界由引擎负责。
客户端与外挂复用这些事实，不维护第二套运行真相。

核心按本项目的服务事实和生命周期需求设计。Sonar 是参考及历史代码来源，不决定后续
骨架；来源及原版权保留在 [engine/UPSTREAM.md](engine/UPSTREAM.md)。

## 开发与验证

```bash
mage buildEngine    # 构建 CLI / daemon
mage buildJev       # 构建可选 adapter
mage buildClient    # 构建 macOS 客户端
mage test           # Go 测试
mage vet            # Go 静态检查
```

端到端运行使用独立 `BERTH_HOME` 和自己创建的可丢弃项目；不要触碰用户的真实账本或服务。
运行截图、日志、错误和性能工件都可能包含本机路径、参数或项目内容，公开前必须审查。

`VERSION` 是版本号唯一来源。`mage releasePatch`、`releaseMinor`、`releaseMajor` 会执行
发布前检查，并产生版本提交和本地标签，不应作为普通构建步骤自动运行。

局部基准入口为 `scripts/benchmark-core.py`，真实链路入口为 `scripts/verify-e2e.py`。
重复 Go benchmark 的聚合 min/median/max 不是单次请求延迟 p95；局部加速不能替代完整
起停、重连、资源保留和跨平台结论。测试延期必须明确记录，不能把静态检查写成测试通过。

## 文档

| 文档 | 用途 |
|---|---|
| [产品定义](docs/product.md) | 目标、对象模型与明确不做的事情 |
| [CLI 契约](docs/cli.md) | 命令、JSON 与使用边界 |
| [唯一待办](docs/backlog.md) | 当前缺口和验收状态 |
| [当前架构](docs/architecture.md) | 生产职责、支持范围与安全边界 |
| [验证记录](docs/validation.md) | 实际平台、输入和验证结果 |
| [Jev adapter](docs/jev-adapter.md) | 可选增强、凭证、订阅与质量反馈 |
| [开发约定](AGENTS.md) | 构建、验证和贡献规则 |
| [发布与历史去敏](docs/releasing.md) | 发布前检查顺序和操作边界 |

## 参考来源

历史来源与迁移边界见 [engine/UPSTREAM.md](engine/UPSTREAM.md)。
保留来源不意味着继续以参考项目为骨架；文档清理也不能抹掉已有代码的许可义务。

## 许可证

项目代码和文档采用 [MIT License](LICENSE)，允许在遵守版权与许可保留条件下使用、修改
和分发，不提供担保。原引擎 MIT 声明保留于 [engine/LICENSE](engine/LICENSE)。
Monaspace 字体另按 OFL 1.1；第三方标志与依赖遵循各自条款，详见
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。不要将整个素材目录统一标记为 MIT。

## 第一次体验

[Hello Worktree 示例](examples/hello-worktree/README.md)：用 Python 标准库运行一个本地 API 和无端口 worker，
看见健康检查、自动端口、跨 worktree 隔离与完整停止流程。无需模型账号、容器或额外 Python 依赖。
