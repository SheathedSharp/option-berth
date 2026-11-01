# Install and uninstall

English | [中文](install.zh.md)

This page covers installing, upgrading and uninstalling the engine CLI only. The macOS client and the
coding-agent skill are independent products distributed from their own repositories.

## One-line script (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

The script resolves the latest release, picks darwin/linux and amd64/arm64 from `uname`, downloads
`option-berth-vX.Y.Z-<os>-<arch>.tar.gz`, verifies the same-named `.sha256`, then installs:

| Content | Location |
|---|---|
| Binaries | `~/.local/bin/oberth`, `~/.local/bin/jev-attention` |
| Licenses and build manifest | `~/.local/share/option-berth` |
| PATH | One line added to an existing shell startup file (never a new one) |
| Runtime state | `~/.option-berth` (the installer neither creates nor modifies it) |

Pin a version, change the directory, or leave PATH alone:

```sh
# Pin a version
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --version v1.0.0

# Change the install directory
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --dir "$HOME/tools/bin"

# Install only; do not modify shell startup files
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --no-modify-path
```

The installer starts no service, writes no Git state and needs no administrator rights. After
installing, run `oberth version` and `oberth doctor` to confirm.

### Slow downloads or network restrictions

`github.com`, `raw.githubusercontent.com` and `api.github.com` are reachable per domain, and the common
symptom is a connection timeout rather than an explicit error. The installer does not sit waiting on the
official host:

- It probes the official host for about 5 seconds and switches to the mirror list when unreachable;
- When the official host is reachable but transfer stays under 200KB/s for 15 seconds (mirrors: 50KB/s,
  20 seconds), it abandons that source for the next one;
- Checksums download before the archive; with a mirror, a second source must report the same SHA256
  before it continues;
- The default mirrors are `https://ghfast.top` and `https://gh-proxy.com`. Use your own with
  `BERTH_INSTALL_MIRRORS='https://your-proxy'`; set it to an empty string to use only the official host.

When the script itself cannot be fetched, jsDelivr serves the same file (content from this repository's
`main`):

```sh
curl -fsSL https://cdn.jsdelivr.net/gh/SheathedSharp/option-berth@main/install.sh | sh
```

The Windows installer supports the same mirror fallback, and can also be launched through a proxy entry:

```powershell
irm https://ghfast.top/https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

`go install` does not go through release assets, so a Go module proxy suffices and checksums are still
verified:

```sh
GOPROXY=https://goproxy.cn go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@latest
```

Setting only `GOPROXY` is enough: Go verifies the module through the proxy's `/sumdb/` endpoint. Do not
append `,direct` — when the proxy misses, Go falls back to a direct GitHub connection, which is the path
that times out; `GOSUMDB=off` is unnecessary too.

A third-party proxy can see download traffic and can be replaced. When you need a strict origin, pin the
version with `--version` and compare against the `SHA256SUMS` in Releases; release-archive signature
verification is not available yet.

## One-line script (Windows)

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

| Content | Location |
|---|---|
| Binaries | `%LOCALAPPDATA%\option-berth\bin\oberth.exe`, `jev-attention.exe` |
| Licenses and build manifest | `%LOCALAPPDATA%\option-berth` |
| PATH | The current user's PATH (the system PATH is never modified) |
| Runtime state | `%USERPROFILE%\.option-berth` |

Pin a version or leave PATH alone: `.\install.ps1 -Version v1.0.0 -NoModifyPath`.
Before replacing files, the Windows installer checks whether `oberth` / `jev-attention` are running; if
they are, it runs `oberth daemon stop` first and then reinstalls.

## go install (Go 1.25+)

```sh
go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@latest

# Optional Jev adapter
go install github.com/sheathedsharp/option-berth/engine/cmd/jev-attention@latest
```

This needs v1.1.2 or later. Pin a version with `@v1.1.2`. The binaries land in `$(go env GOPATH)/bin`
(or `GOBIN`), which must be on PATH.

## Homebrew

A self-hosted tap, so there is no need to wait for homebrew-core inclusion. It installs the engine CLI
(`oberth` and `jev-attention`), not the macOS client. The formula points at published archives, with
checksums recorded in the tap.

```sh
brew install sheathedsharp/tap/oberth
```

Upgrade with `brew upgrade oberth`. `brew install option-berth` still waits on homebrew-core inclusion,
which does not exist yet.

## Download a release manually

When you would rather not run a script, download the archive for your platform from
[Releases](https://github.com/SheathedSharp/option-berth/releases), verify `SHA256SUMS` first, then put
`oberth` and `jev-attention` in a directory on PATH:

```sh
# darwin-arm64 and v1.0.0 as an example
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
shasum -a 256 -c option-berth-v1.0.0-darwin-arm64.tar.gz.sha256
tar -xzf option-berth-v1.0.0-darwin-arm64.tar.gz
install -m 0755 option-berth-v1.0.0-darwin-arm64/oberth ~/.local/bin/oberth
install -m 0755 option-berth-v1.0.0-darwin-arm64/jev-attention ~/.local/bin/jev-attention
```

Keeping the archive's `LICENSE`, `licenses/` and `build-manifest.json` is recommended; a manual install
does not copy them for you.

## Linux packages (.deb / .rpm)

The release process produces `.deb` and `.rpm` for amd64/arm64 in the same release as the archives.
`v1.0.0` does not have these assets yet; they are available from v1.1.2:

```sh
# Debian / Ubuntu
sudo apt install ./option-berth_1.0.0_amd64.deb
sudo apt remove option-berth

# Fedora / RHEL
sudo dnf install ./option-berth-1.0.0-1.x86_64.rpm
sudo dnf remove option-berth
```

The packages put `oberth` and `jev-attention` in `/usr/bin` and the licenses and build manifest in
`/usr/share/doc/option-berth`. `apt remove` / `dnf remove` do not delete `~/.option-berth` runtime
state; once you are sure it is no longer needed, run `oberth daemon stop` and delete that directory.
arm64 uses the `_arm64.deb` and `.aarch64.rpm` file names.

## Uninstall

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh
```

Windows:

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.ps1 | iex
```

The uninstaller:

- Tries `oberth daemon stop` first, so no old process keeps running;
- Removes `oberth` and `jev-attention`;
- Removes the licenses and build manifest the installer placed in `~/.local/share/option-berth`;
- Removes the exact line the installer wrote into a shell startup file (only that line; other PATH
  configuration is untouched).

Binaries installed by `go install` are not managed by these uninstallers: delete
`$(go env GOPATH)/bin/oberth` and `jev-attention` directly; runtime state is handled as below.

By default it **keeps** `~/.option-berth` runtime state (database, logs, runs, configuration). When you
are sure it is no longer needed:

```sh
# macOS / Linux: remove runtime state along with the install
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh -s -- --purge-state
```

```powershell
# Windows
.\uninstall.ps1 -PurgeState
```

`--purge-state` / `-PurgeState` is irreversible; projects using `BERTH_HOME` should confirm that it
points at the directory being deleted. To keep only the PATH configuration, use
`--keep-path` / `-KeepPath`; for custom directories use `--dir` / `-Dir` and
`--data-dir` / `-DataDir`.

A manual uninstall is equivalent to:

```sh
oberth daemon stop
rm -f ~/.local/bin/oberth ~/.local/bin/jev-attention
rm -rf ~/.local/share/option-berth
# Remove runtime state only when needed
rm -rf ~/.option-berth
```

Then delete the PATH line the installer added from your shell startup file.

## Upgrade

Rerun the install command for your platform to replace the binaries; after upgrading, run
`oberth daemon stop` once, and the next CLI call starts the new version. On Windows, stop the running
`oberth` first, then reinstall. Pin a version with `--version` / `-Version`.

`oberth`'s first screen shows the local version; when an update exists, its footer gives the install
command for this platform and `oberth daemon stop`. You can also run `oberth version --check` at any
time. The automatic check reads GitHub release tags only, caches success for 5 minutes and failure for
30 minutes, and downloads or installs nothing; `BERTH_NO_UPDATE_CHECK=1` disables the automatic check
on the first screen. When the network is unreachable, the first screen shows only the local version and
never presents it as the latest.

## Channels not yet available

- **Homebrew core**: `brew install option-berth` needs homebrew-core inclusion, which is not available
  yet. The self-hosted tap is described in the Homebrew section above.
- **Scoop**: needs a separate bucket repository, which this repository does not maintain.
- **winget**: needs a submission to microsoft/winget-pkgs and review for every version update.
- **Chocolatey**: needs a community package repository and review, similar to winget.

The currently available paths are the scripts above, the Homebrew tap, manual release downloads and the
Linux `.deb`/`.rpm`. A Windows MSI is not available yet.

## Platform boundaries

Full service scenarios are verified on **macOS / Linux**. Windows has an independent native build, smoke
tests and some fact/persistence tests, but is not full lifecycle support. No platform provides an
auto-upgrade daemon or a background updater.
