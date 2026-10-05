# 客户端个性化配置 / Client personalization

默认目录为 `~/.option-berth/config/`；设置 `BERTH_HOME` 时使用 `$BERTH_HOME/config/`。
只读本机用户文件，不读项目内同名文件、不执行代码、不改变服务事实、Git 写边界或外部 agent 的审批方式。
设置 → 外观可打开配置；不存在时创建最小模板，已有文件从不被客户端覆盖。

## 主题 / Theme

纸张（Paper）是唯一内置默认。`theme.json` 按语义令牌覆盖默认值，不再从几个写死主题中选择。

```json
{
  "schemaVersion": 1,
  "appearance": "light",
  "colors": {"accent": "#A35F42", "accentSoft": "#F2DED3"},
  "interfaceFont": "__system__",
  "dataFont": "Monaspace Neon",
  "interfaceScale": 1.0,
  "dataScale": 1.1,
  "logScale": 1.1
}
```

`appearance` 为 `light` 或 `dark`。颜色为严格的 `#RRGGBB`，不是任意 CSS。
可用令牌：`canvas`、`surface`、`sunken`、`ink`、`inkMuted`、`inkFaint`、`dormant`、`line`、`lineStrong`、
`accent`、`accentSoft`、`live`；差异颜色 `diff.added`、`diff.addedBackground`、`diff.removed`、
`diff.removedBackground`、`diff.changed`、`diff.changedBackground`；终端颜色 `terminal.background`、`terminal.foreground`。
未指定终端专用色时，跟随界面 `canvas` / `ink`。改变颜色不重启 PTY。

字体使用本机已安装家族名称；界面可用 `__system__`。三个缩放值范围均为 0.75–2.0，
分别调整界面、数据/原生终端和日志字号。未提供的颜色使用 Paper，未提供的字体/缩放保留旧 GUI 偏好，
再回退到默认值。旧冰川/午夜/森林名称不再生效，也没有被包装成隐藏预设。
主题文件存在时，旧 GUI 外观编辑控件禁用，避免看似保存却被文件覆盖。
删除文件恢复 Paper 与旧字体偏好；“恢复默认外观”只清除旧偏好，不删除用户文件。

## 工作区偏好 / Workspace preferences

`settings.json`：

```json
{
  "schemaVersion": 1,
  "reduceMotion": true,
  "sidebarWidth": 220,
  "shellIntegration": false,
  "defaultAgent": "codex"
}
```

| 字段 | 类型与生效方式 |
|---|---|
| `reduceMotion` | 布尔值。与只读系统设置取 OR；`false` 不得关闭系统已开启的减弱动态。应用动画事务与呼吸状态使用合并结果，不改系统偏好。 |
| `sidebarWidth` | 140–320 的数值，单位为点。编辑后即时生效；仍可拖动分隔条。文件指定该值时，拖动只是本进程覆盖，不写回文件或旧偏好。此字段变化/删除时重新应用配置；修改其他字段不让分隔条跳位。 |
| `shellIntegration` | 布尔值，只影响以后新建的受支持 zsh。已有 PTY 不重启，不修改 shell 启动文件。文件指定此项时，GUI 开关只读；未指定时沿用本机开关。 |
| `defaultAgent` | `codex`、`claude`、`opencode`、`deepseek` 或 `pi`。只为新创建的 worktree UI 状态提供默认值；不改变已有 provider、草稿、布局或恢复记录，不自动安装/启动 agent。 |

删除文件或将可选字段设为 `null` 表示不覆盖；坏文件则保留最后有效配置。
如果新 worktree 是从保存的布局恢复，使用恢复记录中的 provider，而不是覆盖为新的默认值。

## 快捷键 / Keybindings

`keybindings.json` 使用 `schemaVersion` 和 `bindings`，按稳定命令 ID 配置。解析/监听与上面的文件共用，
菜单/命令面板的消费、原生保留键、冲突检测和旧映射迁移在独立命令路由 PR #71 中交付。
仅合入主题 PR 不代表快捷键改造已交付；最终按该 PR 的命令契约验收。

## 更新和错误 / Reloads

单一后台队列进行有界读取，文件/目录事件被合并，没有轮询定时器。支持原地保存、编辑器原子替换、
删除和目录/父目录重建。每份文件最多 64 KiB，仅接受普通文件，不跟随叶子符号链接，不读取 FIFO。

无效 JSON、未知顶层键、未知版本或非法值会在设置页显示错误，并保留当前进程的最后有效配置；
冷启动遇到坏文件则使用默认/兼容回退。文件错误不会结束会话或启动进程。
客户端不重写用户文件，因此不会覆盖编辑器中的并行修改。当前 v1 不接受实例中的 `$schema` 或任意扩展键。

## 个性设置盘点与剩余边界 / Inventory

| 当前设置 | 来源与状态 |
|---|---|
| 界面/数据/日志字体、缩放，界面/diff/终端颜色 | `theme.json`；旧 `ui.*` 偏好仅作兼容回退。 |
| 侧栏宽度、减弱动态、新 Shell 集成、新 worktree 默认 Agent | `settings.json`；上述生命周期已接入。 |
| 快捷键 | 文件契约与原有 `workspace.shortcuts.v1` 迁移由 #71 负责。 |
| 服务/Git 详情区宽度 | 仍使用旧共享 `detailWidth`，尚未文件化；后续须拆分模块偏好并保持拖动能力，不能把此项标记完成。 |
| 布局恢复/记住会话 | 原有显式授权与 `client-recovery`；涉及路径元数据和删除操作，不因外观配置变化自动开启或删除。 |
| Jev 外部 adapter | 继续使用独立 `jev.json`；不搬运密钥到主题、普通偏好、截图或诊断。 |
| 首次指引、当前选择、命令历史 | 首次指引标记是使用状态；当前选择/历史按既有内存与显式恢复边界管理，不当作主题配置执行。 |

统一 JSON Schema、剩余详情宽度配置和与统一工作台 #75 的组合验收尚未完成。#66 继续跟踪这些缺口；
单项测试通过不是全部个人化需求已经闭环。

## 扩展边界 / Extension boundary

后续市场应贡献声明式主题令牌和稳定命令 ID，经版本/能力校验后由本机用户显式启用。
当前没有市场、安装器、脚本加载、远程配置或扩展权限执行环境；v1 拒绝未知键和插件命令。
预留设计空间不等于实现插件系统。

## 验证 / Verification

纯配置/类型检查、原生文件事件、原生消费者是不同验证层。`PersonalizationChecks` 使用显式隔离 HOME，
真实 NSApplication 事件循环、BoardView 和自己的 PTY，验证热更新、原生拖动、旧状态保留、文件不被覆盖及同一进程。
系统布尔组合由策略矩阵验证，不通过写系统偏好来模拟。

`--render-states` 不读个人文件，默认 Paper；可显式传入
`--render-theme-file client/macos/Fixtures/theme-dark.json` 检查用户文件式深色夹具。
坏夹具退出失败，不静默回退。冻结视图、原生视图位图和真正窗口合成需分开说明：
原生终端的 cacheDisplay 位图可能没有背景层，不能据此声称背景的窗口合成已验证。

当前依据：VS Code 的配置默认值/用户覆盖与声明式贡献边界，以及 Apple 的只读 accessibilityReduceMotion / 自定义 EnvironmentKey。
不移植它们的项目级执行或完整扩展体系。
