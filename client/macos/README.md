# option-berth · 原生客户端（macOS）

客户端只呈现一个 worktree 的清单和运行状态，并提供启动、停止、日志和清单编辑入口。
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

mage states 生成 01-empty、02-services-live、03-services-idle、04-runtime-facts、
05-connection-error、06-manifest-review、07-settings、07-settings-jev、07-settings-typography 和 08-code。改界面后必须运行 mage states
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
