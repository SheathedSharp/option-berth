# 工作台 / Console

工作台是当前 worktree 的原生会话工作区，不是两个名为 Terminal / Agent session 的独立页面。

## 交互决定

- Shell 与 coding-agent 会话共用一个 terminal 主画布和同一 PTY registry。左侧窄 rail 只负责跳转；选择会话只是切换既有 PTY 的呈现与焦点，不重新创建进程。
- rail 按 Shell → Agent 排列并显示运行态；⌘⌥Tab 打开当前 worktree 的非模态会话切换器，⌘Tab / ⇧⌘Tab 在既有会话间循环。切换器按 Shell / Agent 分组，并支持名称、provider、状态和路径检索。
- 当焦点在 agent session 时，Esc 或「返回 Shell」切回进入该 agent 前记住的 Shell；还没有 Shell 时补一个，不向 Agent 发信号。Agent 留在会话栏，工具栏上的同一会话可以再次进入。启动输入打开时，Esc 只关闭输入并留在 Shell。快捷键只绑定当前工作区主窗口，不拦截独立窗口或 terminal 已选中的普通 Esc。投递只有一条：该窗口里实际挂载的视图在 `NSApp.sendEvent` 上安装本地监视器，用同一识别函数处理 ⌘⌥Tab、⌘Tab / ⇧⌘Tab、⌘↩ 和 Esc。同一视图的 `performKeyEquivalent` 是唯一后备，只接监视器没有消费的事件。`windowNumber == 0` 不代表窗口，也不再单独放行。⌘↩ 只在 Shell 且启动输入未打开时开始 Agent；文本编辑里的 ⌘↩ 仍交给那个编辑器。
- 打开 worktree 会话时，若该目录还没有 Shell，并且目录真实存在，界面启动一个交互式 Shell 并聚焦它。这是默认界面，不是 Agent 输入页。目录不存在或会话已满时不留下半启动进程。用户已经正在查看的 Agent 保持在前台，Shell 在其下准备好。
- 从 Shell 用「Agent」或 ⌘↩ 打开启动输入。输入区明确表示“新会话”，后续交流与审批仍在原生 agent 内进行，不把启动参数伪装成续聊。再开一个 Shell 仍是「新会话」里的明确动作。
- 保留当前混合分屏，选中已有窗格只移动焦点；选择不在分屏中的会话替换当前焦点窗格，不广播输入。
- 去除长篇空态说明。命令块、历史、续接等选项就地提供，安全说明放到帮助/提示。
- 原生焦点由实际挂载的终端视图处理一次性请求，不能在每次状态刷新时夺走文本编辑焦点。
- 离开 worktree 后取消尚未完成的启动计划；已启动会话仍由唯一 PTY registry 持有。路径、draft、输出不跨 worktree 迁移。

## 参考与取舍

Warp 的 Terminal / Agent 两种模式（终端是打开标签后的默认界面，⌘↩ 从终端进入 agent 对话，Esc 回到终端且对话仍可再进入）是交互参照，不是要接进 Warp 的模型。这里的 Agent 仍是外部 CLI 的独立 PTY：离开只切换呈现，进程留在同一 registry，再次进入的是同一个会话。

阅读 Warp 在 b865631c9a0e46b548c7ec7dc32e228a148171d1 的
`app/src/terminal/input/universal.rs`、`terminal.rs`：借鉴上下文、输入和焦点集中在一个工作流的组织方式，
独立实现客户端 UI，不复制其 AI 服务、工具循环、自动语言判断或账户系统。

https://github.com/warpdotdev/warp/blob/b865631c9a0e46b548c7ec7dc32e228a148171d1/app/src/terminal/input/universal.rs
https://github.com/warpdotdev/warp/blob/b865631c9a0e46b548c7ec7dc32e228a148171d1/app/src/terminal/input/terminal.rs

现有原生模拟器、CLI agent plan、显式续接校验与会话 registry 继续复用，不造第二份运行事实。
恢复文件中的旧布局字段是兼容格式，不意味着界面继续保留两套模式页面。

## 验收

必须检查真实混合 PTY 的切换、进程身份、cwd、独立键入、焦点、分屏、独立窗口返回、草稿与取消归属。
冻结图只验证排版；原生窗口测试另外执行，不将截图冒充交互验收。进度见 issue #95 和对应 PR。旧布局/恢复字段继续兼容，session manager sheet 仍作为显式全局兜底入口。
