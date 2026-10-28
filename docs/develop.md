# 开发说明

给人读的日常开发指南：怎么构建、怎么跑、改完跑什么。契约以 [product.md](product.md)、
[cli.md](cli.md)、[architecture.md](architecture.md) 为准；agent 需要先知道的硬边界在根目录 [AGENTS.md](../AGENTS.md)，
本文不重复那部分。

## 仓库怎么拆

| 区域 | 路径 | 说明 |
|------|------|------|
| 引擎与 CLI | `engine/` | `oberth` 产品接口；Go 版本见 `engine/go.mod` |
| 开发自动化 | 仓库根 `mage` 目标 | 构建与验证入口；**不要**把 Mage 当成产品 CLI |
| 文档 | `docs/` | 引擎与产品契约、验证范围、发版见 [validation.md](validation.md)、[releasing.md](releasing.md) |

界面与给 agent 的指引不在这个仓库，它们是独立产品：
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)（macOS 客户端）与
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)（coding agent 的 skill）。
三者只通过 CLI 与 `--json` 契约相连。

实现边界与不可违反的约束见根目录 [AGENTS.md](../AGENTS.md)，本文只讲怎么动这些代码。

## 构建与运行

在仓库根目录：

```sh
mage build          # 引擎 + Jev adapter
mage buildEngine    # 仅引擎 → ./bin/oberth
mage runCLI "status --json"
mage runDaemon      # 用刚构建的 CLI 起 daemon（隔离用途）
mage stop
```

`mage install` 会写入安装目录并改 PATH（可用 `NO_MODIFY_PATH=1` 跳过）；**未经明确授权不要**对用户日常环境执行安装或发布。

客户端与 skill 各自在自己的仓库里构建和验证，见它们各自的 README 与 AGENTS.md。

## 验证（改什么跑什么）

细则与边界见 [validation.md](validation.md)。常用入口（在 `engine/` 下）：

```sh
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
```

若 `proxy.golang.org` 拉模块超时，在同一 shell 前缀 `GOPROXY=https://goproxy.cn,direct`（或你环境可用的镜像）再执行上述命令；超时只表示网络，不代表代码不能编译。

脚本、发布与品牌生成：

```sh
python3 -m unittest discover -s scripts -p '*_test.py'
python3 brand/build-option-berth.py --check
```

隔离环境与授权边界见 [AGENTS.md](../AGENTS.md)「验证」；接口兼容和
「取消/未知不算成功」这类实现约束见 [AGENTS.md](../AGENTS.md)「接口」「一条事实链」。

## 提交与协作

本仓额外要求：subject 用产品语言，不写本仓内部的里程碑代号（`M0`–`M3`、阶段名），
也不用「闭环」「补齐」这类指向不明的词。`scope` 与改动的树一致（`engine`、`docs`、`ci`、`brand`…）。

- 推送前 `git log` / `git status`，重读目标 HEAD，注意与他人并行提交。
- 发版与标签流程见 [releasing.md](releasing.md)，版本与标签的约定只在那里维护。

## 相关专题

- CLI 与 RPC 契约：[cli.md](cli.md)；产品边界：[product.md](product.md)；实现与事实链：[architecture.md](architecture.md)
- 可选 attention adapter：[jev-adapter.md](jev-adapter.md)
- 客户端：[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos)
- coding agent 的 skill：[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)
