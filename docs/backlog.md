# 当前能力与待办

以 [产品定义](product.md) 和 [支持边界](architecture.md) 为准。主线是给 coding agent 的能力。
呈现层与 skill 的进度不在这个仓库：macOS 客户端在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)，给 agent 的 skill 在
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)。

## 后续能力

| 优先级 | 能力 | 当前缺口与完成条件 |
| --- | --- | --- |
| 高 | agent 侧的能力面是否够用 | 主线是给 coding agent 的能力，所以「agent 拿到这份代码能不能自己跑起来、自己判断为什么没跑起来」优先于任何界面。**2026-10-28 已跑过一轮**：真实 Claude Code 在陌生 worktree 上只靠 CLI 与 skill 完成了「起服务、失败、看日志、修、再起」，核心闭环 6 步走通（输入与结果见 [validation.md](validation.md)「核心验收」）。那一轮卡住的两处已修：失败提示不再指向不存在的日志，未监听的 `machine:` 依赖不再发布交接（契约见 [cli.md](cli.md)）。**2026-10-30 起**这轮验收沉淀为固定场景库（生成物缺失、端口被占、依赖未就绪、清单与进程不一致、同仓库双 worktree），见 [agent-eval.md](agent-eval.md)。仍缺：在一台能正常观察监听的普通终端或 CI 上，用真实 Codex / OpenCode / Pi 跑出可比的发布矩阵；本机 agent 沙箱内 preflight 会拒绝出表。 |
| 中 | 编排工具配方的端到端验证 | [integrations.md](integrations.md) 的 Conductor / Cursor / Claude Code / Codex 配置来自各工具官方文档，尚未在真实工具里跑通创建、启动和归档/清理。完成条件：在隔离 `HOME` 与可丢弃仓库中各跑一次 setup、`status --json` 和 cleanup（含 checkout 已删除的路径），记录实际输入与结果。 |

## 支持范围内的修复

支持范围内若复现误停、归属错误或账本损坏，必须修复，不能以「后续迭代」掩盖。
验证命令与实际覆盖见 [validation.md](validation.md)；客户端的行为见
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
