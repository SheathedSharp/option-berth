# 运行操作

[English](operations.md) | 中文

让服务开始运行或停止。下面每条命令都是完整契约，包括它拒绝做什么，以及会留下什么继续运行。

## oberth up [project] [--only name,...] [--no-deps] [--wait|--ready] [--wait-timeout duration]
[--allow-outside-home] [--json]

按 oberth.yaml 的 depends_on 顺序启动服务。`--only` 点名服务时，默认连同它们传递的
`depends_on` 闭包一起启动（例如 `--only web`、`web → api → db` 会依次启动 db、api、web）；
显式点名的服务保持点名身份，被依赖带上的服务会在输出中标出由哪个点名服务带上。
`--no-deps` 只启动点名的服务本身，不展开依赖闭包；没有 `--only` 时它不改变行为（本来就启动全部）。
人类可读输出中被带上的行附加 dim 说明（如 `started (dependency of web)`、`skipped · required by web`），
汇总在非空时另起一行 `included dependencies: api, db`。`--json` 中它们仍是普通 `services[]` 条目，
带 `included_by`（排序后的点名根服务）；汇总带 `included`（排序）。能力协商：旧 runtime service 不认识
`include_dependencies`，新 CLI 只在 `--only` 点名了至少一个服务时才发送它，此时遇到旧 runtime service
会直接报错并提示重启 runtime service 或传 `--no-deps`；没有 `--only`（或 `--no-deps`）时不发送，普通 `up` 不受影响。
服务有 `prepare:` 时，先在同一 cwd 直接执行这个前置命令，
成功后才启动长驻的 `cmd:`；前置命令的输出写入该服务日志，失败时服务不会启动。
服务有 `env_file:` 时，prepare 与长驻命令都会先读到文件里的变量（见[共享环境文件](declaration.zh.md#共享环境文件)）。
`prepare_inputs:` 声明该命令读取的输入文件（相对服务 cwd，例如 `[package.json, package-lock.json]`）；
上次 prepare 成功且这些文件内容未变时，本次 `up` 直接跳过 prepare，不再重复安装依赖。指纹只在上次
成功之后记录，输入缺失或读不到时一律重跑；缓存位于 `$BERTH_HOME/prepare`，删除它只会让下次启动更慢。
`sources:` 声明用于判断“进程是否在跑当前代码”的文件或目录（相对服务 cwd，缺省为该目录）；
启动时记录这些路径的内容指纹和当时的 HEAD，`status` 重算后发布 `code_changed_since_start`。
Git 检出中只纳入已跟踪文件和未被 ignore 的未跟踪文件，`node_modules`、`target`、`dist` 等不会进入
指纹——即使它们被显式写进 `sources:`，因为它们通常是源码产出的制品；非 Git 目录只应用固定忽略
列表。`sources:` 的路径本身不能经过符号链接（要声明链接目标）；目录内更深层的链接按链接本身记录，
不跟随到目标。缺省源树相同的多个服务（例如都缺省到同一个仓库根）会一起报告变化；要区分它们，
显式写上各自真正读取的 `sources:`。显式写 `sources: []` 关闭这项检查，适用于自己热重载代码的服务。
带端口的依赖等待监听，无端口的 worker 等待其运行记录变为活跃；
已经在运行的服务跳过；每个服务有自己的
日志文件和运行记录。监听者不是 option-berth 启动的进程时仍是跳过，但原因会写明这一点，
`hint` 会给出要释放的端口；option-berth 不会接管自己没启动的进程。无参数时使用当前目录最近的
清单，也可以显式给项目名。
`ready_timeout:` 声明一个服务的就绪预算（Go duration 字符串，例如 `90s`，缺省 30s，必须为正）：
`depends_on` 的后继服务等待该服务时使用它，`up --wait` 也使用它。`prepare:` 不计入这个预算。
清单声明了 `ready_timeout:` 时，CLI 要求 runtime service 宣告 `groups.start.ready_timeout` 能力；旧 runtime service
会提示 `oberth runtime service restart`，而不是静默按 30s 等待。按项目名启动（`oberth up <project>`、
`oberth restart <project>`）时 CLI 手上没有清单，这项检查改为向 runtime service 的 `state.snapshot`
找到该项目的清单路径后本地解析，再套同一套能力规则；清单找不到或读不出来时不阻断启动，
由 runtime service 报它自己的错误。

`health:` 有三种写法：HTTP 路径（`/healthz`，缺省形式）、TCP 连接（`health: {tcp: 5432}`，端口必须与
服务声明的端口一致）和命令（`health: {command: pg_isready -h 127.0.0.1 -p ${port}}`）。命令在服务
cwd 中执行，按 `cmd:` 的规则拆成 argv（不经过 shell），支持 `${port}`、`${url}`、
`${<service>.port}` 和 `${worktree}` 引用，退出码 0 视为健康。HTTP 和 TCP 只说明端口可访问；数据库这类端口先开、
协议后就绪的服务用命令式检查。健康结果与依赖等待使用的是同一个事实。

成功时人读输出逐服务报告并给出汇总；`--wait` 等待期间先输出一行 dim 提示再逐行出结果。
--json 输出一个包含 services 和汇总字段的值。每个
`services[]` 都有结构化的 `state`：`started`、`skipped` 或 `failed`，失败时还给出机器可分支的
`reason` 和可直接执行的 `hint`，原有 `error` 保留给人读。`hint` 只指向真实存在的证据：进程起来过
才给 `oberth logs <service> --once`，依赖没就绪时给**依赖**的日志，还没起进程就被拒绝（例如 worktree
在 home 之外）时给拒绝本身带的做法，没有可执行的下一步时不给。失败原因为 `port_occupied` 时，
`services[]` 还带与 status 相同形状的 `port_holder`（`port`、`pid`、`command`、`kind`，
托管时另有 `group`/`name`/`run_id`），不需要再自己查进程表。
`services[]` 的 `ready` 是 CLI 的注释，不是 runtime service 事实：`--wait` 确认就绪后为 `true`；
默认不等待时 `started` 服务带 `false`，人读显示 `~` 和 `started, readiness not confirmed`；
`skipped`/`failed` 不带该字段。`false` 不是失败，`state` 仍表示启动动作的结果。存在未确认的
`started` 服务时，命令还会发布下一步：JSON 文档的 `next` 字段与人读输出的 `next:` 行，内容为
`oberth up --only <services> --wait --json`。
等待判定先按运行身份：本次启动 chunk 的 `run_id`、跳过 chunk 指向的正在运行 run，或第一次观察到的同一 run。
只有完全没有 run_id 的旧记录才退回退出时间，并把等待开始对齐到秒，因此同一秒内记录的退出也算本轮。
其他 run 的旧退出记录不会被 `up --wait` 读成本轮启动失败。
`--wait`（`--ready` 的别名）会在返回前等待本次选择的服务（包括已经在运行、未重新启动的服务）真正监听；声明了 `health:` 的服务还必须
通过对应的健康检查（命令式检查不要求服务自己监听）。无端口 worker 以运行记录活跃作为 ready。默认不等待；
不指定 `--wait-timeout` 时按每个服务自己的 `ready_timeout:` 等待（缺省 30s）：本次新启动的服务从状态行里该运行的
开始时间起算，已运行的 skipped 服务或取不到开始时间时从等待开始起算。显式指定 `--wait-timeout` 时它是整次等待的
总上限，只会收紧、不会放宽每个服务的 `ready_timeout:`：每个服务的 deadline 取两者中更早的一个；必须为正值且同时
启用 `--wait` 或 `--ready`，否则退出 2，不执行启动；
超时的服务状态为 `failed`、原因是 `ready_timeout`，消息说明超时来自该服务的就绪预算，还是调用方给的
`--wait-timeout` 上限，并点名该抬高哪一个（预算对应 `ready_timeout:`，上限对应 `--wait-timeout`），
命令退出码为 1。只有本次新启动的超时运行才进入停止清理；
已经存在的运行不会因这次等待失败而被停止或重启。清理按 `run_id` 只停止本次启动的那个运行：
等待期间另一个控制者为同一服务启动的新运行是另一个运行，不会被停掉。runtime service 若未声明
`groups.kill.expect_runs`，就无法把停止限定到指定运行，因此不会收到只带服务名的清理：
命令失败，并说明超时的运行仍在运行。就绪判定同样按运行身份归属：状态行里的运行不是这次等待所指的
运行时就立即以状态 `failed`、原因 `run_replaced` 结束，因为替换运行的监听与健康是它自己的证据，
不能算作原运行的；此时不停止任何运行，替换运行不是本次调用启动的。状态行没有指明运行时也无法确认具名运行：
无法归属到该运行的监听只会让等待继续，不会被算作它的就绪。完全没有 `run_id` 的 chunk
没有可比对的身份，仍按服务行判断。`started` / `skipped` 汇总描述启动动作，
就绪判定失败另外出现在 `errors` 与逐服务 `state` 中。对本次新启动且被清理的运行，
引擎把同一个 `ready_timeout` 写入服务的最后退出事实，因此 `status`、桌面服务行、attention 和事件流
不会把它显示成普通的手动停止。健康事实带有 `status`、`code`、`reason` 与 `observed_at`，
依赖声明了 `health:` 时，后继服务会等待该依赖的健康状态为 `ok`。
依赖等待的预算取 `depends_on` 中被依赖方各自 `ready_timeout:` 的最大值（未声明的依赖按 30s），不是等待方自己的；
依赖等待本身失败时，服务最后退出原因分别是 `dependency_timeout` 或 `dependency_not_ready`，
与启动失败一样会保留在状态、attention 和状态变更中。若依赖是本次 `groups.start` 已经尝试、
并且在等待期间已经退出，后继服务不再等满预算：它立即以 `dependency_not_ready` 失败，
消息带上依赖的退出码与原因，`hint` 指向依赖日志。依赖仍活着但尚未监听时仍按 `dependency_timeout` 等满预算。
任何服务启动失败都会以退出码 1 返回，失败服务仍保留其退出原因供 status 查看。
默认只允许在用户 home 目录内启动服务；明确使用 `--allow-outside-home` 才会放开这个限制。

runtime service 宣告 `groups.start.phases` 能力时，`up` 会在请求里带上 `progress: true`，并在服务进入
`waiting_dependency`、`prepare`、`spawning` 阶段时收到 `state: "starting"` 的进度 chunk：
`phase`、`started_at`、`ready_timeout`、`elapsed_ms`、`waiting_on`、`last_prepare_ms`。
`waiting_on` 随依赖轮询收缩：每次观察到某个声明的依赖已经就绪，它就从集合里消失，
全部就绪时为空；观察本身失败时保留上一次已知集合，不会把“读不到”显示成“没有在等”。
这些 chunk 是诊断，不是结果：它们不计入 `services[]`、不参与就绪等待，也不影响汇总。
人读模式把它们打印成 dim 进度行；`--json` 时进度行只写 stderr，stdout 仍然只有一个最终
JSON 值，不混入诊断。旧 runtime service 不认识 `progress`，旧 CLI 也不会发送，行为与之前一致。

例如一次部分成功的结果仍是一个 JSON 值：

~~~json
{"services":[
  {"service":"api","state":"started","ready":false,"pid":1234,"port":3000},
  {"service":"worker","state":"failed","reason":"start_failed",
   "error":"exec: \"nosuchcmd\": executable file not found in $PATH",
   "hint":"check the command and its working directory"}
],"started":["api"],"skipped":[],"errors":["worker"]}
~~~

## oberth restart [project] [--only name,...] [--run id|name] [--force] [--allow-outside-home] [--json]

先停止再按清单重新启动项目服务。人读输出先报告停止结果，空一行后报告启动结果。
`--only` 只重启点名的服务，依赖和其他服务保持运行；
这类重启保留 `port: auto` 的 worktree 占用，让服务重新拿回原地址。`--force` 跳过优雅停止。
--json 返回一个值，其中 `stopped` 是停止结果，`services` 和汇总字段是重新启动结果。
`restart` 不等待就绪，因此 `started` 服务带 `ready:false`，人读同样标注未确认就绪。

取消或传输失败若发生在已获得停止回执之后，仍会报告该回执及已经收到的逐服务结果。JSON 保持既有 `stopped`/`services`/`start_id` 结构；没有启动终结回复时，`started`、`skipped` 和 `errors` 为 null（未知），而不是成功的空汇总。操作错误仍按标准错误结构写入 stderr，并保留非零退出码（中断为 130）。人读输出同样保留已知结果，不打印启动终结汇总。停止失败绝不启动服务；尚未获得任何停止回执就失败时，不会捏造回执。

`oberth start` 启动的运行没有清单条目：`--run <id|name>` 用记录的 argv 重启它。它通过
`groups.kill` 的 run id 选择器精确停止该运行（无端口运行也包含，退出记录为 `stopped`），
再用记录的 argv、cwd、group、name 和 port hint 通过宿主重新启动。argv 是启动时的精确向量，
从此版本开始记录；早于该版本的运行无法忠实重放，返回 `not_found` 并给出记录的启动行，
不猜引号。名字只在同组恰好一个活运行时接受；id 永远无歧义。`--run` 与 `--only` 选择不同对象，
不能组合。`--json` 返回 `restarted_of`（原运行 id）、`stopped`（运行已经结束时缺省）以及新的
`run_id`、`pid`、`log_path`。精确运行停止需要宿主声明 `groups.kill.runs`；旧宿主会被拒绝，
而不是把停止放宽到整个服务或组。

不带 `--run` 时，清单没有声明的运行不能重启。`restart --only` 点名这类运行且运行账本认识它时，
`not_found` 的 hint 会打印记录下来的启动命令（cwd、group、name、port 与命令），而不是指回
`status`。这条 hint 不依赖清单是否存在：worktree 里根本没有 `oberth.yaml` 时同样给出记录下来的
启动命令，因为知道这条命令的是运行账本而不是清单。账本不认识的名称则回到「没有可用清单」的
`not_found` 及其 `oberth init` hint。

启动半段对「监听者不是 option-berth 启动的进程」的报告方式与 `up` 相同：跳过，并在原因和
hint 里写出进程与端口。`--force` 只跳过优雅停止，不会接管 option-berth 没启动的进程。

停止之前，`restart` 通过窄读 `groups.code` 拿到与 `status` 相同的代码比较，只针对即将停止的
服务；运行中服务的 `sources:` 相对启动时有变化，或该次运行启动的可执行文件在之后被重建时，
在停止前打印 warning。warning 不阻止重启，也不替调用者编造构建命令：它只说明重启会继续跑旧
构建，或会启动新二进制。`--json` 用 `warnings` 带上同样的事实（`service`、`code_changed`、
`code_changed_files`、`executable_changed`）；没有可警告的事实就缺省。没有跑成的比较（运行早于
该事实、快照已删、旧 runtime service 不支持 `groups.code`）不产生 warning——未知不等于未变化。

## oberth down [project] [--worktree path | --identity wt_…] [--force] [--json]

停止该项目清单里的全部服务，动作后确认可释放时交还 `port: auto` 的预留。
无参数时使用当前目录的清单。`--force` 直接使用强制信号，不跳过运行身份或预留释放门禁。
成功时 `--json` 返回停止结果和实际释放数量。有服务或运行没能停下就不算成功：`--json`
仍返回该信封（`ok` 为 false，失败行带原因），但命令以 1 退出，只看退出码的调用方不会
当作项目已经停下。项目清单中声明的 `machine:` 依赖只读其
监听状态，`down` 不会停止它们。`down` 会停止该组里的所有运行，包括 `oberth start` 启动、
清单没有声明的临时 run。`up` 只启动清单里的服务，因此被 `down` 停掉的临时 run 需要用
`oberth start` 重新启动；它们存活时，brief 文档的 `worktree.ad_hoc_runs` 会列出这些运行。

`--worktree PATH` 按 checkout 根路径、`--identity wt_…` 按 `${worktree}` 身份停止。
两者都不能和项目名或清单路径同时使用。它们是 worktree 被删除或移动后的明确入口：
目录还在时 runtime service 重新读取该清单；目录已不在时按身份账本里记录的组名停止仍属于它的
监听与运行，并交还该 worktree 记录的 `port: auto` 预留。找不到身份返回 not_found，
不会猜一个组。目标已经没有任何运行或预留时，显式身份仍然是一次成功的空停止。
`--identity` 的值来自 `status --json` 的 `worktree.identity` 或 `oberth worktrees`。

按项目名停止是组名作用域：同一仓库的不同 worktree 发布为 `<project>` 与
`<project>@<worktree>`，名字不同时互不影响。两个 checkout 真把同一组名占了（例如不同
仓库目录同名，或两个不同父目录下同名的 linked worktree）时，runtime service 的组已经合并，
任何组作用域的停止都会同时打到两者；此时 `down <project>` 和显式身份都返回 ambiguous，
提示改用 `kill` 按端口或 pid 点名，而不是替调用者挑一个。

取消、停止不完整、动作后观察失败、仍有登记运行/监听或预留已被刷新时，不交还未经确认
的预留，返回现有 RPC 错误并提示检查 `status` 和日志后重试。已发送的信号不因这个错误
撤销；`not_found` 不单独证明端口已可释放。无端口运行的真实停止需要准确的出生身份。
底层 dry-run 和 scoped 停止不释放预留；`restart --only` 因此保留原分配。
这不承诺 OS 观察与账本写入原子，也不承诺任意旧版本同时写入同一账本。

## oberth start [flags] -- <command> [args...]

`start` 只运行 `oberth start [flags] -- <command> [args...]` 这一种低级入口；日常项目服务
使用 `up` / `down`。命令参数里的 `${<name>.port}`、`${<name>.url}` 和 `${worktree}` 会在执行前
按当前 worktree 的地址簿展开：本清单的服务、`machine:` 引用和 `workspace:` 引用都可以，`port: auto`
取当前实际端口。因此 `oberth start -- curl '${api.url}/health'` 访问的是这份 checkout 的服务，
双 worktree 联调时不会连到另一份。已声明但当前没有地址的引用会让命令直接失败，不会把 `${...}`
当字面量执行；裸 `${port}` / `${url}` 只在给了 `--port` 时展开。其他参数原样传入。
`start -d` 还会公布守护进程归属到该 run 的监听端口（`port N`）；给了 `--port` 时会短暂等待
归属结果，观察不到就明说，因为命令可能在提示端口被占时改绑其他端口。

## oberth kill <port|pid|run>...

点名停止监听者和运行；--all 是明确的整批操作，仍会确认。
位置参数先按端口读、再按 pid 读，其余按运行名或 run id 读——也就是 `status` 在 `worktree.ad_hoc_runs` 里公布的
那个名字。名字按 `restart --run` 同一条规则在运行账本里解析（精确 run id 总是优先；名字必须唯一指向一个在跑
运行），并限定在命令所在的 worktree。命名运行按整棵树停止——它根进程之下的进程就是这个运行——包括没有监听的
运行；名字指向的代际已经结束就是「没有可停的东西」，而不是一个目标。有 runtime service 应答时，走的是
`restart --run` 同一条精确运行停止，由持有账本的一方钉住代际；没有时，CLI 自己用记录里的根 pid 与开始时间钉住
代际，并拒绝一个从未记录过开始时间的运行。`--no-escalate` 不能与运行名组合：运行总是整棵停。两种词表不混在
同一次请求里：要么给运行名，要么给端口/pid。
人读的结果行用读者词汇：`stopped (SIGTERM)` / `stopped (SIGKILL)` / `stopped container`，
不直接打印信号名；`down` 停无端口 run 时显示服务名（清单里的名字），不是进程表里的解释器名。
被释放的地址行写作 `released <url>`。`--json` 的 `method` 字段仍是 wire 枚举
（sigterm/sigkill/docker_stop/none），人读与机器读各用各的词汇表。
旧的 group/session 选择器只为迁移保留，不出现在公开帮助中。
