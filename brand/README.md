# Brand assets

This directory holds the brand files this repository itself uses.

| File | Used by |
| --- | --- |
| `option-berth-lockup-light.svg` | the header of the [English README](../README.md) |
| `option-berth-lockup-dark.svg` | the header in dark mode |
| `wordmark.svg` | the outline wordmark the lockups are built from |

The `oberth` first screen uses the same 24×24 geometry rasterised for a terminal; it lives in
`engine/internal/cmd/mark_gen.go` and is a static generated asset, not hand-drawn.

These are static files. No sibling checkout or export script is required; edit the SVG and the
terminal mark together if the brand changes. Third-party assets keep their own licences, listed in
[third-party attribution](../third_party/README.md).
