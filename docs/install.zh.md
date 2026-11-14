# 安装与卸载

[English](install.md) | 中文

本页只讲引擎 CLI 的安装、升级与卸载。macOS 客户端与 coding agent skill 是独立产品，
各自在自己的仓库分发。

协议 3 是后台接口的破坏性切换。替换旧安装前，先用匹配旧后台的 CLI 正常停止它。新命令不能停止旧端点；不要删除锁、socket 或数据来强起第二个实例。持久数据仍保存在同一根目录和数据库。见[切换合同](cli.md#runtime-service-protocol-3-switch)。

## 一行脚本（macOS / Linux）

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

脚本解析最新 Release，按 `uname` 选择 darwin/linux 与 amd64/arm64，下载
`option-berth-vX.Y.Z-<os>-<arch>.tar.gz`，核对同名 `.sha256` 后安装：

| 内容 | 位置 |
|---|---|
| 二进制 | `~/.local/bin/oberth` |
| 许可与构建清单 | `~/.local/share/option-berth` |
| PATH | 已有的 shell 启动文件里加一行（不新建启动文件） |
| 运行状态 | `~/.option-berth`（安装器不创建、不改动） |

固定版本、换目录或不动 PATH：

```sh
# Pin a version
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --version v1.0.0

# Change the install directory
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --dir "$HOME/tools/bin"

# Install only; do not modify shell startup files
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

第三方代理能看到下载流量，也可能被替换。需要严格来源时用 `--version` 固定版本，并对照
Releases 里的 `SHA256SUMS`；发布包签名校验尚未提供。

## 一行脚本（Windows）

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

| 内容 | 位置 |
|---|---|
| 二进制 | `%LOCALAPPDATA%\option-berth\bin\oberth.exe` |
| 许可与构建清单 | `%LOCALAPPDATA%\option-berth` |
| PATH | 当前用户的 PATH（不改系统 PATH） |
| 运行状态 | `%USERPROFILE%\.option-berth` |

固定版本或不改 PATH：`.\install.ps1 -Version v1.0.0 -NoModifyPath`。
Windows 安装器在替换文件前会检查 `oberth` 是否正在运行；
正在运行就先执行 `oberth runtime service stop`，再重新安装。

## 手动下载发布包

不想执行脚本时，从 [Releases](https://github.com/SheathedSharp/option-berth/releases)
下载对应平台的压缩包，先核对 `SHA256SUMS`，再把 `oberth` 放到 PATH 中的目录：

```sh
# darwin-arm64 and v1.0.0 as an example
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
shasum -a 256 -c option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
tar -xzf option-berth-v1.0.0-darwin-arm64.tar.gz
install -m 0755 option-berth-v1.0.0-darwin-arm64/oberth ~/.local/bin/oberth
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

安装包把 `oberth` 放到 `/usr/bin`，许可与构建清单放到
`/usr/share/doc/option-berth`。`apt remove` / `dnf remove` 不删除 `~/.option-berth` 运行状态；
确认不再需要后再执行 `oberth runtime service stop` 并删除该目录。arm64 使用 `_arm64.deb` 与
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

- 尽量先执行 `oberth runtime service stop`，避免留下正在运行的旧进程；
- 删除 `oberth`；
- 删除安装器放在 `~/.local/share/option-berth` 的许可与构建清单；
- 删除安装器写进 shell 启动文件的那一行（只删自己写的精确行，不动其他 PATH 配置）。

默认**保留** `~/.option-berth` 运行状态（数据库、日志、runs、配置）。确认不再需要时：

```sh
# macOS / Linux: remove runtime state along with the install
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
oberth runtime service stop
rm -f ~/.local/bin/oberth
rm -rf ~/.local/share/option-berth
# Remove runtime state only when needed
rm -rf ~/.option-berth
```

然后从 shell 启动文件里删掉安装器加的那一行 PATH。

## 升级

重新执行对应平台的安装命令即可替换二进制；升级后执行一次 `oberth runtime service stop`，下一次
CLI 调用会启动新版本。Windows 先停止正在运行的 `oberth`，再重新安装。需要固定版本时使用
`--version` / `-Version`。

`oberth` 首屏会显示本机版本；有更新时底部直接给出本平台的安装命令和 `oberth runtime service stop`。
其他输出给人看的终端命令也会以同样的两行升级提示收尾，同一发布只提示一次。
也可以随时运行 `oberth version --check` 主动检查。自动检查只读发布元数据，
成功结果缓存 5 分钟、失败结果缓存 30 分钟，单次命令最多等待 1 秒，不下载、不安装任何东西；
`BERTH_NO_UPDATE_CHECK=1` 可关闭自动提示。
所有发布来源都不可达时，终端路径只显示本机版本，不把它当作最新版。

## 尚未提供的渠道

- **Scoop**：需要单独的 bucket 仓库，本仓库不维护。
- **winget**：需要向 microsoft/winget-pkgs 提交并过审，每个版本更新。
- **Chocolatey**：需要社区包仓库与审核，和 winget 类似。

当前可用方式见上面的脚本、手动发布包与 Linux `.deb`/`.rpm`。Windows MSI 暂未提供。

## 平台边界

完整服务场景在 **macOS / Linux** 验证。Windows 有独立原生构建、冒烟和部分事实/持久化测试，
但不是完整生命周期支持。各平台都不提供自动升级守护进程或后台更新器。
