# agent 读取的 JSON 字段

[English](json.md) | 中文

下面这些字段是 agent 在 `status`、`up`、`restart` 的 `--json` 输出里做判断所需的最小集合。
字段的完整含义见各自命令的说明；本表同时是 `scripts/check_skill_contract.py` 的锚点来源，
新增或改名时同步更新 skill。

| 字段 | 命令 | 用途 |
|---|---|---|
| `last_exit.reason` | `status` | 当前失败原因；按它分支到日志、依赖或端口占用。 |
| `last_exit.port_hint` | `status` | 本次启动被告知要绑定的端口。 |
| `last_exit.port_holder` | `status` | 读取时仍占着失败端口的进程；缺省表示没观察到，不是端口空闲。 |
| `exits[].hint` | `status` | 历史退出记录里由日志文件是否存在决定的下一步。 |
| `services[].ready` | `up` / `restart` | CLI 的就绪注释；`false` 表示未确认，不是失败。 |
| `services[].port_holder` | `up` / `status --brief` | 声明端口冲突，或运行中服务的监听者不是它自己的运行时，与 `status` 同形状的占用者证据。 |
| `health_status` | `status` | 声明健康检查的结果与原因。 |
| `manifest_runtime_mismatch` | `status` | 运行实例是否来自旧清单。 |
| `worktree.identity` | `status` | `${worktree}` 的展开值，用于命名 per-worktree 数据。 |
| `observation.state` | `status` | 监听表是否可信；先读它再读 `ports`。 |
| `observation.source` | `status` | 非主采集器或 `runtime_registry` 回退；非空表示缺失的端口是未知，不是空闲。 |
| `observation.sources` | `status` | 合并表由哪些采集器按链顺序合成；只在合并读时出现，与 `source` 一样表示缺失的端口是未知，不是空闲。 |
| `observation.degraded_since` | `status` | 当前这次降级从何时开始；主采集器整表读出时为空。 |
| `observation.recovered_at` | `status` | 降级之后当前这段健康运行从何时开始；从未降级时为空。 |
| `ports[].source` | `status` | 产出这一行的采集器；只在合并读时出现，此时每一级各贡献了其中一部分行。 |
| `machine[].listening_known` | `status` | `listening` 是否是观察到的；false 表示这张表看不到该端口的所有者。 |
| `attn_path` | `status` | 当前异常的派生交接文件；没有异常时缺省。 |
| `attn` | `status` | 这份交接现在能否被评估：`enabled`、固定 `reason`、`assessment`（`assessed`、`pending` 或 `unavailable`）。 |
| `services[].starting` | `status` | 正在启动的瞬时事实；不是 run，也不等于 `running`。 |
| `starting.phase` | `status` | `waiting_dependency`、`prepare` 或 `spawning`。 |
| `starting.waiting_on` | `status` | 仍在等的依赖；观察失败时保留上次已知集合。 |
| `starting.elapsed_ms` | `status` | 本次启动已经过的毫秒数，读取时计算。 |
| `starting.ready_timeout` | `status` | 这次生效的就绪等待预算。 |
| `starting.last_prepare_ms` | `status` | 上一次 prepare 耗时；未知时为 null。 |
| `services[].stats.*` | `status` | 服务资源样本；字段缺省表示未知，不是 0。 |
| `worktree.resources.*` | `status` | 已有样本的 worktree 汇总；没有样本时缺省。 |
| `manifest_hash` | `status` | 进程启动时的清单指纹。 |
| `runtime_spec_hash` | `status` | 当前运行参数的指纹；与 `manifest_hash` 不一致即来自旧清单。 |
| `code_changed_since_start` | `status` | 服务是否在跑当前源码；缺省表示未知。 |
| `code_changed_files` | `status` | 发生变化且能精确比较的文件数。 |
| `code_hint` | `status` | 代码已变化时的确定性下一步。 |
| `code_head` | `status` | 启动时的 commit；不是 Git 检出或无提交时缺省。 |
| `services[].exit_reason` | `status --brief` | 精简视图里的失败原因。 |
| `services[].exit_code` | `status --brief` | 精简视图里的退出码。 |
