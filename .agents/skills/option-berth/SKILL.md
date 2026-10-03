---
name: option-berth
description: Use oberth to inspect and operate services for the current worktree, observe explicitly related local worktrees during parallel development, and arm task-scoped attention for long-running service work.
metadata:
  version: "0.3.1"
  source: "github.com/SheathedSharp/option-berth"
---

# option-berth

`oberth` is the source of truth for declared services and runtime facts. Run it
from the worktree that the task concerns. Use `--json` for a single document or
NDJSON for a stream; errors belong on stderr.

## Always start here

Read the current worktree before reasoning about services:

~~~sh
oberth status --json
~~~

This gives the declaration, service runs, listeners attributed to the worktree,
branch and dirty counts, recent exits, any pending manifest draft, and
`attention_path` when a deterministic anomaly is current. Do not turn it into a
question about every port on the machine.

When `attention_path` appears, read
[references/attention.md](references/attention.md) and follow that handoff
before acting on the anomaly.

When the task keeps declared services running or spans concurrent worktrees,
arm the task-scoped attention session after the initial status read. Use the
documented fixed option IDs in the task context, keep the session alive for the
task, and stop it when the task ends. A quiet session does not call Jev; it
records that the task was being watched so a zero-call result is explainable.

For code changes, read the worktree-scoped Git summary as well:

~~~sh
oberth git files --json
oberth git diff --json --file path/to/file
~~~

Read the detailed guide only for the mode the task needs:

- Service start, stop, restart, logs, or status reporting: read
  [references/operations.md](references/operations.md).
- Explicitly related worktrees or repositories: read
  [references/cross-worktree.md](references/cross-worktree.md).
- Manifest drafting or adoption: read
  [references/manifest.md](references/manifest.md).
- `attention_path`, Jev, human-in-the-loop, or quality feedback: read
  [references/attention.md](references/attention.md).

## Boundaries

- Facts come from the CLI and daemon. Do not infer runtime facts from model
  output, ports, process names, branches, or logs that were not published by
  `oberth`.
- `machine:` entries are read-only dependencies. `up` and `down` do not operate
  them; `kill` requires a specifically named target and the existing approval
  boundary.
- A person owns `oberth.yaml`. The agent may draft and explain a manifest, but
  adoption is an explicit human choice.
- Do not write hooks, MCP registrations, agent configuration, or a second
  product model. Do not answer the whole-machine port-radar question.
- Refresh `oberth status --json` after a control command and before acting on a
  derived attention request.

The detailed guides are part of this skill's distribution. Read only the guide
needed for the current task; do not load all references by default.
