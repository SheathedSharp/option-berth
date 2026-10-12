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
9. Changes 与 History 共用同一套 patch 呈现：行号只从契约里的 `@@ -l[,s] +l[,s] @@` 推算，省略的计数按 unified diff 视为 1；解析不了的 hunk 头不编号。文件元信息、hunk 和增删行分层上色。相邻 graph 行只有在返回的 `parents` 里才连线，不把日志顺序画成不存在的边。

History 模式只绘制返回的 commit/parent/ref 事实，并在选中节点后请求对应 commit diff。未返回的提交、refs 和任何客户端猜测仍然不画。未检出分支和分层 index/worktree patch 仍不能由客户端伪造。多车道 DAG 不在本轮；父提交数量和真实的下一条 parent 连线就是当前拓扑。

## 验证门槛

纯 Swift 状态/过滤/统计回归，现有 Git 取消归属检查，macOS 原生搜索与布局检查，合成冻结截图。
行号、省略计数、坏 hunk 头和“相邻行不是 parent”由 `GitReviewTests` 覆盖。原生搜索、窄窗口分隔和冻结图仍由 git-review workflow 与 `mage states` 验收。那些检查通过并且栈下契约合入前，不合并本 PR，也不关闭 issue #94。
