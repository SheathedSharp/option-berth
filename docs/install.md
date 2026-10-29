# 安装与卸载

本页只讲引擎 CLI 的安装、升级与卸载。macOS 客户端与 coding agent skill 是独立产品，
各自在自己的仓库分发。

## 一行脚本（macOS / Linux）

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

脚本解析最新 Release，按 `uname` 选择 darwin/linux 与 amd64/arm64，下载
`option-berth-vX.Y.Z-<os>-<arch>.tar.gz`，核对同名 `.sha256` 后安装：

| 内容 | 位置 |
|---|---|
| 二进制 | `~/.local/bin/oberth`、`~/.local/bin/jev-attention` |
| 许可与构建清单 | `~/.local/share/option-berth` |
| PATH | 已有的 shell 启动文件里加一行（不新建启动文件） |
| 运行状态 | `~/.option-berth`（安装器不创建、不改动） |

固定版本、换目录或不动 PATH：

```sh
# 固定版本
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --version v1.0.0

# 换安装目录
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --dir "$HOME/tools/bin"

# 只下载安装，不改 shell 启动文件
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --no-modify-path
```

安装器不启动服务、不写 Git、不需要管理员权限。安装后可以运行 `oberth version` 与
`oberth doctor` 确认。

## 一行脚本（Windows）

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

| 内容 | 位置 |
|---|---|
| 二进制 | `%LOCALAPPDATA%\option-berth\bin\oberth.exe`、`jev-attention.exe` |
| 许可与构建清单 | `%LOCALAPPDATA%\option-berth` |
| PATH | 当前用户的 PATH（不改系统 PATH） |
| 运行状态 | `%USERPROFILE%\.option-berth` |

固定版本或不改 PATH：`.\install.ps1 -Version v1.0.0 -NoModifyPath`。
Windows 安装器在替换文件前会检查 `oberth` / `jev-attention` 是否正在运行；
正在运行就先执行 `oberth daemon stop`，再重新安装。

## 手动下载发布包

不想执行脚本时，从 [Releases](https://github.com/SheathedSharp/option-berth/releases)
下载对应平台的压缩包，先核对 `SHA256SUMS`，再把 `oberth` 与 `jev-attention` 放到 PATH 中的目录：

```sh
# 以 darwin-arm64、v1.0.0 为例
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
shasum -a 256 -c option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
tar -xzf option-berth-v1.0.0-darwin-arm64.tar.gz
install -m 0755 option-berth-v1.0.0-darwin-arm64/oberth ~/.local/bin/oberth
install -m 0755 option-berth-v1.0.0-darwin-arm64/jev-attention ~/.local/bin/jev-attention
```

压缩包内的 `LICENSE`、`licenses/` 与 `build-manifest.json` 建议保留；手动安装时它们不会自动复制。

## 卸载

macOS / Linux：

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh
```

Windows：

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.ps1 | iex
```

卸载器会：

- 尽量先执行 `oberth daemon stop`，避免留下正在运行的旧进程；
- 删除 `oberth` 与 `jev-attention`；
- 删除安装器放在 `~/.local/share/option-berth` 的许可与构建清单；
- 删除安装器写进 shell 启动文件的那一行（只删自己写的精确行，不动其他 PATH 配置）。

默认**保留** `~/.option-berth` 运行状态（数据库、日志、runs、配置）。确认不再需要时：

```sh
# macOS / Linux：连同运行状态一起删除
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh -s -- --purge-state
```

```powershell
# Windows
.\uninstall.ps1 -PurgeState
```

`--purge-state` / `-PurgeState` 不可恢复；使用 `BERTH_HOME` 的项目请确认它指向的就是要删除的目录。
只想保留 PATH 配置时用 `--keep-path` / `-KeepPath`；自定义目录用 `--dir` / `-Dir` 与
`--data-dir` / `-DataDir`。

手动卸载等价于：

```sh
oberth daemon stop
rm -f ~/.local/bin/oberth ~/.local/bin/jev-attention
rm -rf ~/.local/share/option-berth
# 需要时再删除运行状态
rm -rf ~/.option-berth
```

然后从 shell 启动文件里删掉安装器加的那一行 PATH。

## 升级

重新执行对应平台的安装命令即可替换二进制：

- macOS / Linux 用临时文件加 `mv` 原子替换；正在运行的 daemon 会继续用旧二进制，直到重启。
  升级后执行一次 `oberth daemon stop`，下一次 CLI 调用会启动新版本。
- Windows 会先检查进程并拒绝在运行中替换；先 `oberth daemon stop`，再重新安装。

安装器只读取已发布标签，不会跟踪 `main` 上的未发布代码；需要固定版本时使用 `--version` /
`-Version`。

## 包管理器渠道

Homebrew、Scoop、winget 都是主流包管理器，但它们分别要求先发布 Homebrew formula（tap 仓库）、
Scoop manifest（bucket 仓库）或通过 microsoft/winget-pkgs 审核。**本仓库目前没有发布这些渠道**，
因此现在不提供 `brew install`、`scoop install` 或 `winget install` 命令；在这些外部仓库建立前，
请使用上面的一行脚本或手动下载发布包。这是当前真实缺口，不用脚本冒充包管理器安装。

## 平台边界

完整服务场景在 **macOS / Linux** 验证。Windows 有独立原生构建、冒烟和部分事实/持久化测试，
但 Windows 安装器与卸载器的实际 PATH 行为仍需 Windows 原生验证；仓库 CI 目前只检查 PowerShell
脚本能解析。各平台都不提供自动升级守护进程或后台更新器。
