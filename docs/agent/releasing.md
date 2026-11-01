# Release process

## This repository releases only the engine

option-berth is three independent products: this repository releases the `oberth` engine and CLI; the
macOS client and the coding-agent skill release from their own repositories. All three meet only at the
CLI and its `--json` contract; their versions and release cadences are independent.

This repository's release assets are the engine only:

- macOS, Linux and Windows archives for amd64 and arm64 (`option-berth-vX.Y.Z-<os>-<arch>.tar.gz|zip`);
- Linux `.deb` and `.rpm` (`option-berth_X.Y.Z_<deb-arch>.deb`,
  `option-berth-X.Y.Z-1.<rpm-arch>.rpm`);
- a `.sha256` per asset;
- an aggregate `SHA256SUMS`.

No macOS application, DMG, notarized bundle, client screenshot or skill installer is published here.

The engine module path is `github.com/sheathedsharp/option-berth/engine`;
`go install github.com/sheathedsharp/option-berth/engine/cmd/oberth@vX.Y.Z` builds straight from the
Git tag without going through release assets and without needing an extra module repository.

The install entry points are `install.sh` (macOS/Linux) and `install.ps1` (Windows) at the repository
root, plus the matching `uninstall.sh` / `uninstall.ps1`; they are not release assets and are served
from `main`:

```sh
curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh
```

The script resolves `releases/latest` (or pins a tag with `--version`), downloads the archive and its
same-named `.sha256` for the platform, verifies before extracting, installs `oberth` and
`jev-attention` into `~/.local/bin` (`%LOCALAPPDATA%\option-berth\bin` on Windows), leaves `LICENSE`,
`licenses/` and the build manifest in `~/.local/share/option-berth`
(`%LOCALAPPDATA%\option-berth` on Windows), configures PATH through the bundled `install-path.sh`, and
finally runs `oberth version` as a self-check. It starts no service, writes no Git state and needs no
administrator rights. The install target comes from a published tag, not from unreleased code on
`main`; when an older archive lacks `install-path.sh`, the same tag's `scripts/install-path.sh` is used.

When the official GitHub host is unreachable or too slow, the installer falls back to GitHub proxies in
a fixed order (defaults `https://ghfast.top`, `https://gh-proxy.com`; override or clear with
`BERTH_INSTALL_MIRRORS`). Checksums prefer the official source; when they come from a mirror, a second
source must report the same digest, and a failed verification writes nothing. A mirror changes only the
transport path, never the definition of a release asset.

The uninstaller tries `oberth daemon stop` first, then removes the binaries, licenses and the PATH line
the installer wrote; it keeps `~/.option-berth` runtime state by default, and only
`--purge-state` / `-PurgeState` deletes it. Full details in [install.md](../install.md).

### The version line after the split

The engine version restarts at **1.0.0**. That was not a protocol upgrade: rebasing `VERSION` changes
neither `ProtocolVersion` nor the regenerated protocol schema. Later versions still follow the step
table below.

Tags and releases `v0.2.0` through `v0.8.2` belonged to the combined product and included macOS
application assets. **They were removed from this repository's remote on 2026-10-29**; the commits
remain in Git history and the last pre-split commit is `74a51d1`. Client and skill versions are
maintained only in their own repositories.

## What a version means

The product version is **X1.X2.X3**, and the root `VERSION` file is the single source of the engine
version (no `v`). Git tags use `vX1.X2.X3`, must be annotated, and must point at a verified commit that
belongs to `main`'s history. Every release also creates an annotated `engine/vX1.X2.X3` tag on the same
commit; it is the Go version tag for the `engine/` submodule, and
`go install .../engine/cmd/oberth@vX1.X2.X3` depends on it.

| Change | Step | Example |
| --- | --- | --- |
| Protocol update | X1 + 1, clear X2/X3 | 1.2.3 → 2.0.0 |
| Feature update | X2 + 1, clear X3 | 1.2.3 → 1.3.0 |
| Defect fix | X3 + 1 | 1.2.3 → 1.2.4 |

This is this project's version convention; it does not equate "feature update" with protocol
compatibility. A change to the daemon wire contract counts as a protocol update. RPC has its own
historically independent counter: `ProtocolVersion` in `engine/internal/daemon/rpc/types.go`. A
protocol-level release raises that RPC major at the same time and regenerates the existing protocol
schema; feature and fix releases do not change the RPC version on their own. For example, product 0.1.0
with RPC 1.1.0 stepping the protocol yields product 1.0.0 and RPC 2.0.0 — the two need not match as
strings. Protocol-compatibility code and consumers must be finished and verified first; a script cannot
substitute for protocol design and compatibility review.

## Release cadence

Features merged into `main` ship as a weekly feature version (X2); several feature changes are not
piled into one. Within the support scope, a wrong stop, an attribution error, ledger corruption, or a
service that is listening but reported as not ready ships as a separate fix version (X3) without
waiting for the weekly window. Only an incompatible daemon protocol change raises X1.

## Release path

Start from a clean, reviewed `main` that matches the remote. You need Git, Python 3.11+, Go and the
tools local verification requires. The client and the skill take no part in this repository's release
verification.

There is exactly one path: **version PR → merge → push annotated tags → `Verified release` verifies and
publishes on the runner**. There is no local shortcut for creating a version commit, tagging or
pushing; `scripts/release.py` keeps only `--verify-only` and `--validate-tag` for CI.

1. Branch from `main`, change only `VERSION` (a protocol release also changes `ProtocolVersion` and
   regenerates the schema), commit `chore(release): vX.Y.Z`, and open a PR.
2. Once the PR's required checks pass and it is confirmed, merge with a merge commit.
3. Create the annotated tags `vX.Y.Z` and `engine/vX.Y.Z` on the merge commit and push them.
4. `Verified release` runs `policy` / `verify` / `windows` / `package` / `publish` against the tag,
   executing the same gates as `scripts/release.py --verify-only` on the runner, and only then builds
   assets and promotes the draft.

Local read-only checks:

```sh
python3 scripts/release.py --verify-only          # the same full gate set as the runner
python3 scripts/release.py --validate-tag vX.Y.Z  # verify an existing tag
```

Neither writes a version, creates a tag or pushes anything. A pushed tag never moves again; on failure,
fix and ship the next version.

## Mandatory pre-release verification

`Verified release` runs the build, vet, full unit tests, core race tests, real service scenarios, the
Mage/Python helper tests and the brand consistency check on the tagged commit; any failure prevents
building or publishing assets. There is no option to skip tests or force a publish.

### Agent scenario matrix before release

The fixed scenario library's entry point is `scripts/agent_eval.py`, contracted in
[agent-eval.md](agent-eval.md). A release candidate should run `--execute --repeat 2` on an ordinary
terminal that can observe listeners (or in CI with credentials) and attach `report.md` as release
evidence; `--require-comparable` confirms the same scenario really ran repeatedly from the same build.

Real agents depend on an external CLI, a login state and the network, so this step is **not** part of
`scripts/release.py`'s mandatory verification, and its failure does not block an engine release. Any
agent row that did not run must say "not run" in the evidence; reference scripts and fake-engine
results cannot substitute for a real agent.

The workflow verifies the version, protocol schema, annotated tag and `main` ancestry before continuing.
A genuine failure is never handled by deleting assertions, relaxing timeouts or adding unbounded retries.

## The GitHub Actions release chain

`.github/workflows/release.yml` runs these steps against the tag:

1. Verify the version, protocol schema, annotated tag and `main` ancestry, and run the release policy
   tests.
2. Run native release verification on macOS/Linux, reusing the Windows native build and
   fact/persistence checks.
3. Build six CLI archives for Darwin/Linux/Windows on amd64 and arm64, and generate amd64/arm64 `.deb`
   and `.rpm` packages on Linux targets.
4. Each CLI archive contains `oberth`, the optional `jev-attention`, `install-path.sh`, the build
   manifest, the license texts the target actually depends on, and `SHA256SUMS`; Linux packages extract
   binaries and licenses from that same archive rather than building separate content.
5. After verifying that all targets exist and hashing them, create a GitHub draft release, upload,
   download and verify, comparing every hash against the local pre-upload values, and only then promote
   it to a public release.

Windows arm64 and some CLI archives are cross-built; having a downloadable archive is not the same as
having verified every platform's lifecycle. The tag workflow checks out the triggering commit SHA
explicitly and fetches full history and tag objects, so that a fallback fetch of ref plus a stripped
commit cannot turn the runner's local annotated tag into a lightweight one. The annotated-tag, version
consistency and `main` ancestry checks are never relaxed; after a verification or publish failure the
remote tag is not moved and the fix ships as the next version.

## Failure and recovery

Failure before verification: fix the real problem and start over; no version or tag needs cleaning up.
Local commit/tag created but the atomic push failed: keep the result, check both sides' refs first, then
decide how to retry; never rewrite the tag.
Upload/verification failure: inspect leftover drafts and assets; never promote a failed draft to public.
A network interruption may leave the client unable to confirm the final state — read the remote to
establish it, and never infer "nothing happened" from a local error.
A published version never gets overwritten assets or a moved tag; fix and ship the next version by
category. An existing installation does not auto-upgrade or stop user services.

## Native Windows service regression

The Windows workflow runs `verify_service_lifecycle.py` after building the CLI, using a temporary
HOME/BERTH_HOME, an independent named pipe, its own generated API and a portless worker. It covers port
and run identity, repeated `up`, read-only status after the collector exits, collector recovery, scoped
project `down`, and unrelated listeners staying alive. On Windows it validates the fixture executable at
launch time and holds a kernel process handle, then confirms exit through the same handle instead of
re-selecting a stop target from an old PID. On failure it cleans up only its own temporary fixtures and
explicitly started daemons, and every fixture has its own bounded self-exit deadline.

This check does not cover Windows/Linux desktop endpoints, every terminal/permission/service-tree
scenario, or native execution on Windows arm64.

## Preflight before tagging

The automated release CI runs `Verified release` (verify, package, publish) only when a **`v*` annotated
tag** matching `VERSION` is pushed; the automatic gates on PRs and `main` pushes cover build, vet, unit
tests and helper-script tests, as described in [validation.md](validation.md). The daily
`Nightly full verification` runs the release-equivalent full gate set on the default branch; confirm it
passes before tagging so problems that only surface there are not left for after the release. To rerun
locally, use `python3 scripts/release.py --verify-only`; it reads only the currently checked-out commit.
On failure, never move or overwrite the tag; fix and ship the next version.
