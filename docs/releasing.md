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

## 发布节奏

合入 `main` 的功能按周发布一个功能版本（X2），不把多次功能改动积到一次。
支持范围内的误停、归属错误、账本损坏，以及服务已在监听却被当成未就绪，按修复版本（X3）单独发布，不等周窗口。
不兼容的 daemon 协议变更才升 X1。

## 发布路径

从干净且与远端一致的已审阅 `main` 发起。需要 Git、Python 3.11+、Go，以及本地验证所需工具。
客户端与 skill 不参与本仓库的发布验证。

发布只有一条路径：**版本 PR → 合并 → 推注释标签 → `Verified release` 在 runner 上验证并发布**。
本机不再有创建版本提交、打标签或推送的快捷入口；`scripts/release.py` 只保留 CI 用的
`--verify-only` 和 `--validate-tag`。

1. 从 `main` 开分支，只改 `VERSION`（协议版本发布同时改 `ProtocolVersion` 并重新生成 schema），
   提交 `chore(release): vX.Y.Z`，开 PR。
2. PR 的 required checks 通过并经确认后，用 merge commit 合并。
3. 在合并提交上创建注释标签 `vX.Y.Z` 和 `engine/vX.Y.Z`，推送标签。
4. `Verified release` 按标签执行 `policy` / `verify` / `windows` / `package` / `publish`，
   在 runner 上跑与 `scripts/release.py --verify-only` 相同的门槛，通过后才构建资产并把
   draft 转正。

本地只读检查：

```sh
python3 scripts/release.py --verify-only          # 与 runner 相同的完整门槛
python3 scripts/release.py --validate-tag vX.Y.Z  # 核对已存在的标签
```

这两条不写版本、不建标签、不推送。标签一旦推送就不再移动；失败时修复后按下一个版本交付。

## 发布前的强制验证

`Verified release` 在标签提交上执行构建、vet、完整单元测试、核心竞态测试、真实服务场景、
Mage/Python 辅助测试与品牌一致性检查；任一项失败都不会构建或发布资产。
没有跳过测试或强制发布的选项。

### 发布前的 agent 场景矩阵

固定场景库的入口是 `scripts/agent_eval.py`，契约为 [agent-eval.md](agent-eval.md)。
发布候选构建应在一台能正常观察监听的普通终端（或具备凭据的 CI）上跑
`--execute --repeat 2`，把 `report.md` 作为发布证据附上；`--require-comparable`
用于确认同一场景确实重复跑过且来自同一构建。

真实 agent 依赖外部 CLI、登录状态和网络，这一步**不放进** `scripts/release.py` 的强制
验证，也不因为它失败而阻止引擎发布。没有执行的 agent 行必须在证据里写「未执行」；
参考脚本和 fake engine 的结果不能替代真实 agent。

工作流校验版本、协议 schema、注释标签与 main 归属后才继续。真实错误不通过删断言、
放宽超时或自动无限重试处理。

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

## 打标签前的预检

完整的自动发布 CI 仅在推送与 `VERSION` 一致的 **`v*` 注释 tag** 时运行 `Verified release`
（验证、打包、发布）；PR 与 `main` 推送的自动门槛覆盖构建、vet、单元测试与辅助脚本测试，见
[validation.md](validation.md)。每日 `Nightly full verification` 在默认分支上运行与发版等价的
完整门槛；打标签前应先确认它通过，避免把只在该门槛暴露的问题留到发版后。
需要在本机复跑时用 `python3 scripts/release.py --verify-only`；它只读当前检出的提交。
失败时不移动或覆盖 tag，修复按下一版本交付。
