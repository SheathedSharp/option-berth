# 工作记录

[English](workplace.md) | 中文

交接层的一份**工作记录**。一个 worktree 下可以有多个 workplace，每个按时间顺序保存其中的
工作条目（block）：引擎跑过的命令，和交给 agent 的每一轮任务。它回答的是「在这里做过什么、
结果如何」——agent 可以读回来接手别人（或自己上一轮）的工作，也可以把引擎看不见的尝试自己
写进去。命令只读写这份记录，不判断、不启动、不恢复。

它挂在 worktree 上，不在服务链上，不参与服务事实的归约，也不被 `status` 当成运行真相。
命令与字段的契约以本文为准；其余 CLI 契约见 [CLI 使用说明](cli.zh.md)。

## 命令

~~~text
oberth workplace list [--worktree PATH] [--json]
oberth workplace new [name] [--worktree PATH] [--json]
oberth workplace use <name|id> [--worktree PATH] [--json]
oberth workplace show [name|id] [--worktree PATH] [--limit N] [--json]
oberth workplace delete <name|id> [--worktree PATH] [--json]
oberth workplace record --kind shell|agent [flags] [--worktree PATH] [--json]
~~~

`--worktree` 缺省是当前目录；解析成 checkout 的根路径并归一符号链接，同一个 checkout 的
不同写法是同一份记录。既不是 checkout 也没有清单时返回 not_found。

- `list` 按创建顺序列出，`●` 标出当前 workplace；没有 workplace 不是错误，是空列表。
- `new` 建一个并设为当前；同名返回 conflict。
- `use` 切换当前 workplace —— 之后 `oberth start` 和 `oberth agent stream` 记录到它。
- `show` 不带参数读当前 workplace；`--limit N` 只取最新 N 条，`truncated` 标明记录还有更多。
- `delete` 删除一个 workplace 及其全部 block；删掉当前那个时，剩余里最新的接任，没有剩余就
  没有当前 workplace。
- 选择器依次按 精确名字 → 完整 id → id 前缀 → 名字前缀 匹配；同一层命中多个返回 ambiguous，
  不替调用者挑一个。

## block 的 JSON 字段

~~~json
{"id":"…","workplace":"…","seq":1,"kind":"shell","source":"engine",
 "input":"npm test","status":"completed","run_id":"…","pid":1234,
 "log_path":"…","exit_code":0,"started_at":"…","ended_at":"…"}
~~~

- `kind`：`shell`（引擎跑的命令）或 `agent`（交给 agent 的一轮任务）。
- `source`：`engine`（引擎自己观察到的运行，run_id / pid / log_path / 退出码都是它采的）或
  `reported`（引擎没启动、由上报方写进来的条目）。只有 `reported` 的 block 带 `output` 文本；
  引擎的 block 指向 `log_path`，日志仍是输出的唯一一份。
- `status`：`running` · `completed` · `failed` · `interrupted`。没有观察到结束的条目保持
  running，不会被当成成功；被 `oberth` 或用户停下的运行是 interrupted，不看它留下的信号退出码。
- `seq` 是条目在所属 workplace 里的顺序，每个 workplace 最多保留 500 条，超出时丢弃最旧的。

## 记录怎么产生

记录不需要手动操作：

- `oberth start` 把这次运行写进当前 workplace；非附加（`-d`）的运行由 daemon 在记录它的退出时收尾。
- `oberth agent stream` 每一轮写一条：`turn.started` 开，`turn.completed` 按 provider 给的
  outcome 收尾（completed / interrupted / failed）。
- 引擎看不见的工作用 `oberth workplace record` 写进来：agent 自己的尝试（「我改了 X，测试仍然
  失败」）、前端自己的交互式 Shell、原生 agent。同一个 `--id` 再报一次是更新同一条而不是新增，
  所以一次工作的开始与结束可以任意顺序到达；重复的 `running` 不会覆盖已经记下的结果。

## 边界

`show` 读回的是做过什么，不是把当时的进程叫回来。daemon 不在时读命令仍从数据库直接读；
`new` / `use` / `delete` / `record` 需要一个 daemon（按既有规则自启动），而 `oberth start` 与
`oberth agent stream` 的记录不会为你去起一个 daemon —— 没有 daemon 就没有记录，在 stderr 说明一次。
清单里的服务起停（`up` / `down`）不写工作记录：那是声明的生命周期，不是临时的工作条目。

实现在 `internal/workplace`（规则、选择器与 RPC）、`internal/store/workplaces.go`（行与迁移 009）
和 `cmd/workplace*`（CLI 与记录接线）。写入只有一个所有者：daemon 是账本的唯一写者；
CLI 的读路径在 daemon 不在时只读打开同一份数据库，写命令走 RPC。
