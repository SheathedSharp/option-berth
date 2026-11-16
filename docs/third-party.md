# Third-party source and licenses

English | [中文](third-party.zh.md)

This repository does use third-party software: Go libraries, the website's copied GSAP file, and Playwright for site development. Their source and notices belong here; a custom source-management framework does not.

## What is stored here

| Material | Where | Purpose |
|---|---|---|
| Versions, official sources and local modifications | [Source directory](../third_party/README.md) | A human-maintained list, not an extra development gate |
| Complete source archives | [sources/](../third_party/sources/) | Fixed upstream source for inspection and development |
| Original legal text | [licenses/](../third_party/licenses/) | Convenient reading copies; all original files also remain in the archives |
| Distribution responsibilities | [Third-party notices](../third_party/README.md) | Attribution and the existing binary-release license boundary |

## Scope and completeness

The archives cover all 59 modules selected by the engine's Go module graph, including indirect and selected test/tool modules. They separately include original SQLite source, GSAP, Playwright, and the declared Go/build-tool sources. The directory records the exact version and official tag or commit for each component. All snapshots are unmodified locally.

A module ZIP is the complete published module, not a whole upstream repository. The 24 retained module ZIPs supplement the full upstream archives; the other selected modules also have full upstream source. The actual JSONSchema test-suite submodule is included separately at its fixed commit rather than moving main.

This is not a copy of an entire CI machine. Tool-source snapshots do not claim to include each tool's independent development ecosystem. OS SDKs, hosted Actions, browser binaries, external agents and model services remain environment prerequisites. Embedded upstream vendor/test assets and their own licenses stay in the original archives.

## Read or update source

Choose a version from the directory, inspect the archive, then extract it into a new directory outside this checkout with path/link safety filtering. The original nfpm test fixture `files/testdata/tree/symlinks/link1` points to `/etc/foo`; it is preserved as data, not extracted or executed. Do not run installation hooks or tests just to read source.

An intentional upgrade needs only the corresponding official source, its original LICENSE/NOTICE/headers, and an update to the directory's version/source/local-modification record. There is no custom downloader, checksum lock, manifest-consistency checker or automatic updater to maintain. Collection-time provenance checks were one-off work, not a new product-development dependency.

Archives are not vendored into builds. Go still resolves the unchanged `engine/go.mod` and `engine/go.sum`. Existing product tests, module verification and binary-release checks remain in place; removing the extra scripts does not remove those safeguards.

## License and provenance decisions

GSAP uses its own Standard “No Charge” License, not this project's MIT and not an assumed OSI license. The original file header, official license-page snapshot and readable legal text are retained. Previous source-history records remain in Git history; they are not reintroduced as current dependency archives.

The independent macOS client maintains its own fonts and terminal dependencies. They are not this repository's shipped client assets. Root project licenses do not replace upstream or embedded-subtree terms. Preserve applicable notices; mark uncertain provenance as uncertain rather than inventing a license.
