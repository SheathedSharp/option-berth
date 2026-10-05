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

`settings.json` 与 `keybindings.json` 的解析契约同时版本化；消费端接线与完整示例在本 PR 后续提交补齐。
不把目前尚未接通的字段声明为可用功能。

## 扩展边界 / Extension boundary

未来插件市场应贡献声明式主题令牌和稳定命令 ID，经过版本/能力校验后由本机用户显式启用。
当前没有市场、安装器、动态脚本加载或扩展权限执行环境；v1 不接收未知配置键或插件命令。
这是保留兼容设计空间，而不是把占位字段当成已实现插件系统。

## 冻结渲染 / Deterministic rendering

`--render-states` 不读取个人目录，默认渲染 Paper；可显式使用
`--render-theme-file client/macos/Fixtures/theme-dark.json` 验证用户文件式深色主题。
该文件是测试夹具，不是客户端内置主题。错误的显式夹具使渲染退出失败，不能静默回退冒充测试通过。
