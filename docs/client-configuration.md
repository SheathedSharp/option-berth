# 客户端个性化配置 / Client personalization

默认目录为 `~/.option-berth/config/`；设置 `BERTH_HOME` 时使用 `$BERTH_HOME/config/`。
这些是本机用户文件，不读项目内配置、不执行代码、不改变服务事实、Git 写边界或外部 agent 的审批方式。

## 主题 / Theme

纸张（Paper）是唯一内置默认。`theme.json` 使用语义令牌覆盖默认值；不是从几个写死主题里挑选。
设置 → 外观可打开文件；文件不存在时只创建最小模板，已有文件从不被客户端覆盖。

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

`appearance` 为 `light` 或 `dark`。颜色是严格的 `#RRGGBB`，不是任意 CSS。
令牌包括 `canvas`、`surface`、`sunken`、`ink`、`inkMuted`、`inkFaint`、`dormant`、`line`、`lineStrong`、
`accent`、`accentSoft`、`live`，以及 `diff.added`、`diff.addedBackground`、`diff.removed`、
`diff.removedBackground`、`diff.changed`、`diff.changedBackground`、`terminal.background`、`terminal.foreground`。
不提供终端专用前景/背景时，它们跟随界面 `ink`/`canvas`。

字体使用已安装字体的家族名称；界面可用 `__system__`。三个缩放值范围均为 0.75–2.0。
原生终端使用 dataFont/dataScale，不为改色重启 PTY。未提供的颜色使用 Paper，未提供的字体/缩放
保留旧界面偏好，再回退到默认值。旧冰川/午夜/森林主题名称不再生效；它们没有被包装成隐藏预设。

主题文件存在时旧 GUI 外观控件禁用，避免看似保存成功却被文件覆盖。删除主题文件恢复 Paper 与
旧字体偏好；“恢复默认外观”只清除旧偏好，不删除用户文件。

## 更新和错误 / Reloads

文件事件与目录事件均被监听，因此支持原地保存、编辑器原子替换、文件/目录删除及重建。
事件有界合并；解析在单一后台队列执行，无轮询定时器。无效 JSON、未知顶层键、未知版本或非法值
保留当前进程的最后有效配置，设置页显示错误；冷启动遇到坏文件使用默认值。每份文件上限 64 KiB，
只接受普通文件，不跟随叶子符号链接，不读取 FIFO。配置错误不会关闭会话或启动任何进程。

`settings.json` 的解析契约同时版本化；其消费端仍待后续提交，不把未接通的字段声明为可用功能。

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

## 扩展边界 / Extension boundary

未来插件市场应贡献声明式主题令牌和稳定命令 ID，经过版本/能力校验后由本机用户显式启用。
当前没有市场、安装器、动态脚本加载或扩展权限执行环境；v1 不接收未知配置键或插件命令。
这是保留兼容设计空间，而不是把占位字段当成已实现插件系统。

## 冻结渲染 / Deterministic rendering

`--render-states` 不读取个人目录，默认渲染 Paper；可显式使用
`--render-theme-file client/macos/Fixtures/theme-dark.json` 验证用户文件式深色主题。
该文件是测试夹具，不是客户端内置主题。错误的显式夹具使渲染退出失败，不能静默回退冒充测试通过。
