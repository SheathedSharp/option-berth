# 文档导航

[English](index.md) | 中文

按你手上的任务往下读，不必先学协议。`oberth` 根据当前 Git 工作树的配置启动本地服务，再发布它看到的情况：进程、端口、健康、退出，以及出了问题时该往哪查。

## 使用 oberth

| 你要做什么 | 从这里开始 |
| --- | --- |
| 安装或升级 | [安装说明](install.zh.md) |
| 让第一个服务跑起来 | [快速开始](quickstart.zh.md) |
| 同时开发两个分支 | [多工作树使用](worktrees.zh.md) |
| 了解后台宿主 | [Runtime Service 后台服务](runtime-service.zh.md) |
| 看整体运行画面、弄明白某个服务为什么坏 | [运行事实](cli/facts.zh.md#oberth-runtime-inspect-path---json) |
| 一步步排查启动或运行故障 | [常见问题](troubleshooting.zh.md) |
| 读 Attention 提示，或开启模型建议 | [Attention 辅助判断](attention.zh.md) |
| 让编程助手学会操作 | [工具接入](integrations.zh.md) |
| 记录临时任务、交接工作 | [工作记录](workplace.zh.md) |

## 查询接口细节

[CLI、JSON、RPC 与配置参考](cli.zh.md)用来查字段和行为，不是入门教程。[产品范围](product.zh.md)说明职责与不做的事；[待办](backlog.zh.md)区分已有能力和还缺的验收证据。自动生成的 schema 位于 [engine/docs/schema](../engine/docs/schema/)；要改对应的 Go 源定义再生成，不直接修改 JSON 来掩盖差异。

## 开发或扩展项目

先读[开发指南](agent/develop.md)，再按需要看[架构](agent/architecture.md)中的包职责和[验证指南](agent/validation.md)中的检查选择。[发布流程](agent/releasing.md)管发布，不是初次使用的步骤。[第三方源码](third-party.zh.md)说明源码归档和许可校验方法。

`agent/` 下的工程资料只维护英文。`agent/history/` 下带日期的记录只说明当时的输入与结果，不能当作当前版本已经通过测试。[文档规范](AGENTS.md)规定这些页面的维护方式。
