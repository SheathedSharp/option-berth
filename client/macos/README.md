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

设置窗口提供客户端外观偏好：可切换冰川、午夜、纸张和森林主题，自定义强调色，选择已安装的
界面与数据字体，并分别调整界面、数据和日志字号。偏好实时生效并保存在本机；工作区一栏仍只
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
05-connection-error、06-manifest-review、07-settings、07-settings-jev、07-settings-typography 、08-code、09-terminal 和 10-agent-console。改界面后必须运行 mage states
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
`⌘T` 打开终端页。结束按钮只向该会话的直接子进程发送 SIGHUP，等待退出回执后才允许关闭；
退出应用前必须先结束活动会话。脱离终端的子进程需用户另行管理，项目服务仍通过 `oberth down` 停止。

SwiftTerm 固定版本负责终端仿真与 PTY；构建需要 Swift 6+，首次构建需要下载依赖。
复制、粘贴、选择保留原生按键；OSC 52 剪贴板读写默认拒绝，终端链接需确认且仅允许 HTTP(S)。
滚动历史保存在进程内，不自动保存或上传终端输出。原生终端不是操作系统安全沙箱。

## Coding agent 会话 / Agent sessions

在终端页切换 **Terminal / Agent session**。Terminal 只显示 shell 会话，Agent session
只显示 agent 会话；两者都固定归属于创建时选择的 worktree。切换页面不会结束原生进程，
也不会把会话迁移到另一个项目。草稿输入不跨页面持久保存。

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
submitted. Composer shortcuts are not installed in native terminals. Draft text is not persisted across pages.
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
