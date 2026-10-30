# 开发约定

本文件只放这个仓库特有的、动手前必须知道的边界。通用的交付规则（中文 `type(scope): 主题`、
一主题一提交、分支与合并、不擅自改检查门槛）来自本机全局规则，不在这里复述。

## 交付

合并前移到远端 PR：主题分支推送到 `origin`，等本 PR 的所有 required checks 通过（清单见
[`.github/quality-gate-checks.json`](.github/quality-gate-checks.json)，以 `main` 的 ruleset 为准），
经用户确认后用 merge commit 合并；不在本地把主题分支合入 `main` 后直接推送。发布脚本的原子
推送是例外。修改 workflow、CODEOWNERS、race 清单或发布/检查脚本时，`Gate integrity` 要求
PR 作者是仓库维护者，不要绕过。命令、失败处理与清理见项目 skill
[`option-berth-delivery`](.agents/skills/option-berth-delivery/SKILL.md)。

契约与做法各有去处，冲突时以契约为准：

| 要查的东西 | 去哪里 |
|---|---|
| 产品定义、该不该做 | [docs/product.md](docs/product.md) |
| CLI / RPC / JSON / 清单字段 | [docs/cli.md](docs/cli.md) |
| 生产实现边界、事实链 | [docs/architecture.md](docs/architecture.md) |
| 构建、运行、改哪跑什么 | [docs/develop.md](docs/develop.md) |
| 验证项的含义与平台范围 | [docs/validation.md](docs/validation.md) |
| 版本、标签、发版前置 | [docs/releasing.md](docs/releasing.md) |
| 改 macOS 客户端 | [option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)（独立产品） |
| 改 coding agent 的 skill | [option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)（独立产品） |

历史方案、临时排障和旧迭代的要求不是当前契约，不按它们补实现。

## 一条事实链

`worktree → manifest → service → run → runtime facts` 是唯一的运行真相。
新增能力挂到这条链上，不另立状态源、不缓存第二份运行结果来绕过失败路径。

- 清单由人确认，模型只起草；`machine:` 依赖只读；产品 CLI 的 Git 操作只读。
- 取消、未知和观察失败不是成功，信号回执不等于进程已退出；跨代际的结果不能覆盖新快照。
- 锁、缓存、队列要明确所有权与释放边界，不用游离任务伪造硬超时。

细节见 [docs/architecture.md](docs/architecture.md)。

## 接口

`oberth` 的 CLI、RPC、JSON 和清单是本项目的对外接口，变更要同步 schema、文档与回归。
JSON stdout 不混入诊断。Mage 只是开发工具，它的目标和日志不属于产品语义。

## 验证

改什么跑什么，入口见 [docs/develop.md](docs/develop.md)，各项检查证明什么见
[docs/validation.md](docs/validation.md)。

- 完整服务场景只在 macOS/Linux 上验证；Windows 的证据是独立原生构建/冒烟与 arm64 交叉构建。
- GUI 改动要区分冻结渲染与真实交互：离屏图不能证明鼠标、输入法和长时负载正常。
- 性能结论需要可比测量，不同基线上的数字不算结论。
- 测试、真实服务和截图使用独立 `HOME`/`BERTH_HOME` 与可丢弃项目，结束后回收自己起的进程。
  不动用户日常 daemon、账本或真实仓库写入；安装、发布、Git 写入和仓库可见性变更需明确授权。

只报告实际执行的输入和结果。格式检查、回归定义和旧提交绿灯都不能代替测试；
没跑的验证写清楚没跑。

## 资产

示例、夹具和截图使用合成身份与路径。不提交凭证、私人日志、真实用户截图、缓存、原始扫描
报告或历史备份。保留根 MIT、上游版权与随附字体的 OFL：不修改系统字体，不把字体重新许可为 MIT。

## 文档

只保留当前有用的契约、依据和真实缺口；已落地的计划并进契约文档后从计划里删掉。
