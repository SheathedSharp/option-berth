# Third-party source and licenses

English | [中文](third-party.zh.md)

This repository does include third-party software. Go modules are dependencies, GSAP is copied into the website, and Playwright is a site-development tool. Historical engine work also referenced Sonar. Removing their notices would not make the code independent of its sources.

## What is stored here

| Material | Where to look | Meaning |
|---|---|---|
| Pinned source inventory | [third_party/README.md](../third_party/README.md) | Component versions, scopes, source archives, hashes and update procedure |
| Machine-readable inventory | [sources.json](../third_party/sources.json) | Input-lock hashes, source provenance and archive integrity metadata |
| Original source archives | [sources/](../third_party/sources/) | Real upstream source, not a directory of license texts and links |
| Required notices | [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) | Project-wide attribution and distribution responsibilities |
| Existing release-license input | [go-runtime-licenses.txt](../third_party/go-runtime-licenses.txt) | License text used by the binary packaging process; narrower than the source inventory |

The archives are intentionally not extracted inside the repository. That keeps copied packages out of Go's package discovery and makes the original snapshots verifiable. They are source material for developers, not an alternative module proxy. Builds still resolve the unchanged `engine/go.mod` and `engine/go.sum`; there is no `vendor` mode or `replace` directive.

## Scope and completeness

For the engine, the inventory covers the selected Go module graph, including indirect, test and generation dependencies. It separately identifies the union of production imports for macOS, Linux and Windows on amd64 and arm64. A dependency selected by the module graph is not necessarily linked into every binary.

A Go module ZIP is the complete published module snapshot, not the whole upstream project. Where available, it is retained with its `go.sum` h1 checksum. Each selected module also has an official upstream source archive pinned by a tag or commit and an archive SHA-256. Some module ZIPs were unavailable in the local cache and the public Go proxy timed out; their upstream source is present, but ZIP-to-module checksum equivalence is not claimed.

SQLite needs an additional distinction: the engine uses modernc's generated Go implementation, not a system SQLite library. Both that module's source and the corresponding original SQLite source are included. GSAP's website file is compared with the same file in its pinned upstream archive. The Playwright source covers the three packages in the site lockfile; browser binaries are not bundled here.

This is not a copy of an entire CI machine. External operating-system libraries, browser downloads, hosted Actions, external coding agents and model services remain environment prerequisites. Toolchain and build-tool boundaries are listed in the inventory. Git history is not source content and is not included. Any unresolved submodule or Git LFS content must be reported rather than called complete.

## Read or develop from a snapshot

First verify the archives without running any downloaded code:

```sh
python3 scripts/check_third_party.py
```

Choose the component and exact version in the inventory, inspect its archive, then extract it into a new directory **outside this checkout** with an archive tool that filters unsafe paths and links. Preserve its original license, notices and file headers. Do not run upstream installation hooks, scripts or tests merely to inspect the source. Building or modifying an upstream project is a separate development task with its own prerequisites.

To refresh snapshots after an intentional dependency change:

```sh
python3 scripts/collect_third_party.py --collect
python3 scripts/check_third_party.py
```

Collection reads public official sources and the existing verified module cache; it does not install dependencies or alter module resolution. Review the version, source URL, commit or tag, hash and license changes before committing. A missing download is an explicit gap, never a successful empty placeholder. The inventory is generated; do not hand-edit it to conceal a failed check.

## License and provenance decisions

GSAP uses its own Standard “No Charge” License, not this project's MIT license and not an assumed OSI license. The unmodified file header and an official license-page snapshot are retained. The page snapshot is dated; it is not misrepresented as a historically versioned legal text.

Sonar is recorded as historical provenance, not as a current runtime dependency. Its original MIT copyright and complete license text are preserved conservatively. A code-similarity percentage is not a legal test for deleting attribution. This does not assert that every current engine file is derived from Sonar.

The independent macOS client owns its own fonts and terminal dependencies. They are not distributed by this repository and are not copied here. See the notices for the boundary. Unknown provenance must be marked as unknown, not assigned a convenient license.
