# 发布流程 / Release process

## 版本含义

产品版本为 **X1.X2.X3**，根目录 `VERSION` 是产品版本的唯一来源，不加 `v`。
Git 标签使用 `vX1.X2.X3`，必须为注释标签，指向已经验证且属于 `main` 历史的提交。

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
macOS 客户端构建还需要对应 Swift 工具链，详见 [客户端说明](../client/macos/README.md)。

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
核心竞态测试、真实服务场景、Mage/Python 辅助测试、品牌一致性，以及 macOS 上的客户端构建、原生 PTY 和实际 CLI→agent 界面交接回归（外部 provider 使用临时替身，不调用模型）。
验证失败不修改日常工作区，不创建标签。没有跳过测试或强制发布选项。

验证通过后仍重新检查工作区、main 提交、origin/main 与标签，防止验证期间的并行工作被覆盖。
只允许候选中的版本与协议文件发生预期变化；其他生成或修改必须先被审查。
代码与环境中的真实错误不通过删断言、放宽超时或自动无限重试处理。

推送使用 `git push --atomic`，main 和标签一起成功或一起失败，不使用 force。
远端需为本项目，发布者必须有正常写入权限；分支保护拒绝时停止，不更改保护规则或绕过审查。
要求版本变更也走 PR 的仓库，应先按保护规则完成版本 PR，再由有权限的维护者创建相应注释标签。

## GitHub Actions 发布链路

`.github/workflows/release.yml` 对标签执行以下步骤：

1. 校验版本、协议 schema、注释标签及 main 归属，并运行发布策略测试。
2. 在 macOS/Linux 运行原生发布验证，复用 Windows 原生构建与事实/持久化检查。
3. 构建 Darwin/Linux/Windows 的 amd64 与 arm64 CLI 压缩包；macOS arm64 额外生成应用包。
4. 每个 CLI 包包含 `oberth`、可选 `jev-attention`、构建清单、目标实际运行依赖的许可原文和 SHA256SUMS。
5. 验证全部目标齐全及散列后创建 GitHub draft release，上传，再下载校验并逐项比对上传前的本地散列；最后才转为正式 release。

Mac 应用包携带同一提交构建的 `oberth`，优先使用随包引擎；显式 `BERTH_BIN` 仍可覆盖。
应用和引擎的版本/提交身份必须一致，避免只升级界面而悄悄调用另一份旧引擎。
Windows arm64 与部分 CLI 包是交叉构建；有下载包不等于已经验证所有平台生命周期。

**当前 macOS 包只有 ad-hoc 签名，没有 Developer ID 签名或公证。** 文件名与发布说明明确标记 `adhoc`。
它不等于受 Apple 信任的安装包；不提供关闭 Gatekeeper 的安装建议。Developer ID/公证需要维护者另行
配置证书主体与凭证并完成验收，本流程不会创建证书、上传个人密钥或伪称已经完成这一步。

标签工作流显式按触发提交 SHA checkout，并获取完整历史与标签对象，避免 ref + 已剥离提交的
回退 fetch 将 runner 本地的注释标签改成轻量引用。注释标签、版本一致性与 main 归属检查不放宽；
校验或发布失败后不移动远端标签，修复按下一版本交付。

## 失败与恢复

验证前失败：处理真实问题后重新发起，没有版本或 tag 需要清理。
本地提交/tag 已建立但原子推送失败：保留结果，先核对两端 refs，再决定如何重试；不要重写标签。
上传/校验失败：检查遗留 draft 和资产，不直接把失败的 draft 标为正式版。
网络中断可能造成客户端无法确认最终状态，必须读取远端核实，不能只根据本地报错推断“完全没发生”。
已经公开的版本不覆盖资产或移动标签；修复后按类别发布下一个版本。既有安装不会自动升级或停止用户服务。

## English quick reference

`VERSION` follows **protocol.feature.fix**. Higher-component increments reset lower components.
Protocol releases also advance the independently versioned daemon protocol major and regenerate its existing schema.
This does not make an incompatible consumer compatible: all implementation and compatibility checks must pass first.

The shell scripts default to a read-only plan. `--execute` creates a verified local version commit and annotated tag;
`--publish` also atomically pushes main and the tag. Run from clean, reviewed main matching the expected origin.
There is no force-publish or skip-verification flag. Validation uses a disposable checkout and does not overwrite
concurrent user changes. Existing tags are immutable.

Tag CI verifies native platforms, packages six CLI targets plus the macOS ARM64 app, includes build identities and
licenses, verifies SHA-256, and publishes a draft only after uploaded assets are downloaded and rechecked.
The application embeds its matching engine. Current macOS artifacts are **ad-hoc signed, not notarized**;
Developer ID distribution remains an explicitly separate credential-dependent step.


## 显式 Developer ID / Apple 公证

默认发布仍使用 `adhoc`，不会读取、导入或创建开发者凭证。具备已有 Developer ID 证书和
notarytool Keychain profile 的授权 macOS runner，可在明确选定的打包操作中使用：

```sh
python3 scripts/package_release.py --os darwin --arch arm64 --include-app --output dist \
  --macos-signing developer-id --signing-identity '<certificate SHA1>' \
  --team-id '<10-character Team ID>' --notary-profile '<existing profile>'
```

代码先校验参数，逐个签署嵌套 Mach-O 和应用包并启用 hardened runtime、timestamp，再核对
TeamIdentifier。只有 notarytool 返回 `Accepted`、staple/validate、严格签名验证和 Gatekeeper
检查全部成功后，再生成外层 DMG 并对镜像执行签名、公证、staple/validate 和 Gatekeeper open 检查；全部通过才生成 `-notarized.dmg`；任何失败都不降级伪装成已公证。原始构建应用不会被
修改，签名发生在暂存副本。`--deep` 仅用于验证，不作为隐式递归签名策略。

发布资产校验默认只接受 ad-hoc 资产集合。发布经过上述完整流程产生的公证包时，须显式使用
`publish_release.py --macos-trust notarized`。这项参数只选定预期资产名称集合，不独立证明
签名、公证或下载完整性；原有下载回验与 SHA256 对照仍执行。CI 默认不自动使用开发者账号，
证书/公证环境尚未配置或实机验收时，不能把准备好的脚本写成已完成真实公证。

依据：[Apple 自定义公证工作流](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)。
`macos_release_signing_test.py` 通过注入命令响应验证拒绝、正确顺序、Team ID 不符和各关卡失败；
隔离临时 Mach-O 的真实 ad-hoc 签名另行验证。模拟 Accepted 结果不等于向 Apple 实际提交。

## 原生 Windows 服务回归

Windows workflow 在构建 CLI 后运行 `verify_service_lifecycle.py`，使用临时 HOME/BERTH_HOME、
独立 named pipe、自己生成的 API 与无端口 worker。覆盖端口/运行身份、重复 up、采集器退出后的
只读状态、采集器恢复、限定项目 down，以及不相关监听保持存活。Windows 在启动阶段校验
夹具可执行文件并持有内核 process handle，之后按同一 handle 确认退出，不凭旧 PID 重选停止目标。
失败时只清理自己的临时夹具和显式启动的 daemon，所有夹具另有有限自退出期限。

这项检查不涵盖 Windows/Linux 桌面端、所有终端/权限/服务树场景，也不代表 Windows arm64
已经原生执行。客户端升级入口只检查公开发布信息，自动替换安装与状态迁移仍须单独验收。


## macOS 安装容器

新客户端使用 DMG（[安装与替换](macos-install.md)）。打包器验证只读挂载、Applications 拖放入口、复制后的签名、内置引擎身份和许可。完成全部关卡及自有挂载清理后才暴露最终资产。CLI 压缩包不变，旧 Release 的 ZIP 不覆盖。PR 的 macOS 原生验证不等于发布。


## 无可用前台 Mac 时的完整 CI 预检

`Verified release` 支持在 **main** 上显式 `workflow_dispatch`，运行与 tag 完全相同的
Linux/macOS 原生 `scripts/release.py --verify-only` 及 Windows 门槛。此入口仅验证，
不更改版本、创建 tag、打包或发布；非 main 调用拒绝，不接受任意脚本/跳过检查输入。

```sh
gh workflow run release.yml --ref main
```

需要从 CI 完成发版时：先以单独 PR 提交 VERSION 更新并合入 main；对该精确 SHA 运行上述完整预检，
逐项确认所有必需 verify/Windows/policy 作业成功而非跳过，并确认 main 没有移动。
然后只给这个已验证的 SHA 创建与 VERSION 一致的**注释 tag**，非强制推送。tag 触发的既有工作流
会再次执行完整验证，全部成功后才进行包构建、下载回验和公开发布。失败时不重用/覆盖 tag，不把
普通 PR policy 的成功当作完整验证。此路径不需要更改本机录屏/辅助功能权限或跳过原生检查。
