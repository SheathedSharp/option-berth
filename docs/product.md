# option-berth · 产品定义

本仓库唯一的产品定义。实现与支持边界见 [`architecture.md`](architecture.md)，
当前计划在 [`backlog.md`](backlog.md)。

## 本质需求

> 同时在几个 worktree 上让 agent 写代码时，拿到任何一份代码，人和 agent 都能用**一条命令**回答：
> **它该跑什么、是不是真在跑、没跑是为什么** —— 并且能直接把它起起来、停下去。

主线是**给 coding agent 的能力**，不是给界面的能力：agent 不该靠猜端口、翻进程表，或者回头问人
「你那个服务起来了吗」来判断运行环境，也不该把「为什么没起来」丢给用户去查。人读同一份答案。

客户端是这份答案的一种呈现，不是产品主线，也不是第二个事实来源（见「两个读者，一个接口」）。

终端与外部 coding-agent 会话围绕这份 worktree 的开发工作展开，不改变核心运行事实的来源。

## 实现原则

产品按自身需求自主设计，核心算法与底层实现以本文件的对象模型、写边界和 CLI 契约为准。
Sonar 等参考项目提供思路和工程经验，不决定本项目的架构，也不作为后续开发的骨架；
不为延续参考项目的设计而保留与本产品无关的对象或能力。

自研替换按可验证的模块边界推进，保持现有对外契约，通过行为、隔离性和性能验证，
而不是改名或增加包装层。历史来源如实保留在 [`engine/UPSTREAM.md`](../engine/UPSTREAM.md)，
迁移状态只在 [`backlog.md`](backlog.md) 跟踪。

## 对象模型

只有一条链，每一层都说清由谁产生：

```text
Worktree      路径 + git 身份（仓库、分支、脏文件数）          代码采
 ├─ Manifest  oberth.yaml                                      人写（agent 可起草，人采纳）
 │   └─ Service   一条服务：prepare / cmd / port / depends_on    人写
 │       └─ Run       一次运行：pid、起止时间、退出码、日志路径    后台进程记
 │           └─ Runtime facts 进程是否活着、监听端口、退出原因     代码采
 ├─ Workplace   这个 worktree 上的工作记录：workplace 与其中的 block  引擎记（前端可上报）
+ Undeclared  cwd 落在这个 worktree 里、清单却没写的监听          代码采
```

- **监听端口是 Run 的运行实况，不是独立实体。** 正常端口跟着服务显示；只有未声明监听、端口冲突或归属不明时才单独提示。产品不回答「这台机器上每个端口是什么」。
- **`machine:` 引用**是清单顶层的一节：这个项目依赖、但不归它起停的服务（数据库、中间件）。
  它只显示「在听 / 没听」，`up` / `down` 不碰它。
- 服务可以带 `prepare:` 前置命令。`up` 和 `restart` 会在启动长驻 `cmd:` 前完成它，适合
  Java jar、生成代码或其他必须从当前 checkout 重新产出的制品；前置命令失败时不会启动服务。
- 两个 worktree 同时跑同一个仓库时，每个 Run 归属于起它的那个 worktree，不互相归错。
- **工作记录挂在 worktree 上，不在服务链上。** 一个 worktree 可以有多个 workplace，每个按顺序
  保存其中的工作条目：引擎跑过的命令、交给 agent 的每一轮任务。它是「在这里做过什么、结果如何」
  的记录——agent 可以自己往里追加，也可以读回来接手别人（或自己上一轮）的工作。它不是新的运行
  真相：引擎记录的条目指向对应的 Run 和日志，没有观察到结束的条目保持 running，不冒充成功。
  产品不靠它判断服务状态，也不把它当成会话恢复。

## 核心闭环

agent 在一个 worktree 上做事，来回是这几条：

```text
oberth status --json     现在是什么状态：声明了什么、在跑什么、上次为什么失败、代码是哪份
oberth up --json         让它跑起来（depends_on 顺序，已在跑的跳过）
oberth logs --json       没起来或起错了，看它自己的输出和退出原因
oberth workplace show    之前在这里做过什么、结果如何
```

人读同一条命令的表格，闭环是同一组动作：

```text
cd <worktree>
oberth status    声明了什么、实际在跑什么、上次为什么失败、代码是哪份
oberth up        按清单起服务（depends_on 顺序，已在跑的跳过）
oberth restart   重启项目全部服务，或用 --only 重启点名的服务
oberth logs      从服务直接到日志、退出码和最后输出
oberth down      停掉这个项目的全部服务
```

没有清单时，`oberth init` 写一份 starter。需要 agent 帮忙时用
`oberth init draft`，由人用 `oberth init adopt` 采纳落盘。

## 命令分层

根帮助按**agent 想做的事**分组，不按内部模块分。新的产品能力只能落在已有层里：

| 层 | 命令 | 这一层回答什么 |
|---|---|---|
| 事实 | `status` · `logs` · `events` | 现在是什么状态、在变什么 |
| 操作 | `up` · `restart` · `down` · `start` · `kill` | 让它跑起来、停下来 |
| 代码与声明 | `git` · `init` | 代码上下文，以及这份 worktree 的清单 |
| 交接 | `workplace` · `agent` | 留下和取回工作记录，把会话交给外部 agent |
| 基础设施 | `daemon` · `serve` · `config` · `version` · `completion` · `doctor` | 运行环境、配置与自检 |

`events` 只读订阅明确关系的状态变化；`start` 只跑单个命令，`kill` 只处理明确点名的进程。
`agent list/plan/run` 是外部 coding-agent 的显式启动适配；不实现模型、工具循环或会话推理系统。

## 两个读者，一个接口

**接口只有 CLI。** agent 读 `--json`，人读同一条命令的表格；`status --json` 的顶层对象是
`worktree`。客户端是同一份状态的呈现，不长 CLI 没有的能力。

仓库里那份 skill（`.agents/skills/option-berth/`）是**产品面的一部分**：它是 agent 学会用这套
能力的入口，随仓库分发，装不装由用户决定。命令、`--json` 形状或错误码变了，它跟着变。

`oberth events` 是面向并行开发 agent 的只读 NDJSON 订阅入口。它支持按 worktree 根路径或 ID（沿已有
Repo 关系包含同仓库 worktree）、repository ID 和显式 workspace 关系过滤，首条记录是摘要快照，后续是 `state.changed` 摘要；
它只转发已有运行事实，不广播日志、命令、工作目录或环境变量，也不执行动作。

当代码采到确定异常时，`status --json` 可以同时发布一个位于 `BERTH_HOME` 的派生 attention 文件，
供使用 oberth 的开发 agent 判断是否需要把选择交还给用户。它只引用状态和日志证据，不改变事实，
不执行动作；Jev 若被外部 adapter 使用，也只补充升级建议，不进入引擎控制路径。
macOS 客户端可以为这个可选 adapter 保存本地 Jev 配置；配置只写入用户自己的
`~/.option-berth/jev.json`（隔离运行遵循 `BERTH_HOME`），不改变运行事实、清单或 daemon 接口。

## 写的边界

| 对象 | option-berth 能做什么 |
|---|---|
| 代码 / git | 只读：不提交、不暂存、不切分支 |
| `oberth.yaml` | 读、校验、起草；落盘只随人的采纳 |
| 项目服务 | 按清单起停、读日志 |
| 工作记录 | 记引擎自己启动的工作，以及前端上报的条目；增删 workplace 和其中的 block。不启动、不恢复、不替前端观察它自己的终端 |
| 机器级服务 | 只看在不在听；`up` / `down` 不动它们，点名 `kill` 是唯一例外 |
| 别人的配置 | 不写：不装 hooks、不注册 MCP、不改 agent 的配置 |

## 明确不做

- **整机端口雷达**：全部端口、系统端口、四档分类、机器服务注册表。`events` 必须带有
  worktree、repository 或已注册 workspace 的 filter；旧 RPC 客户端的无 filter 流只是兼容视图，
  不提供新的整机端口页面。
- **核心不做模型判断**：不把「为什么没起来」「这是谁的」交给 daemon 推断；事实只由代码采。
  可选的外部 attention adapter 可以根据这些事实决定何时把选择交还给用户，但不能改写状态或执行动作。
- **额外的接入层**：MCP 服务端、自动安装 skill、hooks。
- **客户端不是产品主线**：不把编排、模型判断、云端会话搬进客户端；它是 CLI 事实的呈现，
  不长 CLI 没有的能力，也不是第二个状态来源。
- 自动猜测启动命令并落盘；产品自身的 Git 写操作与提交流程。用户显式运行的 shell/外部 agent 按其自身权限修改代码，不等于引擎获得这些写权限。
- 远程主机、公网分享、端口映射。

## 成功标准

1. agent 在一个真实项目目录里跑一条 `oberth status --json`，就知道服务该不该跑、是否在跑、
   失败时该看哪一行，不需要问人，也不需要自己拼 `lsof` / `ps` / `tail`。
2. 起、停、重启由 agent 自己完成；服务行里的运行实况、进程和日志能复核它的结论。
3. 两个 worktree 同时运行时，每个服务对应哪份代码不会搞错。
4. 在这个 worktree 上做过什么、结果如何，能读回来——接手的人和 agent 不必从头猜。

## 外部会话边界

原生终端只承载用户选择的 shell 或已有 coding agent。消息作为精确参数或原生终端输入传递，
不拼成隐藏 shell 命令；登录、模型、审批、工具调用和对话恢复由外部 agent 自己负责。
不自动安装 agent、修改其配置、关闭沙箱或从其他 worktree 恢复最近会话。
支持结构化协议的 provider（`oberth agent stream`：当前构建含 Codex、Claude Code、OpenCode、Pi，见 [console.md](https://github.com/SheathedSharp/option-berth-macos/blob/main/docs/console.md) 与 `agent list --json` 的 `stream` 字段）在会话页呈现审批请求，由人决定允许或拒绝，
执行仍由 provider 负责；桥不传放宽权限的参数，也不自动批准。Shell 集成（zsh/bash/fish，可关）在提示符状态把键盘交给输入条。
