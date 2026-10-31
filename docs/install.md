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

### 下载慢或被网络限制

`github.com`、`raw.githubusercontent.com` 与 `api.github.com` 按域名分别可达，常见表现是
连接超时，而不是明确的报错。安装器不会在官方地址上死等：

- 先用约 5 秒探测官方主机，不可达时直接改用镜像列表；
- 官方主机可用但传输持续低于 200KB/s 达 15 秒（镜像是 50KB/s、20 秒），放弃该源换下一个；
- 校验和先于压缩包下载；使用镜像时要求第二个源报告相同的 SHA256 才继续；
- 默认镜像是 `https://ghfast.top` 与 `https://gh-proxy.com`。用自己的代理：
  `BERTH_INSTALL_MIRRORS='https://your-proxy'`；设成空串则只用官方地址。

脚本本身取不到时，可以从 jsDelivr 取同一份文件（内容来自本仓库 `main`）：

```sh
curl -fsSL https://cdn.jsdelivr.net/gh/SheathedSharp/option-berth@main/install.sh | sh
```

Windows 安装器同样支持镜像回退，也可以用代理入口启动：

```powershell
irm https://ghfast.top/https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

`go install` 不经过 Release 资产，改用 Go 模块代理即可，校验和仍会核对：

```sh
GOPROXY=https://goproxy.cn go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@latest
```

只设 `GOPROXY` 就够了：Go 会通过代理的 `/sumdb/` 端点校验模块。不要追加 `,direct`，
代理未命中时会回落到直连 GitHub，也就是超时的那条路径；也不需要 `GOSUMDB=off`。

第三方代理能看到下载流量，也可能被替换。需要严格来源时用 `--version` 固定版本，并对照
Releases 里的 `SHA256SUMS`；发布包签名校验尚未提供。

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

## go install（Go 1.25+）

```sh
go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@latest

# 可选的 Jev adapter
go install github.com/sheathedsharp/option-berth/engine/cmd/jev-attention@latest
```

需要 v1.1.2 或更新版本。固定版本用 `@v1.1.2`。二进制进 `$(go env GOPATH)/bin`（或 `GOBIN`），
该目录需要在 PATH 中。

## Homebrew

自建 tap，不需要等 homebrew-core 收录。它安装引擎 CLI（`oberth` 和 `jev-attention`），
不是 macOS 客户端。formula 指向已发布的压缩包，校验和写在 tap 里。

```sh
brew install sheathedsharp/tap/oberth
```

升级用 `brew upgrade oberth`。`brew install option-berth` 仍然要等 homebrew-core 收录，现在没有。

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

## Linux 安装包（.deb / .rpm）

发布流程会为 amd64/arm64 生成 `.deb` 与 `.rpm`，和压缩包放在同一个 Release。`v1.0.0` 还没有
这些资产，从 v1.1.2 开始提供：

```sh
# Debian / Ubuntu
sudo apt install ./option-berth_1.0.0_amd64.deb
sudo apt remove option-berth

# Fedora / RHEL
sudo dnf install ./option-berth-1.0.0-1.x86_64.rpm
sudo dnf remove option-berth
```

安装包把 `oberth`、`jev-attention` 放到 `/usr/bin`，许可与构建清单放到
`/usr/share/doc/option-berth`。`apt remove` / `dnf remove` 不删除 `~/.option-berth` 运行状态；
确认不再需要后再执行 `oberth daemon stop` 并删除该目录。arm64 使用 `_arm64.deb` 与
`.aarch64.rpm` 文件名。

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

`go install` 安装的二进制不由这些卸载器管理：直接删除 `$(go env GOPATH)/bin/oberth` 与
`jev-attention`；运行状态的处理与下面相同。

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

重新执行对应平台的安装命令即可替换二进制；升级后执行一次 `oberth daemon stop`，下一次
CLI 调用会启动新版本。Windows 先停止正在运行的 `oberth`，再重新安装。需要固定版本时使用
`--version` / `-Version`。

`oberth` 首屏会显示本机版本；有更新时底部直接给出本平台的安装命令和 `oberth daemon stop`。
也可以随时运行 `oberth version --check` 主动检查。自动检查只读 GitHub 的发布标签，
成功结果缓存 5 分钟、失败结果缓存 30 分钟，不下载、不安装任何东西；
`BERTH_NO_UPDATE_CHECK=1` 可关闭首屏的自动检查。
网络不可达时首屏只显示本机版本，不把它当作最新版。

## 尚未提供的渠道

- **Homebrew core**：`brew install option-berth` 需要 homebrew-core 收录，暂未提供。自建 tap 见上面的 Homebrew 一节。
- **Scoop**：需要单独的 bucket 仓库，本仓库不维护。
- **winget**：需要向 microsoft/winget-pkgs 提交并过审，每个版本更新。
- **Chocolatey**：需要社区包仓库与审核，和 winget 类似。

当前可用方式见上面的脚本、Homebrew tap、手动发布包与 Linux `.deb`/`.rpm`。Windows MSI 暂未提供。

## 平台边界

完整服务场景在 **macOS / Linux** 验证。Windows 有独立原生构建、冒烟和部分事实/持久化测试，
但不是完整生命周期支持。各平台都不提供自动升级守护进程或后台更新器。
