# Install and uninstall

English | [中文](install.zh.md)

## Choose an installation method

The script and manual-package methods below are alternatives, not a sequence. First-time users should install the CLI, check `oberth version --json`, then follow the [quick start](quickstart.md). Existing users should read the protocol-switch warning before replacing anything. Installation does not start project services. For failures, use [troubleshooting](troubleshooting.md).

This page covers installing, upgrading and uninstalling the engine CLI only. The coding-agent skill is an
independent product distributed from its own repository.

Protocol 3 is a breaking host-interface switch. Before replacing an older installation, use that older host's matching CLI to stop it normally. New commands cannot stop old endpoints; do not delete locks, sockets or data to force a second instance. Persistent data stays in the same home/database. See [the switch contract](cli.md#runtime-service-protocol-3-switch).

## One-line script (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

The script resolves the latest release, picks darwin/linux and amd64/arm64 from `uname`, downloads
`option-berth-vX.Y.Z-<os>-<arch>.tar.gz`, verifies the same-named `.sha256`, then installs:

| Content | Location |
|---|---|
| Binaries | `~/.local/bin/oberth` |
| Licenses and build manifest | `~/.local/share/option-berth` |
| PATH | One line added to an existing shell startup file (never a new one) |
| Runtime state | `~/.option-berth` (on macOS created for the unit's log; otherwise untouched) |

Pin a version, change the directory, or leave PATH alone:

```sh
# Pin a version
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --version v3.0.0

# Change the install directory
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --dir "$HOME/tools/bin"

# Install only; do not modify shell startup files
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh -s -- --no-modify-path
```

The installer writes no Git state and needs no administrator rights. On macOS and Linux it also
registers the user-level runtime service described below; `--no-service` skips that. After installing,
run `oberth version` and `oberth doctor` to confirm.

### The runtime service unit

Once `oberth version` runs, the installer registers the runtime service as a user-level unit and starts
it: a launchd LaunchAgent (`~/Library/LaunchAgents/com.sheathedsharp.option-berth.runtime.plist`) on
macOS, a systemd user unit (`~/.config/systemd/user/option-berth-runtime.service`) on Linux. The unit
runs `oberth runtime service run --persistent`, so one runtime service stays up across logins instead of
being started ad hoc by whichever command needed one; its lifecycle belongs to
[the runtime service page](runtime-service.md).

- Registration happens only when `oberth doctor --only listener_observation` exits 0: a machine that
  cannot see listeners should not get a permanent runtime service that cannot see them either;
- `--no-service` or `BERTH_INSTALL_NO_SERVICE=1` skips it; an existing unit is then left running, and the
  installer says so;
- a missing or refusing supervisor is a warning: the unit file stays and is loaded at the next login;
- nothing here needs root, and an unavailable supervisor does not fail the install.

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

A third-party proxy can see download traffic and can be replaced. When you need a strict origin, pin the
version with `--version` and compare against the `SHA256SUMS` in Releases; release-archive signature
verification is not available yet.

## One-line script (Windows)

```powershell
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex
```

| Content | Location |
|---|---|
| Binaries | `%LOCALAPPDATA%\option-berth\bin\oberth.exe` |
| Licenses and build manifest | `%LOCALAPPDATA%\option-berth` |
| PATH | The current user's PATH (the system PATH is never modified) |
| Runtime state | `%USERPROFILE%\.option-berth` |

Pin a version or leave PATH alone: `.\install.ps1 -Version v3.0.0 -NoModifyPath`.
Before replacing files, the Windows installer checks whether `oberth` is running; if it is, it runs `oberth runtime service stop` first and then reinstalls.

## Download a release manually

When you would rather not run a script, download the archive for your platform from
[Releases](https://github.com/SheathedSharp/option-berth/releases), verify `SHA256SUMS` first, then put
`oberth` in a directory on PATH:

```sh
# darwin-arm64 and v3.0.0 as an example
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v3.0.0/option-berth-v3.0.0-darwin-arm64.tar.gz
curl -fLO https://github.com/SheathedSharp/option-berth/releases/download/v3.0.0/option-berth-v3.0.0-darwin-arm64.tar.gz.sha256
shasum -a 256 -c option-berth-v3.0.0-darwin-arm64.tar.gz.sha256
tar -xzf option-berth-v3.0.0-darwin-arm64.tar.gz
install -m 0755 option-berth-v3.0.0-darwin-arm64/oberth ~/.local/bin/oberth
```

Keeping the archive's `LICENSE`, `licenses/` and `build-manifest.json` is recommended; a manual install
does not copy them for you.

## Linux packages (.deb / .rpm)

The release process produces `.deb` and `.rpm` for amd64/arm64 in the same release as the archives.
`v3.0.0` does not have these assets yet; they are available from v1.1.2:

```sh
# Debian / Ubuntu
sudo apt install ./option-berth_1.0.0_amd64.deb
sudo apt remove option-berth

# Fedora / RHEL
sudo dnf install ./option-berth-3.0.0-1.x86_64.rpm
sudo dnf remove option-berth
```

The package puts `oberth` in `/usr/bin` and the licenses and build manifest in
`/usr/share/doc/option-berth`. A package install registers no user-level unit; only `install.sh` does.
`apt remove` / `dnf remove` do not delete `~/.option-berth` runtime
state; once you are sure it is no longer needed, run `oberth runtime service stop` and delete that directory.
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

- Unregisters the user-level runtime service and deletes its unit file first, so the supervisor cannot
  start a binary that is about to be removed;
- Stops what is still running with `oberth runtime service stop`, so no old process keeps running;
- Removes `oberth`;
- Removes the licenses and build manifest the installer placed in `~/.local/share/option-berth`;
- Removes the exact line the installer wrote into a shell startup file (only that line; other PATH
  configuration is untouched).

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
oberth runtime service stop
rm -f ~/.local/bin/oberth
rm -rf ~/.local/share/option-berth
# Remove runtime state only when needed
rm -rf ~/.option-berth
```

Then delete the PATH line the installer added from your shell startup file.

## Upgrade

Rerun the install command for your platform to replace the binaries. Unless `--no-service` was passed,
the installer stops the previous runtime service and unloads the unit before replacing them, then writes
and starts the unit again, so the new build is what runs. On Windows, stop the running `oberth` first,
then reinstall. Pin a version with `--version` / `-Version`.

`oberth`'s first screen shows the local version; when an update exists, its footer gives the install
command for this platform and `oberth runtime service stop`. Every other terminal command that writes human
output ends with the same upgrade information as a two-line footer, once per release. You can also run
`oberth version --check` at any time. The automatic check reads release metadata only and downloads or
installs nothing: a success is cached for 5 minutes, a check that ran out of its own budget is retried
after 2 minutes, and an unreachable feed or an answer that is not a release is remembered for 30 minutes.
The first screen may wait up to 2 seconds for the answer, because that is where you asked for the
version; any other command waits at most 250 milliseconds for an answer that is already there, and
`oberth version --check` waits up to 6 seconds. `BERTH_NO_UPDATE_CHECK=1` disables the automatic notices.
When no release source answers, the first screen says the update check is unavailable and names
`oberth version --check`; no terminal path ever presents the local version as the latest.

## Channels not yet available

- **Scoop**: needs a separate bucket repository, which this repository does not maintain.
- **winget**: needs a submission to microsoft/winget-pkgs and review for every version update.
- **Chocolatey**: needs a community package repository and review, similar to winget.

The currently available paths are the scripts above, manual release downloads and the
Linux `.deb`/`.rpm`. A Windows MSI is not available yet.

## Platform boundaries

Full service scenarios are verified on **macOS / Linux**. Windows has an independent native build, smoke
tests and some fact/persistence tests, but is not full lifecycle support, and it has no user-level service
unit: there the runtime service is started with `oberth runtime service run --detach`. No platform provides
an auto-upgrade runtime service or a background updater.
