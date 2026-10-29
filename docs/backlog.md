# 当前能力与待办

以 [产品定义](product.md) 和 [支持边界](architecture.md) 为准。主线是给 coding agent 的能力。
呈现层的进度不在这个仓库：macOS 客户端是独立产品，它的能力与待办在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
终端能启动外部 agent，不等于已经具备完整终端工作台或全部 provider 会话协议。

## 后续能力

引擎侧与呈现层分开记：前者是产品主线，后者是它的呈现。

| 优先级 | 能力 | 当前缺口与完成条件 |
| --- | --- | --- |
| 高 | agent 侧的能力面是否够用 | 主线是给 coding agent 的能力，所以「agent 拿到这份代码能不能自己跑起来、自己判断为什么没跑起来」优先于任何界面。**2026-10-28 已跑过一轮**：真实 Claude Code 在陌生 worktree 上只靠 CLI 与 skill 完成了「起服务、失败、看日志、修、再起」，核心闭环 6 步走通（输入与结果见 [validation.md](validation.md)「核心验收」）。那一轮卡住的两处已修：失败提示不再指向不存在的日志，未监听的 `machine:` 依赖不再发布交接（契约见 [cli.md](cli.md)）。仍缺：Codex / OpenCode / Pi 与多 worktree 并发场景。 |
| 高 | skill 不在发布包内 | 安装与更新路径已写进 README：软链指向克隆下来的仓库，`git pull` 就是更新，复制的那份会停在原地。但发布压缩包与 `.app` 只带 `oberth` 二进制、许可与构建清单，只装发布包的用户拿不到 skill；[releasing.md](releasing.md) 里那份包内容清单是既定契约，把 `.agents/skills/option-berth/` 带进包需要决定。 |
| 高 | 客户端与引擎是否拆成两个产品 | 现在两者在 CI、release 脚本、品牌、文档与验证矩阵里绑在一起（17 个 workflow 里约一半是客户端/品牌/site）。未决定。做之前先出拆分清单与代价，别顺手拆。 |

## 支持范围内的修复

支持范围内若复现误停、归属错误或账本损坏，必须修复，不能以「后续迭代」掩盖。
验证命令与实际覆盖见 [validation.md](validation.md)；客户端的行为见
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)。
