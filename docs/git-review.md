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
2. 通过独立的 `oberth git graph --json --limit N` 读取有界提交拓扑、完整 parent IDs、本地 refs 和 worktree；Graph 返回 `observed_head` 与 `truncated`，客户端不得把旧图冒充当前完整历史。
3. 文件状态筛选和路径检索（包含重命名前路径）；清楚标出已选择文件、合并差异基线及读取错误。
4. diff 与文件列表随窄窗口上下排列，允许拖动分隔线，不让文件列把 diff 挤成不可读窄条。
5. 未知或坏数据不能通过宽松解码伪装成干净仓库；未知行数不按零合计。
6. 原生查找入口与 #71 的 Git 命令接通，复制文本不被导航键抢走。
7. Graph 是只读、有界、可取消的事实读取：不 fetch、不刷新 index、不启用 fsmonitor；超过 limit 时明确标记 `truncated`。
8. commit diff 由 `oberth git diff --commit <hex> [--file path] --json` 提供；它固定使用该 commit 的第一个 parent，root commit 使用空树，merge commit 不生成 combined diff，并在响应中返回 `commit`/`base` 以便 UI 显示真实基线。历史 patch 不读取当前 worktree 或 index；重命名同时保留 `old_path` 与新路径。

Graph contract 已在独立 PR 中落地，客户端 History 模式只绘制返回的 commit/parent/ref 事实，并在选中节点后请求对应 commit diff；此前“没有 CLI 事实支撑就不画 DAG”的限制仍适用于未返回的提交、refs 和任何客户端猜测。未检出分支和分层 index/worktree patch 仍不能由客户端伪造。

## 验证门槛

纯 Swift 状态/过滤/统计回归，现有 Git 取消归属检查，macOS 原生搜索与布局检查，合成冻结截图。
Graph contract 的 Go parser/CLI 回归与只读边界在 PR #98 更新；Graph UI 将在后续独立 PR 实现。实现和验证状态未完成前不合并或关闭 issue #94。
