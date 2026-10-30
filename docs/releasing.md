# 发布流程 / Release process

## 本仓库只发布引擎

option-berth 拆成三个独立产品：本仓库发布 `oberth` 引擎与 CLI；macOS 客户端与给 coding agent
的 skill 各自在自己的仓库发版。三者只通过 CLI 与 `--json` 契约相连，版本与发布节奏互不绑定。

本仓库的 Release 资产只有引擎：

- macOS、Linux、Windows 的 amd64 与 arm64 压缩包（`option-berth-vX.Y.Z-<os>-<arch>.tar.gz|zip`）；
- Linux 的 `.deb` 与 `.rpm`（`option-berth_X.Y.Z_<deb-arch>.deb`、
  `option-berth-X.Y.Z-1.<rpm-arch>.rpm`）；
- 每个资产的 `.sha256`；
- 汇总的 `SHA256SUMS`。

不发布 macOS 应用、DMG、公证包、客户端截图或 skill 安装包。

引擎模块路径是 `github.com/sheathedsharp/option-berth/engine`；
`go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@vX.Y.Z` 直接从 Git 标签
构建，不经过 Release 资产，也不需要额外的模块仓库。

安装入口是仓库根目录的 `install.sh`（macOS/Linux）、`install.ps1`（Windows），以及对应的
`uninstall.sh` / `uninstall.ps1`；它们不占 Release 资产，从 `main` 提供：

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

脚本解析 `releases/latest`（或用 `--version` 固定标签），按平台下载压缩包与同名 `.sha256`，
校验通过才解压，把 `oberth` 与 `jev-attention` 装到 `~/.local/bin`（Windows 为
`%LOCALAPPDATA%\option-berth\bin`），把 `LICENSE`、`licenses/` 与构建清单留在
`~/.local/share/option-berth`（Windows 为 `%LOCALAPPDATA%\option-berth`），通过包内
`install-path.sh` 配置 PATH，最后运行 `oberth version` 自检。它不启动服务、不写 Git、
不需要管理员权限。安装目标来自已发布标签，不是 `main` 上的未发布代码；旧包没有
`install-path.sh` 时，从同一标签的 `scripts/install-path.sh` 取用。

官方 GitHub 主机不可达或过慢时，安装器按固定顺序回退到 GitHub 代理（默认
`https://ghfast.top`、`https://gh-proxy.com`，可用 `BERTH_INSTALL_MIRRORS` 覆盖或清空）。
校验和优先取官方；来自镜像时要求第二个源报告相同摘要，校验不通过不落盘。镜像只改变传输
路径，不改变发布资产的定义。

卸载器先尝试 `oberth daemon stop`，再删除二进制、许可与安装器写入的 PATH 行；默认保留
`~/.option-berth` 运行状态，只有 `--purge-state` / `-PurgeState` 才删除。完整说明见
[install.md](install.md)。

### 拆分后的版本线

引擎版本从 **1.0.0** 开始重新起算。这一次不是协议升级：`VERSION` 重定基线不改变
`ProtocolVersion`，也不重新生成协议 schema。之后的版本仍按下面的步进表推进。

拆分前 `v0.2.0` 至 `v0.8.2` 的标签和 Release 属于合并产品，包含 macOS 应用
资产。**2026-10-29 起它们已从本仓库远端移除**；对应提交仍在 Git 历史中，最后一个
拆分前提交是 `74a51d1`。客户端与 skill 的版本只在各自仓库维护。

## 版本含义

产品版本为 **X1.X2.X3**，根目录 `VERSION` 是引擎版本的唯一来源，不加 `v`。
Git 标签使用 `vX1.X2.X3`，必须为注释标签，指向已经验证且属于 `main` 历史的提交。
每次发布同时创建 `engine/vX1.X2.X3` 注释标签，指向同一提交；它是 `engine/` 子模块的 Go 版本标签，
`go install .../engine/cmd/oberth@vX1.X2.X3` 依赖它。

| 变化 | 步进 | 示例 |
| --- | --- | --- |
| 协议更新 | X1 + 1，清零 X2/X3 | 1.2.3 → 2.0.0 |
| 功能更新 | X2 + 1，清零 X3 | 1.2.3 → 1.3.0 |
| 缺陷修复 | X3 + 1 | 1.2.3 → 1.2.4 |

这是本项目的版本约定，不把“功能更新”自动等同于协议兼容。涉及 daemon wire contract 的变化按协议更新处理。
RPC 自身有历史上独立的计数：`engine/internal/daemon/rpc/types.go` 中的 `ProtocolVersion`。
协议级发布同时提升该 RPC major，并重新生成既有协议 schema；功能与修复发布不擅自改变 RPC 版本。
例如产品 0.1.0、RPC 1.1.0 的协议步进分别得到产品 1.0.0、RPC 2.0.0，并非两者必须字符串相等。
协议兼容代码与消费者必须先完成并通过验证；脚本不能替代协议设计和兼容性审查。

## 先预览，再执行

从干净且与远端一致的已审阅 `main` 发起。需要 Git、Python 3.11+、Go，以及本地验证所需工具。
客户端与 skill 不参与本仓库的发布验证。

```sh
# 默认只读预览：不构建、不联网、不写版本或 Git 引用
scripts/release-protocol.sh
scripts/release-feature.sh
scripts/release-fix.sh

# 完成验证，创建本地版本提交和注释标签；暂不推送
scripts/release-feature.sh --execute

# 一次调用：验证 → 版本提交 → tag → 原子推送 → 托管构建与发布
scripts/release-feature.sh --publish
```

每次选择一种类型，不要连续执行三条发布命令。Python 等价入口是
`python3 scripts/release.py --kind feature --publish`。
Mage 提供 `releaseProtocol / releaseFeature / releaseFix`（本地提交和 tag）与
`publishProtocol / publishFeature / publishFix`（包括推送）；均接受 `-dry-run`。
旧的 `releaseMajor / releaseMinor / releasePatch` 保留为协议/功能/修复的本地发布别名。

## 发布前的强制验证

脚本锁定本次本地操作，在仓库外创建可丢弃 checkout，生成候选版本后执行构建、vet、完整单元测试、
核心竞态测试、真实服务场景、Mage/Python 辅助测试与品牌一致性检查。
验证失败不修改日常工作区，不创建标签。没有跳过测试或强制发布选项。

验证通过后仍重新检查工作区、main 提交、origin/main 与标签，防止验证期间的并行工作被覆盖。
只允许候选中的版本与协议文件发生预期变化；其他生成或修改必须先被审查，真实错误不通过删断言、
放宽超时或自动无限重试处理。

推送使用 `git push --atomic`，main 和标签一起成功或一起失败，不使用 force；远端须为本项目，
分支保护拒绝时停止，不更改保护规则或绕过审查。
要求版本变更也走 PR 的仓库，应先按保护规则完成版本 PR，再由有权限的维护者创建相应注释标签。

## GitHub Actions 发布链路

`.github/workflows/release.yml` 对标签执行以下步骤：

1. 校验版本、协议 schema、注释标签及 main 归属，并运行发布策略测试。
2. 在 macOS/Linux 运行原生发布验证，复用 Windows 原生构建与事实/持久化检查。
3. 构建 Darwin/Linux/Windows 的 amd64 与 arm64 六个 CLI 压缩包，在 Linux 目标上生成
   amd64/arm64 的 `.deb` 与 `.rpm`。
4. 每个 CLI 包包含 `oberth`、可选 `jev-attention`、`install-path.sh`、构建清单、目标实际运行依赖的许可原文和 SHA256SUMS；Linux 安装包从同一个压缩包解出二进制与许可，不另建内容。
5. 验证全部目标齐全及散列后创建 GitHub draft release，上传，再下载校验并逐项比对上传前的本地散列；最后才转为正式 release。

Windows arm64 与部分 CLI 包是交叉构建；有下载包不等于已经验证所有平台生命周期。
标签工作流显式按触发提交 SHA checkout，并获取完整历史与标签对象，避免 ref + 已剥离提交的
回退 fetch 将 runner 本地的注释标签改成轻量引用。注释标签、版本一致性与 main 归属检查不放宽；
校验或发布失败后不移动远端标签，修复按下一版本交付。

## 失败与恢复

验证前失败：处理真实问题后重新发起，没有版本或 tag 需要清理。
本地提交/tag 已建立但原子推送失败：保留结果，先核对两端 refs，再决定如何重试；不要重写标签。
上传/校验失败：检查遗留 draft 和资产，不直接把失败的 draft 标为正式版。
网络中断可能造成客户端无法确认最终状态，必须读取远端核实，不能只根据本地报错推断“完全没发生”。
已经公开的版本不覆盖资产或移动标签；修复后按类别发布下一个版本。既有安装不会自动升级或停止用户服务。

## 原生 Windows 服务回归

Windows workflow 在构建 CLI 后运行 `verify_service_lifecycle.py`，使用临时 HOME/BERTH_HOME、
独立 named pipe、自己生成的 API 与无端口 worker。覆盖端口/运行身份、重复 up、采集器退出后的
只读状态、采集器恢复、限定项目 down，以及不相关监听保持存活。Windows 在启动阶段校验
夹具可执行文件并持有内核 process handle，之后按同一 handle 确认退出，不凭旧 PID 重选停止目标。
失败时只清理自己的临时夹具和显式启动的 daemon，所有夹具另有有限自退出期限。

这项检查不涵盖 Windows/Linux 桌面端、所有终端/权限/服务树场景，也不代表 Windows arm64
已经原生执行。

## 无可用前台 Mac 时的完整 CI 预检

完整的自动发布 CI 仅在推送与 `VERSION` 一致的 **`v*` 注释 tag** 时运行 `Verified release`
（验证、打包、发布）；PR 与 `main` 推送的自动门槛覆盖构建、vet、单元测试与辅助脚本测试，见
[validation.md](validation.md)。
发版前应在本机执行 `scripts/release-feature.sh` / `scripts/release-fix.sh` 等预览与 `--execute` 验证；
无前台 Mac 时，可先合入 VERSION PR，再对目标提交打 tag，由 tag 工作流在 runner 上执行与
`scripts/release.py --verify-only` 等价的完整门槛。失败时不移动或覆盖 tag，修复按下一版本交付。
