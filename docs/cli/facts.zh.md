# 运行事实

[English](facts.md) | 中文

回答「现在什么是真的」：声明、运行、监听、健康与退出历史。本页的命令都只读，不启动也不停止服务；agent 据此判断的字段另见 [agent 读取的 JSON 字段](json.zh.md)。

## oberth status [path] [--json] [--no-mark] [--only services] [--brief]

读取一个 worktree 的完整现状：清单声明的服务、运行记录、监听实况、最近退出原因和 git
摘要。path 省略时使用当前目录。命令只在自己的状态文件里留下「上次看」标记，
第二次运行会在 changed 中列出事实变化；--no-mark 只读不移动标记。

`--only api,worker` 只保留这些服务及其 `depends_on` 的传递闭包；`ports[]`、`exits[]` 和
`changed` 里的服务/端口变化一起收窄，`machine:`、`runs` 与 git 摘要保持不变——`runs` 列出的是
清单没有声明的运行，服务选择无法代表它们。名称必须已经声明：
未知名称返回 `not_found` 并列出可选服务，空名称按参数错误处理。过滤发生在完整快照和
「上次看」标记写入之后，因此不会把部分状态写成下一次比较的基线。代码比较也只对选中的
服务做，不会为范围外的服务重读源码。

`--brief` 只保留运行时事实：服务与依赖的状态、声明/实际端口、健康结果、当前退出原因和
下一步、代码变更（`code_changed_since_start`、`code_changed_files`、`code_hint`）、可执行文件
事实（`executable_path`、`executable_modified_at`、`executable_changed_since_start`），以及
监听端口与 `observation`。它不输出 `git`、`drafts`、`changed`、attention 交接或每个服务
一份的清单字段；需要这些时用完整 `status`。

每服务的事实只在确实存在时出现：观察到健康结论时才有 `health`，已知监听者时才有
`port_actual`，运行有可读的启动快照时才有 `code_changed_since_start`。缺席表示未知或不适用
（已停止的服务没有 `health` 与 `port_actual`），绝不是否定结果；先读 `running` 再看这些键。

运行中的服务，如果其 `port_actual` 上的监听者不是它自己的运行（另一个运行的监听者，或
option-berth 没启动的进程），就带 `port_holder`：与失败记录相同的 `port`、`pid`、`command`、
`kind` 形状，另一个运行持有时另有 `group`、`name` 和 `run_id`。这是端口背后进程的肯定事实；
缺省表示这次快照没有显示别的归属者，不代表 option-berth 启动了这个监听者。

服务的 `last_exit.reason` 会区分普通崩溃（`crashed`）和固定端口被其他进程占用
（`port_occupied`），也会标出命令尚未启动就失败的服务（`start_failed`），因此“启动后退出”、
“端口冲突”和“启动前失败”不会混成同一种状态。

已记录运行的进程在没有任何 runtime service 观察时消失——宿主已停止，或该运行比一次崩溃活得更久——
会有自己的终结原因 `ended_unobserved`。结束这件事是已知的，退出状态不是，因此 `code` 为 `-1`，
不能当成进程状态读取。`at` 是宿主记录该结束的时间；完全没有宿主参与的读取会让它为空，因为不存在
观察时间。这个结束是该名字下最新的一代，因此同名更早的崩溃不会再被当成当前结果。runtime service
下次启动时会把同一个结束持久化，并删除承载该证据的 mirror 条目。

`port_occupied` 时，`last_exit.port_hint` 是这次运行被告知要绑定的端口，
`last_exit.port_holder` 描述读取时仍占着该端口的进程：`port`、`pid`、`command`、`kind`，
托管运行时另有 `group`、`name`、`run_id`，可读时另有 `cwd` 和 `bind_address`。
`kind` 取 `oberth_run`（option-berth 启动的运行，可能是另一个 worktree）、
`previous_run`（同一服务更早的一代）、`worktree_listener`（归属于某个 worktree、但没有托管运行）
或 `unknown`（没有 worktree 归属）。它只查失败服务自己的那个端口，不是整机端口表；
占用者已经释放或当前观察不到时字段缺省，缺省表示“读取时没看到”，不是“端口空闲”。

人读输出按项目、服务、依赖、代码和失败原因排列，标签是英文的固定宽度列（services ·
depends on · listeners · code · failed · next · drafts · last look），按终端显示宽度对齐。
每个服务单独一行，同时给出声明端口与当前运行实况；服务状态词带颜色（running 绿、
失败红、常规停止 dim），在 NO_COLOR 下退化为纯文字。无端口 worker 会明确标为 no port，
`port: auto` 尚未分配时会显示 auto port unassigned。`listeners` 行给每个未被运行中服务行
命名的监听一个归属：属于另一个运行的端口写成该运行的名字（`8082 (run finance-web)`），
没有监听的在跑运行写它自己的名字和 pid（`worker (run, no port, pid 1234)`）——`kill` 接受的是
端口或 pid，写名字而不写 pid 的读者没法动手，
没有运行拥有的监听标为 `unowned`，并在进程表可读时带上进程名和 pid
（`9090 (unowned: node, pid 1234)`）。`resources` 行写 `N declared running`；清单之外的运行
由 listener 行点名，所以没有任何声明服务在跑的工作树不会被读成空闲。失败区每个服务只显示最新一条退出记录，并且只在该记录仍是
服务的当前 `last_exit` 时显示：服务已经重新运行、或最新一条是正常退出/被 `down` 停止时，该服务
从失败区消失，旧记录仍留在 `exits[]` 历史里。不同服务各显示一条。端口冲突的失败行直接写出占用者
（端口、pid、命令和归属种类），不需要再跑 `lsof`/`ps`。只有失败记录确实留下了日志文件
才给出 `oberth logs <service> --once` 下一步；该步骤也以 `hint` 出现在 JSON 的 `exits[]` 中，
人读和 agent 读到同一个指引。即使强制停止留下了非零信号退出码，正常退出和被停止的服务也不进入失败区。

`worktree.services[]` 还带有 `health_status`（`status`、`code`、`reason`、`observed_at`）。
运行中的服务声明了 `health:`、首次探测还没有结论时，`health_status` 为
`{status: unknown, reason: pending}`，且没有 `observed_at`；第一次探测结论会替换它。已停止的服务没有结论。
`health` 是声明的规范形式：HTTP 路径，或 `tcp:<port>`，或 `command:<命令行>`；`prepare_inputs` 是
prepare 读取的文件列表，没有声明时为空数组。
启动进行中时，`worktree.services[].starting` 给出这一次启动尝试的瞬时事实：`phase`
（`waiting_dependency`、`prepare` 或 `spawning`）、`started_at`、有效的 `ready_timeout`、
读取时计算的 `elapsed_ms`、本次等待的 `waiting_on`，以及上一次 prepare 耗时的
`last_prepare_ms`（未知时为 null）。它不是一个 run：不进入运行记录、不落盘、不算
`running`、不影响 `worktree.status`，runtime service 重启即消失；依赖等待、prepare 与 spawn 期间可读，
run 注册后消失。`--brief --json` 也带同一个 `starting`（字段相同，读取时复制，不与完整文档
共享可变数据），所以精简读者同样能分辨“正在启动”和“从未启动”。人读输出把该服务显示为
`starting · <phase> · for <已启动时长> · budget <ready_timeout> · waiting on <依赖…>`：
时长由 `elapsed_ms` 四舍五入到秒（不足 1 秒显示 `for <1s`，不会显示 `for 0s`），`budget` 直接用
发布值，只有 `waiting_on` 非空时才追加最后一段。

运行中的服务带 `stats`（`cpu_percent`、`memory_rss_bytes`、`thread_count`、`uptime`、`state`、
`connections`）。有监听的服务取监听行的样本；无端口的 worker 取它运行根进程的样本，所以
worker 不再因为不占端口而看不到资源。没有样本时字段缺省，表示**未知**，不是 0。
`worktree.running_services` 是正在运行的声明服务数，不统计清单没有声明的运行；那些运行和
进程持有的监听读 `worktree.ad_hoc_runs` 与 `worktree.unowned_ports`。`worktree.resources` 是
声明服务样本的 worktree 汇总（`cpu_percent`、`memory_rss_bytes`、`services` 为贡献样本的
服务数）。汇总只在至少一个服务有样本时出现，并明确 `services` 与 `running_services` 的差异；
没有样本时缺省，不会用 0 冒充测量结果。
运行中的服务同时带 `manifest_hash` 与 `runtime_spec_hash`；两者不一致时
`manifest_runtime_mismatch` 为 `true`，表示当前进程来自旧清单，应该用
`oberth restart --only <service>` 重新应用。hash 只覆盖命令、cwd、端口、health 和依赖等可执行声明，
不包含环境变量内容，也不包含 `sources:`——改变检查范围本身不需要重启进程。

运行中的服务若在启动时采集了代码身份，`status` 会按 `sources:` 声明的路径重算内容指纹并发布
`code_changed_since_start`：`false` 表示已核对未变；`true` 时能精确比较文件时给出
`code_changed_files`，并给出可直接执行的 `code_hint`（`oberth restart --only <service>`）。
`code_head` 是启动时 checkout 的 commit，没有提交或不在 Git 检出中时缺省。这些字段缺省表示**未知**
——旧版本启动的运行、快照已删除、或源码当前读不到——不是“没变”。逐文件快照超过保存上限时，
仍能回答是否变化，但文件数缺省。已经持有 group 行的客户端通过 runtime service 的 `groups.code` 方法读
同一份比较：字段名与「null 即未知」的规则相同，回复里没有的服务是未知，不是没变。

由本引擎启动的运行，`executable_path` 是它启动的程序的解析路径，`executable_modified_at` 是读取时
该文件的修改时间，`executable_changed_since_start` 在文件新于运行开始时间时为 `true`（已吸收
公开的开始时间只到秒的精度）。这是源码检查的编译产物对应项：直接运行的二进制在运行开始后被
重新构建会报 changed，解释器的脚本仍由 `sources:` 覆盖。这些字段缺省表示**未知**——运行早于
该事实、路径无法解析、或文件已不在——不是“没变”。

监听表不可信时（例如 agent 沙箱拒绝系统内省），`status` 仍然返回清单、服务、运行记录、
日志路径和最近退出；顶层带 `observation`（`state`、`code`、`source`、`sources`、`detail`、`hint`、
`checked_at`、`degraded_since`、`recovered_at`）。
完全失明时 `ports` 为空、`port_actual` 为 null；部分行不可读时 `ports` 只含可读行，
未读到的端口不在其中，`observation.state` 为 partial。`up --wait` 在所选服务需要监听或健康观察、且观察不是 observable 时
立即返回观察类错误并保留刚启动的运行，不会等满超时再把它停掉；`logs <service>` 对托管服务
仍使用运行登记的日志路径，不依赖监听表。需要自启 runtime service 的写命令会先请求已安装的用户级
服务单元；没有单元时才跑自证探针，失明时
拒绝从当前进程创建 runtime service，提示在普通终端运行 `oberth runtime service run --detach`。

系统观察繁忙时，已知运行和 worker 状态可以独立发布。上一批监听信息过期时，`observation.state` 为 `partial`，code 为 `observation_incomplete`，source 为 `runtime_registry`；缺失端口和 `machine` 可用性仍为未知，不是空闲或已停止。无端口且无健康检查的 worker 可以仅凭精确运行身份就绪。其他就绪判断及强制控制路径不会把这种回退当作新鲜监听观察。

JSON 顶层固定包含：

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "identity": "wt_0123456789ab", "services": []},
  "ports": [],
  "git": {"snapshot": {}},
  "drafts": [],
  "exits": [],
  "changed": {"at": "..."}
}
~~~

完整文档的 `git.files` 最多 100 条；`files_total` 是截断前的总数，`files_truncated` 为 true
时表示列表只是一个前缀。完整文件列表用 `oberth git files --json`，它不截断。

`--brief --json` 是独立的精简文档：

~~~json
{
  "scope": {"kind": "worktree", "name": "demo", "root": "/work/demo"},
  "worktree": {"name": "demo", "identity": "wt_0123456789ab", "branch": "main",
    "running_services": 1,
    "resources": {"cpu_percent": 1.2, "memory_rss_bytes": 104857600, "services": 1},
    "services": [
      {"name": "api", "running": true, "port": 3000, "port_actual": 3001,
       "health": {"status": "ok", "code": 200},
       "code_changed_since_start": true, "code_changed_files": 2,
       "code_hint": "oberth restart --only api",
       "executable_path": "/work/demo/bin/api",
       "executable_modified_at": "2026-10-31T10:00:30Z",
       "executable_changed_since_start": false,
       "stats": {"cpu_percent": 1.2, "memory_rss_bytes": 104857600}},
      {"name": "worker", "running": false, "exit_reason": "crashed", "exit_code": 1,
       "hint": "oberth logs worker --once"},
      {"name": "cache", "running": false,
       "starting": {"phase": "waiting_dependency", "started_at": "2026-10-31T10:00:00Z",
                    "elapsed_ms": 130000, "ready_timeout": "3m0s",
                    "waiting_on": ["db"], "last_prepare_ms": null}}
    ],
    "ad_hoc_runs": [
      {"name": "finance-web", "run_id": "run-7", "group": "demo", "pid": 64530,
       "cmd": "yarn dev", "cwd": "/work/demo/web",
       "started_at": "2026-10-31T09:00:00Z", "ports": [8082]}
    ],
    "unowned_ports": [
      {"port": 9090, "pid": 55120, "command": "python3 -m http.server 9090"}
    ]},
  "ports": [3000, 3001],
  "machine": [{"name": "redis", "port": 6379, "listening": true, "listening_known": true}],
  "exits": [{"run_id": "...", "name": "worker", "reason": "crashed", "code": 1,
             "exited_at": "2026-10-31T09:00:00Z", "hint": "oberth logs worker --once"}]
}
~~~

`worktree.manifest_missing` 只在没有 `oberth.yaml` 声明这个 worktree 时出现且为 `true`，于是空的
`services` 表示「没有可读的清单」而不是「没有声明服务」。完整文档用 `config_path` 为 null 说同一件事；
brief 丢掉了路径，没有这个标志，brief 读者就分不清一个等待 `oberth init` 的 checkout 和一个已经配置
好、只是空闲的 checkout。清单存在时该键不出现。

`worktree.ad_hoc_runs` 给每个未被运行中服务行命名的监听一个归属，此外还列出这个 worktree 启动、
但完全没有监听的在跑运行。运行按 run id 识别，不按名字：
清单没有声明的 `oberth start` 运行、仍用着声明服务名字的旧代际、以及声明服务的运行额外持有的
端口都会出现在这里，没有端口的运行同样在内——它是 option-berth 拥有的进程，`down` 会停它、
`restart --run` 会重放它，漏掉它就只能去翻进程表才找得到。每行带 `name`、`run_id`、`group`、
`pid`，运行 mirror 还保留时带记录的 `cmd`、`cwd` 和 `started_at`（缺省表示未知），以及排好序的
`ports`；没有端口时 `ports` 是空数组。运行中服务行已经命名的运行不会在这里重复：服务行就是它的
归属。`worktree.unowned_ports`
是剩下的一类：没有 option-berth 运行拥有的监听，每项带 `port`，进程表可读时还带 `pid` 和
`command`。`ports` 仍是扁平的监听列表；运行中服务的 `port_actual`、`ad_hoc_runs` 和
`unowned_ports` 合起来覆盖其中的每一项。声明服务的监听者若属于别的进程，由该服务的
`port_holder` 指明；已经有运行的监听不会在那里重复。

`worktree.identity` 就是清单里 `${worktree}` 展开成的值。它由 checkout 的规范化根路径
派生，所以同一个 checkout 每次读取都相同；人或 agent 要用 `psql` 连这个 worktree 的库时，
直接读这个字段拼库名，不必自己算哈希。

changed 是第一次运行时缺省的可选字段。ports 仍是给脚本核对用的原始监听事实，不是
独立的产品对象，也不是整台机器的端口清单。runs 是这个 worktree 拥有的全部在跑运行，
不论清单是否声明、也不论它是否持有监听——brief 文档把这些投影成 `ad_hoc_runs`，两者是
同一个集合。没有监听的运行只出现在这里，`oberth start -d` 起的 worker 因此不必靠 `ps`
才看得见。null 表示运行集合没能读出来，空数组表示这个 worktree 没有在跑运行，两者不是
同一个答案。exits 是最近 5 次结束运行的新到旧历史，
不是当前失败集合；当前事实以 `worktree.services[].last_exit` 为准。`exits[].ports`
描述退出运行仍持有的端口，所以已退出的运行保持空数组；失败端口在 `port_hint`，
占用者在 `port_holder`。每条失败记录可带 `hint`（与 `groups.start` 的 per-service hint 同义），
这是 CLI 按日志文件是否存在填出的确定性下一步。详细输出用 logs。
`worktree.services[]` 的 `pid`、`run_id` 和 `started_at` 在服务由 option-berth 正在运行时提供，
无端口 worker 也能靠这些字段核对实际运行记录；`log_path` 始终由引擎给出。

状态里出现确定的异常事件就会带事实型交接文件，与是否启用可选评估无关。`attn_path` 指向
`BERTH_HOME/attention/` 下的一个可重算、可过期的 agent 交接文件；没有当前异常时该字段缺省。
**它出现表示「有异常」，不表示「用户已同意」**——发布一个本地事实不向任何地方发送数据，
所以同意不能决定它是否存在。相邻的 `attn` 块说明这份交接现在能否被评估：`enabled` 是同意位，
`reason` 是当前无法运行时的固定原因码，`assessment` 取 `assessed`（文件已带校验过的结果）、
`pending`（已启用且配置完整，可以评估）或 `unavailable`。agent 可以在没有配置模型时使用
事实型交接文件，但在运行 `oberth attn assess` 前必须检查 `attn.enabled`。
有异常时同时提供 `attn_revision`，它必须与文件中的 `state_revision`
相等，供 agent 在同一份 `status` 响应中核对文件是否仍对应当前事实。
未声明的监听不是异常：`status` 已经列出该端口及其属主，agent 没有启动它，也不该去停它。
核心文件是 `oberth.attn/v1`，包含 `event_id`、`state_revision`、`worktree_root`、
可选的 `branch`、`generated_at`、`fresh_until`、`first_seen`、`last_seen`、可选的 `recovered_at`、`event`、
可选的完整 `events` 数组、`evidence` 和固定 `options`，不包含 shell 命令或模型调用结果；
`event` 保留为第一个异常的兼容字段，多个同时异常必须以 `events` 为准；
可选的 attention adapter 可以在文件上补充 `assessment`，但 agent 仍须先重新读取 `status --json`，
核对 worktree、`attn_revision` 与 `state_revision`，以及 `fresh_until`，再向用户提出选择。
`assessment.context_id`（若存在）只是 adapter 对当前任务和授权选项 allow-list 的哈希缓存键，
不属于运行事实，也不授予执行权限。
这个文件是派生通知，不是新的运行事实来源；
模型客户端不在 runtime service 或 `status` 的控制路径里：确定性部分在 `internal/attn`，模型客户端在
`internal/attn/model`，这条依赖边界由 `internal/cmd/attention_boundary_test.go` 检查。

## oberth logs <service|port> [--once] [--lines N] [--follow] [--ip address] [--json] [--run current|<id>] [--first-error]

按清单里的服务名查看日志；端口形式继续可用。清单没有声明、但有运行记录带着的名字同样能解析
——`oberth start -d --name X` 起的运行，也就是 `status` 在 `ad_hoc_runs` 里和退出提示
`oberth logs X --once` 里给出的同一个名字——读取就用该运行自己的日志文件。同名时清单声明的服务
优先。服务有监听端口时，命令读取该进程的日志；
没有端口的 worker 读取该次运行的服务日志文件。默认先输出当前这次运行的最近几行，并在它仍在
写入时持续跟随；--once 只读一次后退出。只要记录了当前运行的起点，读取就限定在这一代——有
runtime service 或完全没有都一样——所以跨运行追加的文件不会把前一天的日志混进这次运行。
当前这一代没有记录起点时，读取整个文件，并在文档里标 `run_start_unknown: true`，人读输出也会
说明范围未知。--ip 用来消除同一端口绑定多个地址时的歧义。--json --once 输出一个文档：

~~~json
{"source":"/path/to/service.log","lines":["..."],"truncated":false,"service":"api","run_id":"...","pid":1234,"status":"running"}
~~~

打印出来的行遵循颜色规则：颜色关闭时（`--no-color`、`NO_COLOR`、`color: false`，或 stdout 不是
终端），服务自己写入的 SGR 颜色序列会从打印的 `lines` 与 `first_error.text` 中移除，日志文件
仍保留服务写入的字节；颜色开启时按原样打印。其它转义序列（光标移动、屏幕控制）不会被改写。

`--run current --first-error` 的文档：

~~~json
{"source":"/path/to/service.log","lines":[],"truncated":false,"service":"api",
 "run_id":"...","log_offset":1234,"first_error":{"line":42,"text":"ERROR could not connect"}}
~~~

服务日志文件跨多次运行追加。`--run` 只读该文件里这一次运行自己的那一段：从 `log_offset` 到
同一服务下一次运行开始写入的位置，因此后续运行的行——包括它崩溃时那条错误——不会被当成这次
运行输出的一部分。仍在运行的那次运行拥有起点之后的全部内容；两次运行在时间上重叠时，它们的
字节交织在同一个文件里，无法区分。已经结束的那次运行只拥有文件里已写入的字节：后续运行还没有
记录接管位置时（最近一次结束的运行就是这种情况），它的这一段止于读取时的文件末尾，跟随它会
打印后返回，而不是等待一次尚未开始的运行。`--run current` 选该服务当前这次运行，其他值则是
`status --json` 里的精确 `run_id`。`current` 优先选仍在运行的进程，没有时才选最近结束的一次。
跟随一次已经结束的运行会打印它的行然后停止：之后不会再有属于它的输出。`--run` 只接受服务名，
不能和 `--ip` 同时使用。服务日志文件里的运行分段记录在本地磁盘上——运行中的记录在 mirror，
结束的记录在退出行——所以服务日志文件这一形式不需要 runtime service；只有监听输出需要宿主，
没有可达宿主时这种情况返回 `runtime_service_unavailable`。旧运行记录
没有 offset 时返回 not_found 并提示重启一次服务。文件被轮转、记录的那一段已不在文件里时，
读取回退到文件开头，并在 JSON 里用 `run_start_unknown: true` 说明这次的行可能混入其他运行。

`--first-error` 与 `--run` 一起使用：按固定词表（不区分大小写的 error、exception、fatal、
panic、traceback）定位该次运行的第一条匹配行，返回行号和原文。它是位置，不是根因判断；
没有匹配时 JSON 带 `no_first_error: true`。`--first-error` 隐含 `--once`。

`--run` 的 JSON 文档在原有字段上增加 `log_offset`、可选的 `run_start_unknown`，以及
`first_error`（`line`、`text`）或 `no_first_error`。行号从该次运行的起点算起；回退到文件
开头时，行号也按回退后的内容算，`run_start_unknown` 是读这份行号的前提。

跟随模式使用 NDJSON，每行带有 `source`、`line` 以及可用的服务和运行元数据；一次性读取还会在
服务已退出时给出 `exit_code` 和 `reason`。找不到端口返回 not_found；多个绑定未指定 --ip 返回
ambiguous。

## oberth events

供 agent 订阅本机 runtime service 的状态变化。并行联调时 agent 应根据任务中明确给出的
关系主动建立并消费订阅；人手执行此命令主要用于排障。`--worktree PATH_OR_ID` 会沿已有 Repo 关系包含同仓库
的其他 worktree；`--repository REPO_ID` 是可重复的加法过滤器，`--workspace NAME` 加入显式的临时联调关系；
首条 NDJSON 直接是 `{"type":"state.snapshot",...}`，后续每行直接是 `{"type":"state.changed",...}`。
workspace 首次建立实时订阅时必须同时提供 worktree 或 repository，后续订阅者可以只带 workspace；
`--once` 只读快照，不登记 workspace 成员。
RPC 的 filtered `state.subscribe` 使用 `filter: {worktrees, repositories, workspace}`，并且必须把
`events` 设为 `true`；只取一次快照时使用 `state.snapshot`。
快照和变更只包含 worktree、服务、端口的运行事实摘要，不包含命令、工作目录、日志或环境变量；
需要细节时回到对应 worktree 执行 `oberth status --json`。客户端应保存快照或变更中的 `seq`，
断线重连时把最后应用的序号作为 `after_seq` 传给 `state.subscribe`；runtime service 能覆盖时会先返回该序号的
基准快照，再按顺序补发增量，窗口已过期或状态链不连续时则返回当前完整快照。`oberth events` 会在传输断开或队列溢出后
自动带着最后的 `seq` 重连；workspace 成员变化仍要以新快照为准，队列窗口过期时也会回到完整快照；
成员变化会以 `changed: ["workspace_changed"]` 提醒现有订阅者。若连接到旧 runtime service，先执行
`oberth runtime service restart` 以取得 `state.scope` 能力。

## oberth worktrees [--gone] [--json]

只读列出本机 runtime service 见过的 `${worktree}` 身份、派生它的 checkout 根路径、最近一次发布的
组名和该路径现在是否还存在。它是身份账本，不是运行事实：行在 checkout 删除或移动后保留，
`exists` 在每次读取时按文件系统重新判断，所以同一路径重新出现时身份也重新存在；
`last_seen` 只是最近一次写入账本（runtime service 启动或组名变化）的时间，不是每次扫描的心跳。

`--gone` 只保留 checkout 已不存在的身份，供人或 agent 找到按旧身份留下的数据库和数据目录
（`app_${worktree}`、`.data/${worktree}`）。删除动作仍由人执行：命令不删除账本行、数据库、
数据目录或正在运行的进程；要停止仍在跑的服务用 `oberth down --identity <wt_…>`。

有 runtime service 应答时，每行还带 `running_services`：该身份下正在运行的声明服务数。它是读取时
对组事实的一次连接，不写回账本。没有 runtime service 时字段缺省——账本不存运行状态，缺省表示
**未观察**，不是 0。

~~~json
{"worktrees":[
  {"identity":"wt_0123456789ab","root":"/work/demo","group":"demo","exists":true,"last_seen":"2026-10-30T09:00:00Z","running_services":2},
  {"identity":"wt_abcdef012345","root":"/work/old","group":"demo@old","exists":false,"last_seen":"2026-10-28T18:00:00Z"}
]}
~~~

没有 runtime service 或还没有账本时返回空列表，不会为了读这份事实启动 runtime service 或新建数据库。
runtime service 是账本的唯一写者：它在扫描到清单时记录身份，重启后按已记录的根路径补齐；
`status` 等读命令和 `down` 都不写账本。
