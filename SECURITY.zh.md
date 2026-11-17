# 安全政策

[English](SECURITY.md) | 中文

## 支持的版本

修复进入 `main`，随下一个打了标签的发布送出。只支持最新发布；更早的标签是历史，
不是仍在维护的分支。

## 报告漏洞

不要用公开 issue、PR 或讨论报告漏洞，走 GitHub 的私密渠道：

<https://github.com/SheathedSharp/option-berth/security/advisories/new>

请写清：

- 确切版本（`oberth version --json`）与运行的平台；
- 复现所需的命令、manifest 或配置；
- 实际看到的现象，以及你预期的结果。

用合成的路径和一次性仓库。不要附带凭据、私有仓库内容、日常运行时日志，或不属于
你自己的机器截图。

## 范围

在范围内：引擎、`oberth` CLI、JSON 与 RPC 接口、manifest 解析，以及本仓库的安装脚本。

不在本仓库范围：macOS 客户端与 coding agent skill 是独立产品，各自有仓库。引擎只是
调用、并不实现的第三方工具、agent CLI 和模型服务同样不在范围内，请向对应上游报告。

## 之后会怎样

单人维护的项目，不承诺响应时间。确认的问题在 `main` 上修复，走正常发布流程送出；
除你另有要求，会在发布说明里署名致谢。
