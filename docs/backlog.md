# 当前能力与待办

以 [产品定义](product.md) 和 [支持边界](architecture.md) 为准。主线是给 coding agent 的能力；
下面客户端那一节记的是**呈现层**的进度，它不定义产品，也不该反过来决定引擎做什么。
终端能启动外部 agent，不等于已经具备完整终端工作台或全部 provider 会话协议；
CI 通过也不等于所有日常交互已验收。

## 呈现层：macOS 工作区

服务声明、观察、归属、生命周期和持久化共用引擎事实链。macOS 工作区在同一 worktree 下
提供服务、只读 Git 和统一工作台。打开 worktree 会话默认进入该目录的交互式 Shell。底部**单输入条**：↩ 送 Shell；⌘↩ / ⇧⌘↩ 送 Agent（`stream: true` 的 provider 默认 `oberth agent stream` 结构化桥，否则原生 PTY）。右侧 Agent 栏展示结构化轮次/审批或终端会话事实。详见 [console.md](console.md)。DeepSeek 无结构化桥，仍走原生/TUI 入口。

会话管理可搜索、重命名、打开和结束本应用拥有的会话，项目清单被移除也不丢失入口。
草稿按 worktree 在内存中保留；布局/provider 元数据可经用户同意保存并在重启后确认恢复。
同一 worktree 的已有会话支持有界递归分屏与独立窗口呈现；每个会话只有一个 PTY，输入不广播。
原生模式可收起消息框；取消与错误入口仍可访问。顶部工具栏统一呈现命令、会话、恢复与更新入口。

这些是**应用内的 UI 状态与 PTY 归属**，不是新的服务账本、对话存储或 agent 推理系统。

有限 CLI 读取已有独立进程组、取消时限、双流结束和真实回收验证。切换 worktree/文件会
取消失效请求；草稿候选只有在命令成功退出后才回填，关闭窗口后不接收旧结果。完整边界见
[客户端说明](../client/macos/README.md#有限-cli-读取的取消边界)，回归入口见 [验证范围](validation.md)。

Paper 是唯一内置默认；主题、快捷键、新会话默认值与各模块宽度已文件化，错误编辑保留有效值。
Cmd+N 接入项目，Cmd+1…9 按可见 Worktrees 导航，模块采用 Option+Cmd+S/G/T。Git 提供冲突/暂存/工作区/
未跟踪筛选、重命名路径检索、上游和同仓库上下文；没有 CLI 事实支撑的历史 DAG 不画占位图。

## 后续能力

引擎侧与呈现层分开记：前者是产品主线，后者是它的呈现。

| 优先级 | 能力 | 当前缺口与完成条件 |
| --- | --- | --- |
| 高 | agent 侧的能力面是否够用 | 主线是给 coding agent 的能力，所以「agent 拿到这份代码能不能自己跑起来、自己判断为什么没跑起来」优先于任何界面。**2026-10-28 已跑过一轮**：真实 Claude Code 在陌生 worktree 上只靠 CLI 与 skill 完成了「起服务、失败、看日志、修、再起」，核心闭环 6 步走通（输入与结果见 [validation.md](validation.md)「核心验收」）。卡点见下面四条。仍缺：Codex / OpenCode / Pi 与多 worktree 并发场景。 |
| 高 | `up` 的失败提示对「启动前拒绝」是错的 | 拒绝类错误（worktree 在 `$HOME` 之外、cwd 不可用或缺失）发生在 spawn 之前，没有日志；但 `groupstart` 的失败分支丢弃 RPC 自带的 hint，无条件换成 `oberth logs <svc> --once`，`reason` 也落到 `start_failed`，读起来像「启动后失败」。完成条件：保留 RPC 的 hint，pre-spawn 拒绝不发日志提示并给对应的 reason。 |
| 高 | 未监听的 `machine:` 依赖无条件升级成 attention 交接 | 只要声明的 machine 条目没在听就发 `machine_unavailable`，不看有没有服务依赖它；而 `up` / `down` 永不操作 machine 条目，于是交接给 agent 的是它无权也无从处理的事，两轮分别花了 8 步和 5 步以上。完成条件：没有 `depends_on` 指向它时不发布交接，或降级成 `status` 里的一行。 |
| 高 | skill 的安装与更新没有路径 | 仓库里的 skill 是 v0.4.0（含 `workplace.md`），本机装的那份是 v0.3.1，缺整个 `workplace` 参考；两份同名，哪份生效不确定（两轮分别读到旧版和新版）。README 只说「不会自动安装 skill」，没有安装或更新说明。完成条件：给出安装与更新路径，或让已装的那份能看出自己落后于哪个版本。 |
| 中 | attention 的 task context 没有出处 | skill 写死 `--task-context /tmp/oberth-task-context.json`，但没有任何东西创建它，agent 得自己猜内容和位置；Jev 配置也有 `$BERTH_HOME/jev.json` 与 `~/.option-berth/jev.json` 两处，agent 两边都找过。完成条件：说清这个文件由谁写、放哪，或让 runner 在没有它时给出可读的默认。 |
| 高 | 客户端与引擎是否拆成两个产品 | 现在两者在 CI、release 脚本、品牌、文档与验证矩阵里绑在一起（17 个 workflow 里约一半是客户端/品牌/site）。未决定。做之前先出拆分清单与代价，别顺手拆。 |

### 呈现层（macOS 工作区）

这些是客户端进度，不定义产品，也不该反过来决定引擎做什么。

| 优先级 | 能力 | 当前缺口与完成条件 |
| --- | --- | --- |
| 高 | 客户端接入引擎的工作记录 | 引擎已有 `workplace`：worktree 下的工作记录，`oberth start` 与 `oberth agent stream` 自动写入，agent 或前端可经 `oberth workplace record` 写入（契约见 [cli.md](cli.md)）。客户端仍把块流存在本机 `~/.option-berth/workplaces/` JSON，是引擎之外的第二份状态。完成条件：客户端改为读引擎记录、把自己交互式 PTY 与原生 agent 的条目经 `workplace record` 上报，并删除本地 JSON 存储及其迁移路径。 |
| 高 | 外部 agent 的显式续接和结构化会话协议 | 已落地：`oberth agent stream` live 桥（Codex、Claude、OpenCode、Pi）、macOS 结构化 UI、合成/单元回归（见 [validation.md](validation.md)）。仍缺：**真机** Codex/Claude 等验收、DeepSeek 结构化桥与明确续接、安装版本能力探测的完整产品化。审批在会话页由人决定，桥不加放宽参数；原生 TUI 审批不被输入条接管。 |
| 高 | 应用重启后的恢复 | 已实现用户同意后的私有布局快照、历史入口、失效/未知文件拒绝与显式新建Shell映射；不保存消息/输出/PID，不自动复活进程。仍缺旧PTY接管、输出历史恢复、精确分隔尺寸与agent对话恢复入口的完整联动。 |
| 中 | 命令块与 shell 集成 | 已落地临时 **zsh/bash/fish** 集成（OSC `133`/`633`）、命令块条、Shell→Agent 附件与 Tab 补全回读；复杂提示符（powerlevel10k/starship）与 IME 须手测（[validation.md](validation.md)）。 |
| 中 | 完整命令面板、快捷键与布局 | 已实现统一动作面板、自定义按键、有界递归分屏、标题拖放重排和独立窗口呈现权；原生面板/分隔/焦点/窗口回归已通过。样式/入口已整合，仍需全鼠标拖放、所有输入法与系统辅助功能的专项验收，未把模型/窗口回归当作全部交互完成。 |
| 中 | 分发与跨平台 | #45 已提供显式 Developer ID/公证关卡与通过 CI 的 Windows 基础服务生命周期；客户端已有只读更新入口。仍缺真实签名/公证账号验收、自动升级迁移及全面 Windows 生命周期；Windows/Linux 桌面端未实现。 |
| 持续 | 真实 provider 与长期负载 | 参数/原生窗口回归不能替代五个真实账号、全部输入法、鼠标菜单和长期资源趋势。需要受控真实场景及可比较测量，不宣称整体性能领先。 |

## 参考与不做的事

本轮以 Warp 公开提交 `6398d1e96d56e6e81c561cc3ebfc612c783f7dae` 的终端交互为参考，
例如 [pane 焦点与活动会话的分离](https://github.com/warpdotdev/warp/blob/6398d1e96d56e6e81c561cc3ebfc612c783f7dae/app/src/pane_group/focus_state.rs)。
参考行为，不引入或翻译 Warp 实现；本项目仍使用自研工作区状态和固定版本的 SwiftTerm。

不把 Warp Agent、云端 agent 编排、远程共享、模型凭证库、Git 写操作和自动安装 hooks/MCP
列为“为了追平参考项目必须补齐”的任务。它们不属于当前产品边界。

支持范围内若复现误停、归属错误或账本损坏，必须修复，不能以“后续迭代”掩盖。
验证命令与实际覆盖见 [validation.md](validation.md)，用户操作见 [客户端说明](../client/macos/README.md)。
