# 工作台 / Console

工作台是当前 worktree 的原生会话工作区，不是两个名为 Terminal / Agent session 的独立页面。

## 交互决定

- Shell 与 coding-agent 会话共用一个 terminal 主画布和同一 PTY registry。左侧窄 rail 只负责跳转；选择会话只是切换既有 PTY 的呈现与焦点，不重新创建进程。
- rail 按 Shell / Agent 分组显示运行态；⌘⌥Tab 打开当前 worktree 的非模态会话切换器，⌘Tab / ⇧⌘Tab 在既有会话间循环。切换器支持名称、provider、状态和路径检索。
- 当焦点在 agent session 时，Esc 返回进入该 agent 前记住的 Shell；没有 Shell 时只关闭切换器。快捷键只绑定当前工作区主窗口，不拦截独立窗口或 terminal 已选中的普通 Esc。
- 新建 Shell 是明确动作；新建 Agent 使用可收起的输入区。输入区明确表示“新会话”，后续交流与审批仍在原生 agent 内进行，不把启动参数伪装成续聊。
- 保留当前混合分屏，选中已有窗格只移动焦点；选择不在分屏中的会话替换当前焦点窗格，不广播输入。
- 去除长篇空态说明。命令块、历史、续接等选项就地提供，安全说明放到帮助/提示。
- 原生焦点由实际挂载的终端视图处理一次性请求，不能在每次状态刷新时夺走文本编辑焦点。
- 离开 worktree 后取消尚未完成的启动计划；已启动会话仍由唯一 PTY registry 持有。路径、draft、输出不跨 worktree 迁移。

## 参考与取舍

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
