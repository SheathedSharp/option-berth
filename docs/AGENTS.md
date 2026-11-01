# Documentation standard

Rules for every document in this repository. They exist so a fact has one home and a reader can tell,
from the path alone, whether a page is written for a person or for a coding agent.

## Two readers, two sets of pages

| Reader | Path | Language | Carries |
|---|---|---|---|
| A person using or integrating `oberth` | `docs/*.md` | English and Simplified Chinese, paired | The product's external contracts: CLI, manifest, installation, integration recipes, work records |
| A coding agent working on this repository | `docs/agent/*.md` | English only | Engineering process: architecture boundaries, build, validation, release, the scenario library |

The split is by reader, not by subject. `docs/agent/` is not a mirror of `docs/` and never becomes one.

## One home per fact

A contract lives in exactly one place. Everything else links to it.

| Fact | Home | Never in |
|---|---|---|
| Product definition, what the product refuses to do | `docs/product.md` | `docs/agent/` |
| CLI surface, error codes, JSON fields, manifest references | `docs/cli.md` | `docs/agent/`, the skill repository |
| Work records and handoff | `docs/workplace.md` | `docs/cli.md` beyond a pointer |
| Installation, upgrade, uninstall | `docs/install.md` | `README.md` beyond a summary |
| Orchestrator and dev-server recipes | `docs/integrations.md` | `docs/agent/` |
| Implementation boundaries and the chain of facts | `docs/agent/architecture.md` | `docs/product.md` |
| Build commands and what to run after a change | `docs/agent/develop.md` | `README.md` |
| What each check proves and its platform scope | `docs/agent/validation.md` | `docs/agent/develop.md` |
| Scenario library, arms, metrics and the comparison report | `docs/agent/agent-eval.md` | `docs/cli.md` |
| Version, tag and release rules | `docs/agent/releasing.md` | `AGENTS.md` |
| Standing orders an agent needs in every session | root `AGENTS.md` | any document below it |

When you find the same rule in two places, delete one and link to the other. A duplicated rule is stale
in one of its homes and nobody can tell which.

## Bilingual pairs under `docs/`

Each `docs/foo.md` has a `docs/foo.zh.md`. Both carry equal authority: either may be authored first and
the counterpart is translated from it, but they must say the same thing. What binds them is meaning, not
form.

- **A pair merges whole.** A PR never lands one language without the other.
- **Structure mirrors.** Heading depth and order, list kinds and item counts, table row and column
  counts, and code blocks match one to one. Code blocks are byte-identical on both sides.
- **Language switcher.** The English file opens with `English | [中文](foo.zh.md)` immediately after
  its H1; the Chinese file carries `[English](foo.md) | 中文` in the same place.
- **Links follow the locale.** A relative link to another paired document uses `.md` on the English
  side and `.zh.md` on the Chinese side. A link to `docs/agent/` or to a file outside `docs/` is the
  same on both sides.
- **Translations are faithful.** Preserve facts, numbers, dates, caveats and negative statements. Never
  soften "not run" into "pending" or drop a known gap to make the Chinese read better.

`docs/AGENTS.md` itself is exempt: it is an agent instruction and stays English only.

## Tier rules

`docs/agent/` pages are read by agents, so they state current mechanics and boundaries — not rationale
transcripts, not marketing, not a teaching sequence. A reader arrives looking up one fact.

- State the current state. History belongs in commits and pull requests.
- One physical line per paragraph; code blocks, tables and list structure keep their own formatting.
- Name the exact type, function, check or command instead of a metaphor.
- Describe a boundary by what it prevents, not by how the code reaches it.
- Link the owning document instead of restating it.

## What not to write

- Implementation-status annotations ("implemented", "future: …"). Status rots; the code and the
  manifests carry it.
- Hand-copied catalogs of commands, fields or tests when a generator or the source is authoritative.
- Step-by-step narration of an implementation, or proof of an obvious branch. Keep the resulting
  contract or the durable rationale.
- Paragraph walls that carry several rules and parenthetical asides. Split them, or move the detail to
  its home.
- Emphasis inflation. Bold everywhere means nothing stands out; reserve it for the clause that changes
  behavior.

## Checks

`python3 scripts/check_docs.py` verifies pair completeness, matching structure, byte-identical code
blocks, the language switcher and that every relative link target exists. It runs as part of
`python3 -m unittest discover -s scripts -p '*_test.py'`.

The check proves the pair was consistent at these exact contents; it cannot judge whether the two sides
say the same thing, or whether the wording is accurate. That is the reviewer's half of the contract.
