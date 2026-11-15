<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg"><img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420"></picture></p>

# option-berth

[English](README.md) | 中文

在多个 Git 工作树中管理本地开发服务：按配置启动和停止，查看哪个目录的进程在运行、端口是否监听、健康检查是否通过，以及失败原因。`oberth` 是命令名。

适合需要同时开发多个分支、运行 API 和后台任务的本地项目，也能给编程助手提供结构化状态。它不是操作系统沙箱、生产部署平台或全机进程清理工具；它不会替你管理未声明的外部服务。

## 安装后，先跑通一个服务

macOS / Linux：

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows 安装、手动下载、校验、固定版本和协议切换见[安装说明](docs/install.zh.md)。完整服务生命周期主要支持 macOS/Linux；不要把 Windows 构建通过当作功能已经同等验收。

第一次使用请跟着[快速开始](docs/quickstart.zh.md)：只需 Python 3 标准库，不用模型账号或 Docker。在已审阅 `oberth.yaml` 的项目目录里，日常操作是：

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` 等待配置的就绪条件；`status` 区分进程、监听和健康，不把“看不见”当作“没运行”。`down` 针对当前工作树，不是批量清理整台电脑。示例服务名是 `api`，自己的项目请用实际名称。

完整的适用范围与安全限制，见[产品说明](docs/product.zh.md)。

## 接下来做什么

| 任务 | 入口 |
| --- | --- |
| 同时运行两个分支 | [多工作树](docs/worktrees.zh.md) |
| 后台没运行、空闲退出或需要切版本 | [Runtime Service](docs/runtime-service.zh.md) |
| 启动失败、端口冲突、观察受限 | [常见问题](docs/troubleshooting.zh.md) |
| 接入编排工具或让编程助手操作 | [工具接入](docs/integrations.zh.md)、[独立 skill](https://github.com/SheathedSharp/option-berth-skill) |
| 了解可选模型建议及其授权 | [Attention](docs/attention.zh.md) |
| 留下结构化交接信息 | [工作记录](docs/workplace.zh.md) |
| 查参数、JSON、RPC、配置字段 | [接口参考](docs/cli.zh.md) |

全部阅读路径见[文档导航](docs/index.zh.md)。macOS 界面是[独立项目](https://github.com/SheathedSharp/option-berth-macos)，不是 CLI 的必要组件；各组件需要匹配的协议版本。

## 参与开发

从[开发指南](docs/agent/develop.md)进入，再读[架构](docs/agent/architecture.md)和[验证范围](docs/agent/validation.md)。修改前遵守 [AGENTS.md](AGENTS.md)；不要在自己的日常后台和真实项目数据上运行测试。历史结果不代表当前提交已经通过验收。

发布要求由[发布流程](docs/agent/releasing.md)维护，本地编译通过不代表可以发布。

## 许可与来源

项目贡献适用 [MIT](LICENSE)。第三方组件及历史来源各自保留版权和许可，见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 与[源码归档说明](docs/third-party.zh.md)。归档用于核查和开发，不改变 Go 的依赖解析。外部工具名称只说明兼容性，不代表背书。
