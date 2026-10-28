# macOS 安装与替换 / Install and replace

新客户端分发格式为 `OptionBerth-v<版本>-macos-arm64-adhoc.dmg`。
`notarized.dmg` 只用于应用和外层镜像均经过显式 Developer ID 签名、Apple 公证与验收的构建。
DMG 只是安装容器，不使 ad-hoc 签名变成 Apple 信任的签名；不要关闭 Gatekeeper。
已经发布的 ZIP 不覆盖或改名；是否已有 DMG，以 Release 实际资产为准。

## 安装与替换

先核对官方 Release 的 `SHA256SUMS` 或同名 `.sha256`，打开 DMG，将 `OptionBerth.app`
拖到同一窗口的 `Applications` 入口。弹出镜像，从 Applications 启动，不在镜像中长期运行。
应用包含相同版本、相同提交构建的 `oberth`，首次打开完整 GUI 不需要先手动运行 `oberth status`。

升级前结束应用拥有的活动终端/Agent 会话并退出旧应用，拖入新应用，在 Finder 确认“替换”。
替换的是完整 `.app`（包括内置引擎），不在原应用内混放新旧文件。
它不会删除项目/状态目录，不替换独立安装在 `~/.local/bin`、Homebrew 等目录的 CLI，
也不自动重启已运行的 daemon。外置 CLI 用户应另行升级对应安装。

显式 `BERTH_BIN` 优先于随包引擎；未设置时完整应用优先使用内置引擎。
替换磁盘文件不等于替换已运行的后台。可以用选定引擎的只读命令核对身份：

```sh
/Applications/OptionBerth.app/Contents/MacOS/oberth version --json
/Applications/OptionBerth.app/Contents/MacOS/oberth daemon status --json
```

后者不会启动后台；`daemon_version`、`build_commit`、`build_matches` 用于比较身份。
切换后台版本前先阅读迁移说明并确认服务/会话状态，再显式安排重启。安装盘不会替用户做这个决定。
不要因存在 `daemon.sock` 就自行删除 socket 或强杀进程。

## English

Verify release checksums, open the DMG and drag **OptionBerth.app** to **Applications**.
Eject the image and launch the installed copy. End the old app's owned active sessions and quit before
confirming Finder's Replace operation. The whole bundle includes its matching engine.

Bundle replacement does **not** upgrade independently installed CLI binaries, delete workspace data,
or restart a running daemon. An explicit `BERTH_BIN` takes precedence; otherwise the bundled engine wins.
Use the read-only commands above to inspect build identities. Review migrations and active services before
an explicit daemon restart. Do not disable Gatekeeper to treat an ad-hoc image as a notarized one.

Existing ZIP releases remain immutable and discoverable by the update checker; new releases require DMG.
Development CI verification is not a public release.
