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
  "servicesDetailWidth": 420,
  "gitDetailWidth": 380,
  "shellIntegration": false,
  "defaultAgent": "codex"
}
```

| 字段 | 类型与生效方式 |
|---|---|
| `reduceMotion` | 布尔值。与只读系统设置取 OR；`false` 不得关闭系统已开启的减弱动态。应用动画事务与呼吸状态使用合并结果，不改系统偏好。 |
| `sidebarWidth` | 140–320 的数值，单位为点。编辑后即时生效；仍可拖动分隔条。文件指定该值时，拖动只是本进程覆盖，不写回文件或旧偏好。此字段变化/删除时重新应用配置；修改其他字段不让分隔条跳位。 |
| `servicesDetailWidth` / `gitDetailWidth` | 各自 340–500 点，分别控制服务日志与 Git 差异阅读区，互不影响。未配置时，各自读取新偏好键，再只读兼容旧共享 `detailWidth`；以后拖动写入各自键。文件覆盖期间拖动只改变本进程值；对应字段改变/删除时重新解析。窄窗口只压缩显示，不改写偏好。 |
| `shellIntegration` | 布尔值，只影响以后新建的受支持 zsh。已有 PTY 不重启，不修改 shell 启动文件。文件指定此项时，GUI 开关只读；未指定时沿用本机开关。 |
| `defaultAgent` | `codex`、`claude`、`opencode`、`deepseek` 或 `pi`。只为新创建的 worktree UI 状态提供默认值；不改变已有 provider、草稿、布局或恢复记录，不自动安装/启动 agent。 |

删除文件或将可选字段设为 `null` 表示不覆盖；坏文件则保留最后有效配置。
如果新 worktree 是从保存的布局恢复，使用恢复记录中的 provider，而不是覆盖为新的默认值。

## 快捷键 / Keybindings

`keybindings.json` 覆盖默认命令。所有键位包含 Command；修饰键为可选布尔值，不支持脚本或 when 表达式。

```json
{
  "schemaVersion": 1,
  "bindings": {
    "services": {"key": "j", "option": true},
    "worktree.1": {"key": "1"}
  }
}
```

默认：`connect` ⌘N，`worktree.1`…`worktree.9` ⌘1…⌘9；数字遵循侧栏当前筛选后的名称排序，
保留所选模块；没有对应项目时不执行。`services` ⌥⌘S、`code` ⌥⌘G、`terminal` ⌥⌘T、`sidebar` ⌘B。
其余命令：`sessions` ⇧⌘O、`recovery` ⌥⇧⌘O、`updates` ⌥⌘U、`find` ⌘F、`refresh` ⌘R、`settings` ⌘,。
命令面板固定 ⇧⌘P。菜单和侧栏提示随有效键位同步更新。

文件是“默认映射 + 文件覆盖”，不是再叠加旧 GUI 键位。未知 command ID、未知修饰键、重复键位或
覆盖受保护的原生编辑键会拒绝整份变更并保留此前有效映射。设置页和快捷键编辑器显示诊断。
文件存在时 GUI 不写入或重置它；删除文件后恢复旧 GUI 自定义值（再回退默认）。
旧 services/code/terminal 的 ⌘1/2/3 与侧栏的 ⌥⌘S 默认拷贝会迁移；真正的用户改动尽量保留。
若旧自定义与新默认冲突，使用新默认并明确提示，原始偏好字节不删除。

项目接入等待或弹窗期间禁止并行导航命令；原生输入法有组合文本时不派发操作。Git 中的 ⌘F 路由
预留给文件检索视图（该视图在 Git 审查 PR 中接入），不会错误地打开服务日志搜索。

## 更新和错误 / Reloads

单一后台队列进行有界读取，文件/目录事件被合并，没有轮询定时器。支持原地保存、编辑器原子替换、
删除和目录/父目录重建。每份文件最多 64 KiB，仅接受普通文件，不跟随叶子符号链接，不读取 FIFO。

无效 JSON、未知顶层键、未知版本或非法值会在设置页显示错误，并保留当前进程的最后有效配置；
冷启动遇到坏文件则使用默认/兼容回退。文件错误不会结束会话或启动进程。
客户端不重写用户文件，因此不会覆盖编辑器中的并行修改。`$schema` 是可选、惰性的编辑器元数据；
只接受非空且最多 2048 UTF-8 字节、无控制字符的字符串。客户端不解析 URI、不联网、不执行它。
除明确列出的字段和 `$schema` 外，v1 仍拒绝未知键。

## 个性设置盘点与剩余边界 / Inventory

| 当前设置 | 来源与状态 |
|---|---|
| 界面/数据/日志字体、缩放，界面/diff/终端颜色 | `theme.json`；旧 `ui.*` 偏好仅作兼容回退。 |
| 侧栏宽度、减弱动态、新 Shell 集成、新 worktree 默认 Agent | `settings.json`；上述生命周期已接入。 |
| 快捷键 | `keybindings.json` 与旧 `workspace.shortcuts.v1` 迁移已接入；原生命令、编辑器/终端组合文本与 Git 查找由同候选检查覆盖。 |
| 服务/Git 详情区宽度 | `settings.json` 的独立字段；原共享 `detailWidth` 只作兼容回退，拖动使用分开的 `ui.servicesDetailWidth.v1` / `ui.gitDetailWidth.v1`。 |
| 布局恢复/记住会话 | 原有显式授权与 `client-recovery`；涉及路径元数据和删除操作，不因外观配置变化自动开启或删除。 |
| Jev 外部 adapter | 继续使用独立 `jev.json`；不搬运密钥到主题、普通偏好、截图或诊断。 |
| 首次指引、当前选择、命令历史 | 首次指引标记是使用状态；当前选择/历史按既有内存与显式恢复边界管理，不当作主题配置执行。 |

Schema、独立详情宽度、新 Git 自适应布局及统一工作台的新会话入口消费同一配置。
宽窄窗口、文件热更新、原生命令/组合文本、同一 PTY 与新会话默认值分别有检查，全部属于发版门槛。
未读取真实 provider 账号；最终日常使用与安装由用户验收，不能用合成场景替代真实账号和长期负载。

## JSON Schema 与编辑器 / Editor assistance

`client/macos/Configuration/` 包含 `theme.schema.json`、`settings.schema.json`、`keybindings.schema.json`
和对应示例，采用 JSON Schema 2020-12。把示例与相应 Schema 放在同一目录，示例中的相对 `$schema`
即可由支持它的编辑器提供字段补全和错误标注。也可在编辑器的 JSON 文件关联配置中指定仓库内 Schema，
不向个人配置加入元数据。客户端不替编辑器下载 Schema，也不把它当作插件。

键位 Schema 描述结构；未知命令 ID、原生保留键及与默认值合并后的冲突由 #71 的命令目录进一步校验。
Schema 的 `maxLength` 按 Unicode 字符而非 UTF-8 字节计数；字体/元数据/命令 ID 的字节上限、
完整 Unicode 控制字符与整个文件的 64 KiB 上限仍由本机解析器强制执行。Schema 验证通过不替代运行时校验。

`python3 scripts/check_client_configuration.py` 在一次性虚拟环境中安装固定版本的测试验证器，
编译真实 Swift 解析器，再对同一组合成样例进行双向对照，并核对全部顶层键和颜色令牌，防止静态文档漂移。
这也是 PR CI 和 macOS 发版验证使用的同一个入口；失败会阻止继续，不提供跳过开关。
需要 Python venv、Swift 和测试依赖索引访问；依赖只装入临时目录，不进入应用或全局 Python。

参考：
- https://json-schema.org/draft/2020-12/json-schema-validation
- https://code.visualstudio.com/docs/languages/json#_json-schemas-and-settings
- https://python-jsonschema.readthedocs.io/en/stable/validate/

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
`PersonalizationChecks/WindowCompositionChecks.swift` 另行捕获测试自有终端窗口，并断言背景合成像素，
不截取其他窗口或用户项目；颜色更新保持同一 PTY。

当前依据：VS Code 的配置默认值/用户覆盖与声明式贡献边界，以及 Apple 的只读 accessibilityReduceMotion / 自定义 EnvironmentKey。
不移植它们的项目级执行或完整扩展体系。

### 原生命令更新与焦点

有效键位或当前可见 Worktrees 变化后，客户端按事件刷新自己贡献的原生菜单内容。
新的键位立即生效，旧键位立即卸载；不要求用户先打开菜单，不轮询，也不接管原生编辑键。
命令面板的选择等待所属窗口真正结束 sheet 并重新成为 key window 后，只派发一次。
NSTextView 与原生终端的 NSTextInputClient 组合文本均受保护；原生面板中不切项目。
独立终端仍可查找自身内容，但不能重定向主工作区。没有新的全局按键监听器。
