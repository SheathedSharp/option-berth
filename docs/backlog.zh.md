# 当前能力与待办

[English](backlog.md) | 中文

以 [产品定义](product.zh.md) 和 [支持边界](agent/architecture.md) 为准。主线是给 coding agent 的能力。
呈现层与 skill 的进度不在这个仓库：macOS 客户端在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)，给 agent 的 skill 在
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)。

## 后续能力

| 优先级 | 能力 | 当前缺口与完成条件 |
| --- | --- | --- |
| 高 | agent 侧的能力面是否够用 | 主线是给 coding agent 的能力，所以「agent 拿到这份代码能不能自己跑起来、自己判断为什么没跑起来」优先于任何界面。**2026-10-28 已跑过一轮**：真实 Claude Code 在陌生 worktree 上只靠 CLI 与 skill 完成了「起服务、失败、看日志、修、再起」，核心闭环 6 步走通（输入与结果见 [validation.md](agent/validation.md)「核心验收」）。那一轮卡住的两处已修：失败提示不再指向不存在的日志，未监听的 `machine:` 依赖不再发布交接（契约见 [cli.md](cli.zh.md)）。**2026-10-30 起**这轮验收沉淀为固定场景库（生成物缺失、端口被占、依赖未就绪、清单与进程不一致、同仓库双 worktree），见 [agent-eval.md](agent/agent-eval.md)。**2026-10-31 起**增加 ten-service 大项目档（慢依赖、生成物、迁移、无端口 worker、三 worktree）与 `oberth` / `native` 对照臂，报告输出完成率、步数、耗时、输出字节、进程探测、误停和串 worktree 的差值；夹具与公开示例 [stack-workshop](../examples/stack-workshop/README.md) 共用。仍缺：在一台能正常观察监听的普通终端或 CI 上，用真实 Codex / OpenCode / Pi 跑出可比的发布矩阵，并补一档真实 JVM / Node 构建 / compose Postgres+Redis 的手动夹具；本机 agent 沙箱内 preflight 会拒绝出表。 |
| 高 | 发布结论的证据面 | 第一行只测了一半。最近一次真实矩阵（2026-11-01，CI run 41280627281，`main` 在 `81bf405`）只覆盖**一个** provider（Claude Code）、八个 `large` 场景、两个臂、**repeat 2**；每行都可比，但表里是两次运行的中位数、没有区间，`core` 一档、四个 provider 的说法和 `n ≥ 5` 都没有数据。2026-10-28 的核心验收是一次手工单轮。完成条件：`4 个 provider ×（core + large）× n ≥ 5`，报告给区间，再加上面那档 JVM / Node / compose 手动夹具；在那之前，任何能力结论都不高于已经存在的那几行。两个前置属于未完成的工作而不是本行：场景档与臂的泛化（`cold` 档、`oberth-noskill` 臂、`--repeat` 接受区间）尚不存在，`docs/agent/agent-eval.md` 也只写了 `oberth` / `native` 两臂。 |
| 中 | 无清单的冷启动基线 | 场景库每一行都从手写的 `oberth.yaml` 开始；它前一步——一个完全没有清单的陌生仓库，经 draft → adopt → `up --wait` 到服务健康——没有测量过，因此起草环节的任何改进都没有基线可比。完成条件：三种仓库形状（compose + Makefile、Node monorepo、Maven 多模块）的夹具套件，进程只用 Python 标准库，报告 draft → adopt → `up --wait` 健康的成功率、步数、耗时、首次失败后清单被改的次数、漏声明与多声明的服务数，以及「除 `oberth.yaml` 外已跟踪文件不变」这条安全项。`init draft` 会再起一个 agent 进程，每次冷启动多一次模型调用，预算要写进场景。在那份基线出来之前不要改起草（`internal/draft/brief.go`）。 |
| 中 | 编排工具配方的端到端验证 | [integrations.md](integrations.zh.md) 的 Conductor / Cursor / Claude Code / Codex 配置来自各工具官方文档，尚未在真实工具里跑通创建、启动和归档/清理。完成条件：在隔离 `HOME` 与可丢弃仓库中各跑一次 setup、`status --json` 和 cleanup（含 checkout 已删除的路径），记录实际输入与结果。 |

## 上表未收口期间的约束

这些是对「怎么算做完」的限制，不是能力。上面几行都是关于证据的结论，而新增一块面只会让证据
更难，不会更容易。

- **核心证明之前不新增命令。** 产品能力只能落在 [product.md](product.zh.md)「命令分层」已有的层里；
  表达不进现有层的能力，等上面几行，而不是加一层。
- **macOS 27 的监听限制已修，但 macOS 27 本身在 CI 里仍未验证。** issue #156（Go 子进程读到空的
  `netstat` 表）已关闭：采集器回退到 `lsof`（契约见 [cli.md](cli.zh.md)「监听观察」，证据见
  [validation.md](agent/validation.md)）。托管 runner 提供的是 `macos-14`、`macos-15` 和
  `macos-26` 镜像，没有 macOS 27；因此这次修复只在本机被证明，CI 的 macOS 行不覆盖它。本机复验
  是写下来的手工步骤，不是 CI 结果。

## 支持范围内的修复

支持范围内若复现误停、归属错误或账本损坏，必须修复，不能以「后续迭代」掩盖。
验证命令与实际覆盖见 [validation.md](agent/validation.md)；客户端的行为见
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
