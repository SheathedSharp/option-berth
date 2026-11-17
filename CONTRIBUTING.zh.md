# 贡献指南

[English](CONTRIBUTING.md) | 中文

本仓库只有引擎和 `oberth` CLI。macOS 客户端与 coding agent skill 是各自仓库里的独立
产品，它们的改动不属于这里。

## 动手之前

1. 先确认改动属于这个产品。[docs/product.md](docs/product.md) 写明了项目负责什么、
   有意不做什么。
2. 先读入口文档：[docs/agent/develop.md](docs/agent/develop.md) 是构建与检查命令，
   [docs/agent/architecture.md](docs/agent/architecture.md) 是改动必须挂上去的那条事实链。
3. 改 CLI、RPC、JSON 或 manifest 属于合同变更：生成的 schema、[docs/cli.md](docs/cli.md)
   里的说明和对应回归要在同一个 PR 里一起更新。

## 检查

跑你改动的那部分，并如实报告输入与结果。[docs/agent/validation.md](docs/agent/validation.md)
说明每项检查证明了什么、在哪里跑才安全。

任何会启动服务的检查都要用隔离的 `HOME` 与 `BERTH_HOME`，配一次性项目；自己起的进程
自己收干净。永远不要把测试指向日常的运行时服务、账本或真实仓库。

不要为了让改动通过而放宽检查、required checks 清单或 workflow 门槛；改改动本身。

## Pull request

- 一个主题一个分支一个 PR，主题内分几个小提交。
- 写清跑了什么、结果是什么、哪些没跑。PR 模板要的就是这三项；格式检查不能顶替测试。
- required checks 变绿后由维护者通过 PR 合并。不要提前合并，不要直接往 `main` 推。

## 贡献与许可

贡献按 [MIT 许可](LICENSE) 接受。第三方源码、字体与站点资产各自保留原许可，加入前先看
[third_party/README.md](third_party/README.md)。
