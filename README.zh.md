<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="brand/option-berth-lockup-dark.svg"><img src="brand/option-berth-lockup-light.svg" alt="option-berth" width="420"></picture></p>

# option-berth

[English](README.md) | 中文

按 Git 工作树里声明的配置启动本地服务，再说清它现在是什么状态：哪个目录的进程在跑、端口有没有监听、健康检查是否通过、为什么退出、下一步能做什么。命令名是 `oberth`。

适合同时跑 API、后台任务或多个分支的本地项目，也适合由编程助手驱动的开发流程。它不是操作系统沙箱、生产调度器，也不是整机进程清理工具；没有声明的服务，它不会去接管。

## 安装后先跑通一个服务

macOS / Linux：

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

Windows 安装、手动下载、校验、固定版本和协议切换见[安装说明](docs/install.zh.md)。完整的服务生命周期以 macOS 和 Linux 为准；Windows 构建通过不等于同等验收。

[快速开始](docs/quickstart.zh.md)只需要 Python 3 标准库，不用模型账号，也不用 Docker。在已审阅 `oberth.yaml` 的项目里：

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` 在配置的就绪条件满足后返回。`status` 把进程、监听和应用健康分开看，“看不见”就报未知，不会说成“没在运行”。`down` 只停当前工作树，不动整台机器。示例服务名是 `api`，换成自己的名字即可；完整边界见[产品说明](docs/product.zh.md)。

## 读 Runtime World

`oberth runtime inspect` 把一个工作树当前的事实收敛成一份文档，`schema` 固定为 `oberth.runtime.world/v1`，`revision` 由内容决定且不含时钟：

```sh
oberth runtime inspect --json
```

`coverage` 说明这次观察看到了多少——`observable`、`partial` 还是 `blind`；`health` 给出整棵工作树的结论。每个服务带 `state`（`ready`、`crash_loop`、`port_conflict`、`blocked_by_dep` 等），以及 `blocked_by`、`drifts` 和 `first_error`。出问题时 `diagnosis` 说明原因：`root_causes` 连同证据，相关的 `drifts` 与 `loops`，以及按 `safety` 排序的 `next_actions`——`read_only`、`local_restart` 或 `human_decision`。`oberth status` 与 `status --brief` 以新增字段的形式带上同一份 `diagnosis`，原有读取方不受影响。

## 需要时才开的 Attention

Attention Notes 是确定性的，随时可用：`oberth attn notes` 从 Runtime World 派生出这些提示，以仅本人可读的权限存进 `BERTH_HOME`，不需要密钥、网络或任何授权。

```sh
oberth attn notes
oberth attn on
```

`oberth attn on` 是另一件事：叠加模型分诊，模式为 `strict`、`assist` 或 `reconcile`。`reconcile` 只能把一条带重启的提示标成「可自动处理」，不会去杀不是它启动的进程，也不会改清单。授权规则和哪些信息会离开本机，见 [Attention](docs/attention.zh.md)。

## 接下来做什么

| 任务 | 入口 |
| --- | --- |
| 同时运行两个分支 | [多工作树](docs/worktrees.zh.md) |
| 后台为什么不在、空闲退出或要切版本 | [Runtime Service](docs/runtime-service.zh.md) |
| 启动失败、端口冲突、观察受限 | [常见问题](docs/troubleshooting.zh.md) |
| 接入编排工具或编程助手 | [工具接入](docs/integrations.zh.md)、[独立 skill](https://github.com/SheathedSharp/option-berth-skill) |
| 了解模型建议、提示与授权 | [Attention](docs/attention.zh.md) |
| 留下结构化交接信息 | [工作记录](docs/workplace.zh.md) |
| 查参数、JSON、RPC、配置字段 | [接口参考](docs/cli.zh.md) |

完整阅读路径见[文档导航](docs/index.zh.md)。组件之间只在协议版本匹配时通信。

## 参与开发

从[开发指南](docs/agent/develop.md)进入，再读[架构](docs/agent/architecture.md)和[验证范围](docs/agent/validation.md)。修改前先遵守 [AGENTS.md](AGENTS.md)；测试不要碰你日常的后台和真实项目数据，旧提交上的绿色结果也不能代表当前版本。

发布要求由[发布流程](docs/agent/releasing.md)维护，本地编译通过不代表可以发布。

## 许可与来源

项目贡献适用 [MIT](LICENSE)。第三方组件各自保留声明，见[归属与源码清单](third_party/README.md) 与[源码归档说明](docs/third-party.zh.md)。归档用于核查和开发，不改变 Go 的依赖解析。这里出现的外部工具名称只说明兼容性，不代表背书。
