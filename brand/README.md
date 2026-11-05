# Brand assets

This directory holds only the brand files this repository itself uses.

| File | Used by |
| --- | --- |
| `option-berth-lockup-light.svg` | the header of the [English README](../README.md) |
| `option-berth-lockup-dark.svg` | the header in dark mode |
| `wordmark.svg` | the outline wordmark the lockups are built from |

The `oberth` first screen uses the same 24×24 geometry rasterised for a terminal; it is generated into
`engine/internal/cmd/mark_gen.go` and is not hand-drawn.

## Regenerating

The geometry and palette live in `Sources/BerthGeometry.swift` of the separate
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) checkout. Point
`BERTH_GEOMETRY` at that file, or keep the two clones side by side. Then run from the repository root:

```sh
python3 scripts/brand_export.py --check   # read-only; fails on drift
python3 scripts/brand_export.py           # rewrite the SVGs and the Go constant
```

Commit the regenerated files with the geometry change. The macOS app icons, empty states and small
sizes belong to the client and are built by its own native pipeline; they are not exported here.
Third-party assets keep their own licences, listed in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).
