# Worktree Git 审查 / Read-only review

## 设计依据

面向多 worktree 与外部 coding agent 的人工审查，不做另一份 lazygit。
参考 lazygit 的 files/worktrees 上下文与窄窗口自适应，而不引入 staging、commit、reset、checkout 或自动 fetch：
https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md

状态解释按 Git short-format 两列语义：
https://git-scm.com/docs/git-status#_short_format
七种 unmerged 状态必须全部识别；MM 同时属于暂存与工作区，但全部文件总数只计一次。

## 本 PR 的实现目标

1. 复用 `oberth git files --json` 的分支、HEAD、上游、最近提交、同仓库 worktree 与文件事实。
2. 文件状态筛选和路径检索（包含重命名前路径）；清楚标出已选择文件、合并差异基线及读取错误。
3. diff 与文件列表随窄窗口上下排列，允许拖动分隔线，不让文件列把 diff 挤成不可读窄条。
4. 未知或坏数据不能通过宽松解码伪装成干净仓库；未知行数不按零合计。
5. 原生查找入口与 #71 的 Git 命令接通，复制文本不被导航键抢走。

当前 CLI 只提供最近一条提交和已检出的 worktree 引用，不能据此伪造完整提交 DAG。
历史图、未检出分支和分层 index/worktree patch 若要加入，需先做 CLI 新事实契约；本轮不新增无效占位面板。

## 验证门槛

纯 Swift 状态/过滤/统计回归，现有 Git 取消归属检查，macOS 原生搜索与布局检查，合成冻结截图。
实现和验证状态只在 PR/issue #69 更新，未完成前不合并或关闭。
