# 基础设施与显示

[English](infrastructure.md) | 中文

Runtime Service、这份安装、自检与可选的改进报告，以及首屏显示的内容。

## 基础设施命令

- oberth doctor：检查清单、runtime service、CLI 路径、日志目录和当前进程能否看见监听，给出可操作的修复提示。
  `--only listener_observation` 绑定一个真实本地端口，再用与 `status` 相同的采集器确认能否看到它：
  报告 observable / blind / bind_denied；CLI 模式下同时附带 runtime service 的自证结果，runtime service 失明也会失败。
  `--only runtime_service_build_matches` 对比 CLI 与 runtime service 的构建提交；同版本号不代表同构建。
  缺少提交身份或状态 RPC 失败会报告 warn，不会标记匹配，也不会自动重启 runtime service。
  `--only agent_skill` 只读 `HOME` 下已安装的 skill（`~/.claude`、`~/.codex`、`~/.config/opencode`
  的技能目录），比较各份 `SKILL.md` 的 `metadata.version`：没装是 `skip`，只有一份永远 `ok`，
  多份版本不一致或软链指向已不存在的 checkout 才 `warn`。它不联网，不知道哪个版本是最新的，
  因此不会把单独一份说成「落后」，也不安装、不更新、不修复 skill。
  `--only agent_integration` 读取最近清单所在目录的 `AGENTS.md` 与 `CLAUDE.md`，任一提到 oberth
  即 `ok`；没有清单是 `skip`，有清单但两个文件都没提到 oberth 是 `warn`，fix 指向
  [接入编排工具](../integrations.zh.md) 的粘贴段落。它只读不改，也不知道某个工具是否真的会加载这些文件。
- oberth runtime service run|status|path|stop|restart|log|schema：管理或查看后台采集器。`runtime service status --json`
  带 `observation`（`state`、`code`、`detail`、`hint`、`checked_at`），表示 runtime service 自己能否看见监听；
  `runtime.service.hello` 也带同一份结果，客户端不能把失明 runtime service 的空端口表当成事实。
  `run` 默认前台，`--detach` 转后台；`--persistent` 无视 `runtime_service.idle_timeout` 关闭空闲退出，用户级服务单元正是带它启动的。装了该单元的机器上，`restart` 重启单元，不会另起一个游离进程。
- `oberth -v` / `oberth --version`：简洁地打印当前 CLI 版本；`oberth config init|path|edit`、`oberth runtime service run`、`oberth version`、`oberth completion`：配置与运行环境入口。
- `oberth version --json` 输出当前构建的 `version`、`commit`、`built` 和 `platform`，供安装检查脚本确认实际运行的构建。
- `oberth version --check` 读取最新发布标签，报告本机是否落后；`--check --json` 在原来的字段上增加
  `latest`。它只读发布信息，不下载、不安装，也不替换正在运行的二进制。终端命令复用同一份检查结果：
  首屏显示本机版本并给出完整升级块，其他输出给人看的命令在本机落后时以两行升级提示收尾。
  同一构建对同一发布只提示一次。成功结果缓存 5 分钟。首屏最多等待 2 秒，因为版本正是在这里被问到的；
  其他命令只读取已经就绪或接近就绪的答案，最多等 250 毫秒；显式 `--check` 最多等 6 秒。
  失败会连同种类一起记下：被这份预算掐断的检查 2 分钟后重试，发布源不可达或应答不是发布标签的则记 30 分钟。
  `BERTH_NO_UPDATE_CHECK=1` 可关闭自动提示。JSON 输出、`--help` 与显式 `--check` 都不加这段提示。
  检查同时读取 GitHub 的发布元数据和 CDN 上的标签列表，GitHub 慢或被限制时不会因此看不到新版本。
  所有来源都不作答时 `--check` 以非零退出并说明原因，且任何终端路径都不把未知当成“最新版”：首屏说明更新检查不可用
  并给出 `oberth version --check`，页脚保持沉默。检查结果缓存在
  `BERTH_HOME/update-check.json`（默认 `~/.option-berth/update-check.json`），只为这些提示复用，不是运行事实。

`oberth version --json` 的 stdout 是一个 JSON 值：

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64"}
~~~

`--check --json` 在这个对象上增加 `latest`：

~~~json
{"version":"v0.1.0","commit":"<git-sha>","built":"<UTC timestamp>","platform":"darwin/arm64",
 "latest":{"version":"v0.1.1","checked_at":"<UTC timestamp>","update_available":true}}
~~~

`latest.version` 是发布源报告的最新标签；只有本机版本严格更旧时 `update_available` 才为 `true`，
本机版本更新（例如从 `main` 构建）或无法比较（例如 `dev`）都为 `false`。

### Runtime Service 协议 3 切换

Runtime 运行领域仍是 worktree → manifest → service → run → runtime-facts 事实链，Runtime Service 是后台宿主。产品 3.0.0 / 协议 3.0.0 直接移除旧公共宿主命名空间，不保留别名。能力协商仅适用于协议主版本一致的双方，不能让 v2 宿主变成可接受的目标。

<!-- retired-runtime-v2-begin: refused names and manual switching only -->
| 退役的 v2 输入 | 协议 3 替代项 |
|---|---|
| `oberth daemon …` | `oberth runtime service …` |
| `oberth serve [--detach]` | `oberth runtime service run [--detach]` |
| `--no-daemon` | `--no-runtime-service` |
| `daemon.*` RPC methods | `runtime.service.*` RPC methods |
| `daemon_*` JSON fields/check IDs; `Daemon*` wire definitions | `runtime_service_*`; `RuntimeService*` |
| `daemon:` config stanza | `runtime_service:` |
| `daemon.sock`, `daemon.log`, endpoint `daemon.lock` | `runtime-service.sock`, `runtime-service.log`, endpoint `runtime-service.lock` |
| `BERTH_SOCKET` | `BERTH_RUNTIME_SERVICE_SOCKET` |
| `BERTH_DAEMON_DETACHED` | `BERTH_RUNTIME_SERVICE_DETACHED` |
| `BERTH_NO_AUTOSTART` | `BERTH_RUNTIME_SERVICE_NO_AUTOSTART` |
| `BERTH_ALLOW_TEST_DAEMON` | `BERTH_RUNTIME_SERVICE_ALLOW_TEST_BINARY` |

新 CLI 对旧命令、参数、配置键及非空的旧环境变量明确报迁移错误，不输出环境变量值。 无法解析的 YAML 以及无效/相对的 `previous_service_lock_dirs` 在操作前直接拒绝，不会改用默认安全门闩；配置读写共用同一校验。旧 `daemon.*` 请求得到带协议 3 提示的 `not_found`。没有旧 socket 探测、命令别名、RPC 双路、自动关闭旧宿主或删除数据。

真正切换之前，必须使用匹配的 v2 CLI 显式停止旧宿主（`oberth daemon stop`；若是自定义端点，则使用旧端点设置），并确认状态已经停止。之后再改 shell/配置键，保留数据库及 run ledger，用配套新 CLI 执行 `oberth runtime service restart`（未安装用户级常驻单元时用 `oberth runtime service run --detach`）。不要用新 `restart` 或 `doctor --fix` 停止 v2 宿主。本次源码改造不执行该切换，也不安装任何东西。

`daemon.lock` 只能作为额外的跨代门闩，绝不是传输回退。可选的 `runtime_service.previous_service_lock_dirs` 为已知旧端点增加持续持有的门闩；数据库安全不依赖枚举这些目录。新宿主在监听、恢复或发布之前，以单个不超时连接取得 SQLite EXCLUSIVE/WAL 所有权。未修改的 v2 Store 已持有数据库时，新宿主拒绝进入；新拥有者运行期间，旧/新写者即使换端点、`BERTH_HOME` 或文件别名也不能重开。没有打开 SQL 连接的静默旧进程不被宣称已识别或停止：新拥有者持有 SQLite 时，它不能恢复数据库写入，显式关闭旧宿主的切换步骤仍然适用。每次写入正常提交，不会把事务一直留到退出。拥有者连接丢失后直接失败，不偷偷重连。所有生产者停止后才关闭存储，端点锁最后释放。
<!-- retired-runtime-v2-end -->

`runtime.storage.read` 从现有拥有者 Store 读取，`section` 为 `schema`、`worktrees` 或 `run_exits`。响应重复 `section`，并携带该部分的回执（`schema` 对象、`worktrees` 数组或 `run_exits` 数组）；观察到的空集合是 `[]`，不能省略回执。schema 包含路径、版本及文件大小。worktree 行和最多 100 条持久退出记录使用生成 schema 中的 snake_case 字段。不接受 SQL 文本或写入。本地 CLI 读取发送绝对路径 `expected_db_path`；拥有者核对文件身份，同一端点对应不同或缺失数据库时拒绝，同文件路径别名则接受。其他 RPC 调用者若明确要读取所选端点，可省略这项本地路径断言。CLI 历史、worktree 身份及 doctor 在线时走此路径；只有拥有者缺席时，才离线只读打开同一个数据库。已连接拥有者的错误、缺失/错误回执、取消及所有权失败都不是成功的空结果。等待拥有者连接的读取遵守取消。

离线只读访问不迁移或修改数据库/WAL 内容，但读取崩溃后遗留的 WAL 时，SQLite 可能创建或更新 `-shm` 协调文件；这不是零文件系统写入模式。

拥有者在单连接上串行执行 SQL 读写，不再保证原有连接池的并行读取。持有所有权期间，外部 SQLite 读取及在线 SQLite 备份连接会被拒绝。离线备份数据库前，须用匹配 CLI 正常停止 Runtime Service；仅复制运行中数据库的主文件不是受支持的备份。

Runtime Service 的存储及控制文件目录必须位于已验证的本地文件系统：macOS 为带本地标记的 APFS/HFS/tmpfs，Linux 为明确支持的本地文件系统类型，Windows 为本地固定/可移动/RAM 卷。网络、FUSE 及未知文件系统在创建存储 sidecar 前被拒绝；换 socket 不能削弱数据库所有权。坏库及未来 schema 阻止启动并保留可检查证据，不会静默挪走、重置或替换。命名改造不改变数据库 schema 及历史记录。

默认根 `BERTH_HOME` / `.option-berth`、数据库 schema/内容、`oberth` 可执行文件和公共 Go module 路径不变。Windows 使用 `\\.\pipe\option-berth-runtime-service`。外部客户端必须先适配协议 3、新端点及字段/检查名才能控制此宿主。

### oberth feedback

改进报告是可选的，说的是引擎自身。发布构建在首屏（无参数的 `oberth`）问一次，而且必须两端都是
终端。答案以仅本人可读的方式记在 `BERTH_HOME/feedback.json`（默认 `~/.option-berth/feedback.json`）：
跳过只会在升级后再问一次，已做的决定不再问；`BERTH_NO_FEEDBACK`（只要存在就关闭，和 `NO_COLOR`
一样）同时关掉询问和发送。

| 命令 | 做什么 |
|---|---|
| `oberth feedback status [--json]` | 已记录的决定、`gh` 持有的 github.com 凭据、一份报告会走的通道、本安装的报告总数，以及最近一天提交了几份 |
| `oberth feedback on` / `oberth feedback off` | 不用等首屏，直接记录决定；`on` 会写明将由哪个账号提交 |
| `oberth feedback report [--json] [--dry-run]` | 组装并提交一份报告 |
| `oberth feedback log [--json]` | 每份报告对应 issue 的状态、关闭时间、标签和链接，经 `gh` 读取 |

`report` 只接受固定词汇：`--kind`（`slow`、`stalled`、`coarse`、`misleading`、`observation`、
`other`）、`--command`（oberth 的命令名）、`--error-code`（引擎会发布的错误码，包括只有 CLI 才会
报告的 `runtime_service_unavailable`、`runtime_service_not_running`、`interrupted`、`timeout`、
`stale` 和 `attn_*` 系列）和 `--observation`（`observable`、`partial`、`blind`、`bind_denied`）。
`--elapsed` 与
`--services` 按档位提交，不是原始数字；构建自己补上版本、提交和平台。`--note` 是报告人自己的话：
先脱敏，上限 800 字节，看起来像凭据时直接拒绝，并且在传 `--yes` 之前要给人看过并确认。

终端上没有 `--kind` 或 `--command` 时，`report` 用方向键从同一份固定词汇里问，再问可选的
描述；提交前展示完整报告并要求确认。`--json`、`--yes` 和管道仍走纯参数路径，脚本不会卡在提问上。

`gh` 已安装并持有凭据时，报告通过 `gh issue create` 用报告人自己的 GitHub 凭据提交，因此那个账号在
issue 上可见；只有没有凭据时，命令才打印一条预填好的 `issues/new` 链接由人提交，`--open` 会打开它。
是否真的提交由 `gh issue create` 自己决定：本地探测只说明这次尝试值不值得做，所以探测不到
github.com 也照样提交，提交失败才退回链接并给出真正发生的错误。
`feedback status` 与 `feedback on` 在组装报告之前就写明这份凭据，`feedback log` 用同一个登录读取
已提交的 issue。三者都只读本地 gh 凭据（`gh auth status --json hosts`），不发送任何东西。同一构建的
同一个问题 24 小时内最多提交一次，一个安装滚动一天最多提交二十份。判据包含类别、命令、错误码、
观察状态、版本和脱敏后的描述：任一项不同就是不同的问题。本地账本在
`BERTH_HOME/feedback/reports.ndjson`，它只是记录；删掉它只会让重复提交再次成为可能。

`status` 给出的是预测，不是承诺：`channel` 是此刻一份报告会走的通道，和投递路径共用同一个判据。
它把两类历史分开：`gh` 条目是真正提交的 issue，`url` 条目是人还需要自己按提交的预填页面。
人读的一行是 `N recorded · N filed · N prefilled`，预填页面不会虚增提交数。`--json` 增加
`github`（`available`、`state`、`account`、`reason`）和 `reports`（`total`、`filed`、
`prefilled`、`last_at`）。`state` 取 `signed_in`、`credential_unconfirmed`、`signed_out`、
`not_installed` 或 `unknown`；`available` 为真表示这份报告会尝试经 gh 提交，所以凭据已存在、只是
探测没能连上 github.com 时是 `credential_unconfirmed` 且仍然 `available`——连不上不等于未登录。
人读的 `github` 一行是 `signed in as <account>`、`credential stored for <account> but github.com is
unreachable; a report is still attempted through gh`、`not signed in (...)` 或 `not available
(gh is not installed)`。

`log` 是这个回路的另一半。对每份已提交报告，它读取 issue 并打印 `open`、`resolved`、
`closed as not planned`、`unknown` 或 `prefilled page only`；旁边给出 issue 编号、kind、command、
版本、报告日期、关闭或更新时间、标签和链接。`--json` 同样给出 `github`、`summary`（本地总数加上
`open`、`resolved`、`not_planned`、`unknown`）和逐条的 `status`、`reason` 与 GitHub `issue`
对象（`number`、`state`、`state_reason`、`title`、`labels`、`updated_at`、`closed_at`、`url`）。
人读视图最多列最近 20 条，`summary` 统计整个账本。没有 gh 凭据时本地记录照常打印，每份已提交报告
说明状态未知的原因，不猜。

报告永远不带路径、日志行、清单字段、命令行、worktree 身份或环境变量：它说的是引擎，不是它跑在
哪个项目里。`--dry-run` 打印确切的标题和正文，什么都不发。

## 显示与欢迎页细节

无参数运行 `oberth` 显示首屏：品牌标记与字标、一句定位、本机版本、一次工作会话起步的三条命令
（status / up / down）、接着可能会用到的可选能力（`attn` 与 `feedback report`，单独成组，免得读成
必需）和到完整帮助的指引；它是指路牌不是手册，分组命令清单只在 `oberth --help`。版本行在发布版本检查
成功时带 `(latest)` 或 `(latest version vX.Y.Z)`；检查未知时只打印本机版本，不把“没查到”写成
“最新”。发现更新时，首屏底部给出对应平台的安装命令和 `oberth runtime service stop`。还没有记录过改进报告
的决定时，首屏也可能问一次是否发送关于 oberth 自身的脱敏报告；这个问题和报告携带什么，写在
基础设施命令一节的 `oberth feedback` 里。标记是 `engine/internal/cmd/mark_gen.go` 里的静态生成资产，不手写；字标是轮廓字，终端里用粗体文字表示。

标记按品牌自己的分工上两色：两条轨用墨色（深色终端用纸色），胶囊用陶土色。涂成一色时
胶囊会和两个轨帽并成三个一样的块，看起来像一张脸，所以这个分工是语义的一部分，不是配色
偏好。truecolor 下用 `#23262B` / `#F8F6F2` 与 `#A8543E` / `#DB9378`，`COLORFGBG` 报告
深色背景时取后一组；256 色终端取最接近的槽位（235 / 255 / 131 / 174）；8 色终端下轨保持
终端自身前景色，胶囊退化为 yellow。`NO_COLOR` 下不着色。
