# option-berth · 原生客户端（macOS）

客户端围绕一个 worktree 呈现服务、Git、终端与外部 coding agent 会话，
并提供启动、停止、日志和清单编辑入口。
左栏只列出已经发现 oberth.yaml 的项目；监听情况跟在服务运行实况里，不是整机端口列表。
产品边界和 CLI 契约见 ../../docs/product.md 与 ../../docs/cli.md。

## 构建和运行

在仓库根目录：

~~~sh
mage build          # 引擎、客户端和 Jev adapter
mage buildClient    # 只构建客户端
mage runApp        # 安装到 /Applications 后打开同一份 App
mage runCLI "version --json"  # 运行刚构建的 CLI
mage runDaemon     # 用刚构建的 CLI 启动后台采集器
mage stop           # 停止 daemon
mage install        # 安装 CLI、App，并把 ~/.local/bin 写进 PATH
~~~

开发构建和运行统一由 Mage 管理。

只改客户端时：

~~~sh
cd client/macos
VERSION=0.1.0 ./build.sh
./build.sh --run
~~~

窗口关闭不会停止 daemon。daemon 的 socket、数据库和日志都在
~/.option-berth/；oberth daemon path 会打印当前路径。

## 界面

左栏显示有清单的项目。选中项目后，主区显示：

- 清单声明的服务、依赖和每个服务的运行状态；
- 当前 worktree 的 Git 分支、脏文件计数、变更文件和只读 diff；
- 最近退出码和日志入口；
- 服务对应的运行实况，以及清单未声明的监听；
- 启动全部、停止全部、编辑清单和移除项目等动作。

Paper（纸张）是唯一内置默认。设置 → 外观通过内置原生编辑器打开 `theme.json`、`settings.json` 和 `keybindings.json`，
覆盖语义颜色、已安装字体、模块字号、独立面板宽度和会话默认值；支持注释与尾逗号，有效编辑自动保存并热更新，坏编辑保留草稿与上次有效值。
配置位于 `$BERTH_HOME/config/`（默认 `~/.option-berth/config/`），不读项目内同名文件，打开不改写文件；编辑遇到外部变化会暂停保存并提示冲突。
完整字段、Schema 和迁移规则见 [个性化配置](../../docs/client-configuration.md)。工作区一栏仍只
显示产品路径和连接信息。Jev 增强一栏可以启用 agent-side Jev、保存或清除 OpenRouter key，
配置写入 `~/.option-berth/jev.json`，供任务级 `jev-attention --session` 读取。客户端不维护模型目录、daemon
判断结果或另一套事实模型。新建项目的清单审阅框可以调用本机 agent 起草，并显示它正在检查的
阶段和最近动作；结果回填后仍由人确认写入。

## 无头验证

客户端提供四个验证入口：

~~~sh
# 真 daemon 的数据自检，不打开窗口
./build/OptionBerth.app/Contents/MacOS/OptionBerth --probe

# 当前机器的实况截图
mage snapshot

# 冻结数据的全部界面状态
mage states

# 真实窗口几何和截图
mage window
~~~

截图前先执行 `mage client` 或 `mage buildClient`；截图目标不再隐式构建。

mage states 生成 01-empty、02-services-live、03-services-idle、04-runtime-facts、
05-connection-error、06-manifest-review、07-settings、07-settings-jev、07-settings-typography 、08-code、09-terminal 、10-agent-console 和 11-console-split。改界面后必须运行 mage states
并查看 PNG；需要验证真实 daemon 数据时再运行 mage snapshot。mage window 用于检查标题栏、
弹窗和窗口尺寸等离屏渲染看不到的部分。

也可以直接指定状态或尺寸：

~~~sh
./build/OptionBerth.app/Contents/MacOS/OptionBerth \
  --render-states .cache/states --size 900x640
./build/OptionBerth.app/Contents/MacOS/OptionBerth \
  --snapshot .cache/board.png --scope services:demo
./build/OptionBerth.app/Contents/MacOS/OptionBerth \
  --dump-window .cache/window.png --size 720x452
~~~

--scope 接受 services:NAME、code:NAME 或兼容的 project:NAME。--probe 的 BERTH_PROBE_START 和
BERTH_PROBE_ADOPT 仅在明确需要端到端起停或采纳清单时设置；它们会操作真实项目。
不要把真实用户的账本用于截图，端到端验证使用隔离的 BERTH_HOME。

## 数据来源

DaemonClient 通过 `state.subscribe` 长连接接收首帧快照和端口 / 项目增量；显式刷新仍读取
`state.snapshot`。其它调用是 groups.config.get、groups.start、groups.kill 和 ports.logs。
Git 面板调用 `oberth git --json`、`oberth git files --json` 和按需的
`oberth git diff --json`，复用 CLI 契约，不新增 daemon Git 状态。ServicesStore 和
GitStore 只缓存当前界面需要的数据；它们不另建产品模型。引擎协议变更后，先更新 daemon
schema，再重新构建客户端。

## 视觉检查清单

- 端口号按标识符显示，不使用千位分隔符。
- 服务空态、未运行、连接失败、清单预览和设置窗口都有明确的下一步动作。
- ImageRenderer 看不到 ScrollView 和独立窗口；冻结状态与真实窗口各自验证。
- 弹窗打开时用 mage window 检查其实际尺寸和位置。

## 原生终端 / Native terminal

终端页按 worktree 保留独立 PTY，点击新建才启动 shell；切换项目不会改变已有会话。
`⌘3` 打开终端页。结束按钮只向该会话的直接子进程发送 SIGHUP，等待退出回执后才允许关闭；
退出应用前必须先结束活动会话。脱离终端的子进程需用户另行管理，项目服务仍通过 `oberth down` 停止。

SwiftTerm 固定版本负责终端仿真与 PTY；构建需要 Swift 6+，首次构建需要下载依赖。
复制、粘贴、选择保留原生按键；OSC 52 剪贴板读写默认拒绝，终端链接需确认且仅允许 HTTP(S)。
滚动历史保存在进程内，不自动保存或上传终端输出。原生终端不是操作系统安全沙箱。

## Coding agent 会话 / Agent sessions

在终端页切换 **Terminal / Agent session**。Terminal 只显示 shell 会话，Agent session
只显示 agent 会话；两者都固定归属于创建时选择的 worktree。切换页面不会结束原生进程，
也不会把会话迁移到另一个项目。草稿仅在内存中保留，切换页面不会丢失；布局和 provider 选择可经用户同意保存，重启后先确认恢复，不自动恢复进程。

先自行安装并登录所需的 OpenCode、Codex、Claude Code、DeepSeek Harness 或 Pi。
客户端使用同一份 `oberth agent list/plan` 契约；未安装会标记 Missing，不自动安装或登录。
旧引擎缺少这些命令时会报错，不会回退拼接 shell。需要与客户端匹配的 oberth 版本；
安装/替换引擎仍由用户明确执行。

在消息框内，**⌘Enter** 用该消息建立一个新 agent 会话；**⇧⌘Enter** 将焦点交给当前活动的
原生会话，没有可继续的原生会话时才新建。普通 Enter 插入换行。只有消息框处理这些快捷键，
中文输入法组合文字时不会误提交；进入原生终端后，输入和审批交给 agent 自己。
这不是第二套聊天协议：后续对话直接在原生界面继续，再次点击「发送到新会话」会另建会话。
不猜测 TUI 屏幕、不向未知输入状态模拟粘贴、不按“最近一次对话”跨 worktree 恢复。

DeepSeek 的消息入口运行 `dsh --profile headless`；原生入口要求用户已有 `tui` profile。
两者是不同模式，不承诺同一对话的 headless/TUI 无缝切换。客户端不创建 profile，
不存储模型凭证，不新增工具调用循环，也不添加跳过审批或禁用沙箱的参数。
worktree 是启动归属，不是限制 agent 文件权限的操作系统沙箱。

启动计划的消息与输出临时文件使用私有目录/权限，计划进程退出后清理；不持久保存会话记录，
不上传输出，不把错误输出中的原始消息显示出来。取消只针对计划进程并等待实际退出，
不是对忽略退出信号的故障程序承诺硬超时。agent 启动后的结束操作仍遵守上面的 PTY 规则。
外部 agent 自身的日志、会话存储和文件改动遵循它自己的配置。

**English.** Switch between Terminal and Agent session in the workspace console. Each process keeps
its initial worktree. Install/authenticate your coding agent yourself; Refresh only discovers executables.
In the message composer, Cmd+Enter starts a new session with a literal initial prompt. Shift+Cmd+Enter
focuses the selected live native session, or opens a new native session when none is available.
Continue subsequent turns in the agent's own terminal. Return inserts a newline; marked IME text is not
submitted. Composer shortcuts are not installed in native terminals. Drafts remain in memory only; layout and provider metadata can be saved with explicit consent and reviewed after restart, without automatically launching processes.
DeepSeek uses headless for messages and requires an existing tui profile for native mode; switching
between them does not resume the same conversation. No agent reasoning loop, credential store,
automatic installation, approval bypass, shell prompt interpolation, or implicit cross-worktree resume is added.
A worktree is not an OS sandbox; external agents retain their own permissions, storage and side effects.

无需模型账号的原生回归 / Native checks without model credentials:

```sh
swift run --package-path client/macos --force-resolved-versions TerminalChecks
swift run --package-path client/macos --force-resolved-versions AgentChecks
# 对接实际兼容版本的 oberth；外部 provider 仍使用临时测试替身，不调用付费模型
BERTH_AGENT_TEST_BINARY="$PWD/bin/oberth" \
  swift run --package-path client/macos --force-resolved-versions AgentChecks
```

AgentChecks 的默认路径使用合成 CLI 协议；显式二进制路径验证实际 CLI→计划→PTY 链路。
两种路径均检查真实窗口按键、组合文字保护、原生编辑/撤销、字面参数、会话连续性、
错误边界与取消。不把这些回归等同于已登录的五个 provider、完整输入法或长时间交互验收。

## 会话工作区 / Session workspace

**⇧⌘O** 打开全局会话管理。可以按名称、worktree、agent 类型搜索，重命名并打开、结束或关闭
本应用拥有的会话。即使原项目清单被移除，活动 PTY 仍可从这里找到；未关联清单的会话页明确
提示没有项目服务事实，不把会话存在冒充服务正常。移除项目会丢弃该项目的输入草稿与导航状态，
但不会静默杀掉终端进程。关闭已退出会话才释放会话名额（总共最多 16 个）。

每个窗格的分屏菜单可呈现同 worktree 的已有会话，并支持左右/上下递归拆分。标题拖放可
重排；隐藏窗格不结束会话，结束则仍需确认真实退出。每个会话只有一个 PTY，独立窗口与
工作区之间移动的是其呈现权，不复制进程、不广播输入。具体操作与上限见下方“动作、布局与窗口”。

进入 **原生 / Native** 时可收起消息框；后续对话仍由外部 agent 处理。计划读取进度/取消和
错误提示位于消息框之外，收起后也可访问。**⌘F** 搜索当前焦点终端，**⌥⌘G** 打开只读 Git。
标准 Edit/Window/App 菜单保留，复制、粘贴、全选和撤销仍走原生响应链。

**English.** Shift+Cmd+O opens session management, including sessions whose manifest was removed.
Recursive same-worktree panes can be split horizontally/vertically or moved to a detached window.
There is one native PTY per session, not one process per view. Closing a detached window returns its
presentation to the workspace; ending a session still requires observing its exit. Cmd+1…9 selects
the visible worktree; Option+Cmd+S/G/T selects services/Git/Console. Standard native editing and window actions remain available.

冻结图与原生 UI 共用布局模型，但用静态 H/V 栈绘制分隔，去掉 ImageRenderer 无法捕获的
原生菜单/拖放宿主；这不等于原生窗口或终端输出截图。TerminalChecks / ClientChecks 另用
实际 NSWindow、NSSplitView 和 PTY 验证查找、输入、焦点、窗口所有权与进程退出。

## 有限 CLI 读取的取消边界

只读 Git 请求默认 30 秒、清单草稿默认 300 秒后开始取消。执行器在专属进程组中启动
直接子进程，非阻塞排空 stdout/stderr；保留 8 MiB 输出、256 KiB 错误和 1 MiB 进度行上限。
取消、超限、读取失败与超时都不是成功。TERM 后仍存活则升级 KILL，并观察退出、核对原
进程组内的存活后代，最后 reap 直接子进程；没有延迟发信号或遗留读取 worker。

子进程身份在信号阶段通过 `waitid(WNOWAIT)` 保留，不对已回收 PID 追加信号。即使直接
子进程自行换组，仍可按保留的直接身份取消。主动脱离原组的后代不按猜测 PID 追杀；如果
它继续持有管道，命令返回失败并关闭应用的读端，而不是无限等待 EOF。

这些时限是**开始取消的截止时间**，不是操作系统不可杀状态、启动系统调用或用户回调的
硬实时保证；stream 回调必须及时返回。该执行器不接管 daemon、用户已有服务或终端 PTY。
实现依据为 POSIX 的 `posix_spawn` 进程组属性与 `waitid` 的 WNOWAIT 语义，未引入外部实现。

### 请求与界面生命周期

切换 worktree、移除选中项目或销毁 GitStore 会取消旧项目读取；切换 diff 文件会取消旧
文件读取。队列仍只有一个运行项和每类最多一个待办项，旧回执由请求代际检查拦截。

草稿窗口的“取消”和视图消失会取消当前 CLI。`done` 进度帧仅代表候选 YAML 已到达，
此时仍禁用写入并显示等待命令结束；只有同一请求的 CLI 成功退出才回填编辑器。
候选后的失败、空草稿、错误后零退出、窗口关闭后的回执和上轮结果都不会覆盖当前内容。
清单仍由用户确认写入，不新增提示词/对话存储，也不声称应用崩溃后能恢复进程。


## 可选命令块（zsh）

在 Terminal 工具栏勾选“命令块”后，新建的 zsh 会话使用私有临时启动目录加载原有
`.zshenv` / `.zshrc`，再安装仅属于该次 shell 的 preexec/precmd 回调；不改写用户配置。
退出后删除临时目录。其他 shell 或外部 agent 不自动注入集成。

命令、开始与退出状态来自带会话随机标记的 OSC 帧，而不是从屏幕猜测提示符。每个会话
最多保留 256 个命令块，解析帧上限 16 KiB；缺失/错误退出码显示未知，不算成功。
历史菜单只复制命令，不执行它；可以清空内存历史。命令可能包含敏感内容，因此默认关闭，
不写磁盘、不上传，也不把命令块用于服务归属或权限判断。

标记用于隔离正常的远端提示和其他会话，不是对同一 shell 内恶意程序的安全隔离。
同一 worktree 的跨会话搜索从“搜索历史”打开；Bash/Fish、复杂提示插件与块级输出定位仍需后续实现/验收。
`swift run --package-path client/macos --force-resolved-versions WorkspaceChecks` 覆盖分片
OSC、UTF-8、历史/帧上限、未知退出码以及隔离的真实 zsh PTY 起止与原配置保留。


## 动作、布局与窗口

`⇧⌘P` 打开可搜索命令面板；输入关键词后 Return 执行选中项，上下键选择，Escape 关闭。
同一动作目录生成菜单与面板。默认 `⌘N` 接入项目，`⌘1…9` 选择当前可见 Worktrees 并保留模块，
`⌥⌘S/G/T` 切换服务/Git/工作台，`⇧⌘O` 打开会话，`⌘F` 查找，`⌘R` 刷新，`⌘B` 切换侧栏，`⌘,` 打开设置，`⇧⌥⌘O` 打开恢复，`⌥⌘U` 检查更新。面板中的“快捷键”可修改
工作区动作按键；文件存在时改由 `keybindings.json` 管理，GUI 不覆盖它。重复按键、原生编辑/退出/关闭窗口快捷键
和固定 `⇧⌘P` 被拒绝。菜单、命令面板与原生终端均保护组合文本；目录面板和独立终端窗口不会误切工作区。

每个窗格可将同 worktree 的已有会话左右或上下分屏；嵌套布局最多 16 个会话，输入不广播。
拖动原生分隔条调整尺寸；拖动窗格标题到另一个窗格可左右重排。拖入未知/跨 worktree
会话 ID 不会启动进程或修改布局。收起窗格保留会话；停止按钮仍要求确认并观察真实退出。

“独立窗口”移动的是同一个 PTY 的呈现，不复制进程。独立窗口关闭或选择“返回工作区”
只归还呈现权，不终止会话；嵌入视图刷新不能把终端从独立窗口抢回。应用退出仍检查所有
活动会话，包括独立窗口中的会话。恢复布局不意味着恢复原进程，见恢复功能说明。

`WorkspaceChecks` 验证有界平面布局树、递归拆分/移动/删除、环与重复引用拒绝；
`ClientChecks` 在原生 NSWindow 中验证面板查询与 Return、独立窗口往返、三个真实 PTY 的
递归水平/垂直分屏、焦点隔离与可移动分隔条。它通过独立 BerthClient 模块编译实际 UI，
无需 Xcode 的 XCTest，不另建模拟客户端。


## 重启恢复：布局、历史与进程分开处理

“视图 → 工作区恢复”默认关闭。明确允许后，客户端在 `$BERTH_HOME/client-recovery/`
（默认 `~/.option-berth/client-recovery/`）保存一个版本化布局快照：目录0700、文件0600，
原子替换，最多512 KiB。内容仅有工作目录、会话名称/类型、provider选择和窗格结构。
消息草稿、命令块、终端输出、进程PID/运行状态、环境和凭证不进入文件。

重启后先展示待确认恢复项；确认前不会把上次快照覆盖为空工作区。“恢复布局”恢复的是
导航/窗格引用和历史会话入口，不自动启动任何 shell/agent，不把旧进程标为存活。
“新建替代 Shell”需再次确认，启动新进程后把旧布局引用映射到新会话ID；不是按旧PID附着。
外部 agent 的对话续接仍须明确选择原始会话文件。路径失效、版本未知、重复/环引用和不安全
文件类型/权限都拒绝，不以空白成功掩盖错误。关闭恢复时可删除保存的元数据。

支持层次：布局结构和历史**入口**已可恢复；没有保存终端输出/对话历史，没有旧PTY复活或
崩溃后后台进程接管。分隔条的精确像素位置目前不保存。已存在当前会话时拒绝覆盖恢复布局。
`WorkspaceChecks` 验证存储边界；`ClientChecks` 验证用户同意、待确认文件不被覆盖、草稿不落盘、
没有隐式进程恢复、显式新建与引用映射，以及关闭后删除。所有夹具均在隔离目录运行。


## 当前 macOS 工作区界面

顶部工具栏始终提供命令面板、会话管理、恢复和更多操作；更多菜单包含检查更新与设置。
左栏可按项目名/分支筛选 worktree。切换 worktree 保留当前服务/Git/终端页，不把会话移动到
其他代码目录。项目标题区分名称、分支、路径和服务数量；清单与项目级动作集中在项目菜单。
终端窗格只保留一套标题/焦点/状态/查找/分屏/独立窗口/结束入口，移除旧双栏状态与重复控制。

Agent 页的“续接…”选择原始会话文件，复用 `oberth agent plan --resume-file` 的身份校验；
旧引擎未声明该能力或 provider 不支持时禁用入口，不猜测最近对话。续接后显示会话标识摘要，
输入、登录和审批保留在原生界面；点击新消息仍创建新会话。恢复页的历史 agent 入口只导航，
对话恢复仍须在 Agent 页选择原始文件。

“检查更新”仅在明确打开后读取官方仓库公开发布元数据。它不会上传工作目录、自动下载/安装、
替换二进制或重启服务。发布页面 URL 由仓库与合法版本组成，不接受响应里的任意跳转地址；
结果受读取大小/时限约束。安装包名和 SHA256SUMS 的存在，不等于已经下载或验证签名/校验和。

界面沿用现有主题、字体与动效令牌，深色主题同步 SwiftUI/AppKit 颜色方案，不修改用户字体
设置或随附字体。冻结渲染使用独立默认配置且不启动 daemon/agent，不读取个人主题偏好：

```sh
# 已构建后执行；冻结截图不会隐式构建
client/macos/build/OptionBerth.app/Contents/MacOS/OptionBerth \
  --render-states /tmp/oberth-states --size 1060x720
client/macos/build/OptionBerth.app/Contents/MacOS/OptionBerth \
  --render-states /tmp/oberth-dark --render-theme-file client/macos/Fixtures/theme-dark.json --size 900x640
```

默认窗口为 1060×720，最小为760×520。原生交互检查使用隔离 HOME/CFFIXED_USER_HOME/BERTH_HOME；
发布检查包含 ClientChecks、PersonalizationChecks、CommandChecks、GitReviewChecks 和 ConsoleChecks，
在同一候选提交上验证菜单/输入法、独立详情宽度、文件热更新、统一工作台和终端窗口合成；测试不会读取真实 provider 账号。全部输入法、系统辅助功能和
长期资源趋势仍属于单独验收范围，不由冻结截图代替。


## 首次使用指引 / Getting started

首次打开工作区自动展示原生聚光导览：真实控件锚点、遮罩挖空、就近弹窗和中英说明，
不是单独的文字 sheet，也不是套在截图上的假按钮。左栏接入、项目列表、已存在的页签和
工具栏分步骤说明；未出现的目标不会悬空高亮。窗口缩放会重新计算位置。

展示资格使用稳定的本地 `workspaceTourPresented` 偏好，出现时立即记录。
跳过、关闭窗口、重启和升级都不自动重播。已有旧 TipKit 元数据的用户保守迁移为已使用，
不再打开 TipKit 数据库；空态、“更多”和 Help 菜单仍可显式重看。

原生按钮支持继续、返回、跳过、完成，Return/Escape 和左右方向键；Tab 焦点留在导览内，
关闭后只恢复同一窗口仍存活的编辑控件。底层工作区禁用，遮罩挖空也不会让点击穿透。
大号界面字体只滚动说明区，操作按钮保持可见。Reduce Motion 禁用定位动画。导览不会启动 shell/Agent、写清单或打开项目选择器；
看完后再通过真实入口操作，仍须逐项确认。详见 [首次使用](../../docs/macos-guide.md)。

`ClientChecks` 使用独立 HOME/BERTH_HOME，验证跨真实进程的首次资格持久化、旧用户迁移、
原生鼠标/键盘/跳过/重看、焦点归属、760×520 缩放与已接入项目锚点，保存 paper/midnight
实际测试窗口图。冻结导览图补充布局审查，不代替原生交互。完整 VoiceOver/输入法验收仍属
独立范围；Reduce Motion 的分支受测，但测试不会改动用户系统辅助功能设置。

**English.** A native spotlight tour opens once over the real workspace. Its presentation is remembered
immediately; skipping, reopening or upgrading does not replay it. More and Help remain explicit replay
entries. The tour follows visible controls, blocks click-through and keeps keyboard focus inside it.
Reading never launches tools, writes a manifest, installs software or certifies readiness.

## 跨会话历史的设计边界

[Warp 命令历史](https://docs.warp.dev/terminal/entry/command-history/) 提供搜索交互参考，
本项目不复制实现、不同步云端、不扫描 shell 历史文件。跨会话搜索由 #48 跟踪：
数据只投影同一 worktree 的已有终端命令块，必须由用户事先启用；Agent 会话不参加。
只复制命令，不执行或向原生输入框模拟粘贴。未知退出码不能显示成功。

每会话最多256块、整个应用最多16个会话；退出的会话在被关闭前仍有内存历史，
关闭会话或退出应用则不再保留。清空必须确认，且不能清理其他 worktree。
这不等于输出历史恢复、Bash/Fish支持或完整shell交互验收。

**English.** Worktree history is a read-only projection of opted-in terminal command blocks,
not a second history database. It excludes other worktrees and agents. Copy is never execute.
Closing a session or quitting the app discards its in-memory history.
