# Work records

English | [中文](workplace.zh.md)

The handoff layer's **work record**. One worktree can hold several workplaces, each keeping its work
entries (blocks) in order: commands the engine ran, and each round of work handed to an agent. It
answers "what happened here, and how did it turn out" — an agent can read it back to take over someone
else's work (or its own from a previous round), and can write in attempts the engine could not see. The
commands only read and write this record; they never judge, start or restore anything.

It hangs off the worktree, not the service chain: it takes no part in reducing service facts and
`status` never treats it as runtime truth. This page is the authority for its commands and fields; the
rest of the CLI contract is in [the CLI guide](cli.md).

## Commands

~~~text
oberth workplace list [--worktree PATH] [--json]
oberth workplace new [name] [--worktree PATH] [--json]
oberth workplace use <name|id> [--worktree PATH] [--json]
oberth workplace show [name|id] [--worktree PATH] [--limit N] [--json]
oberth workplace delete <name|id> [--worktree PATH] [--json]
oberth workplace record --kind shell|agent [flags] [--worktree PATH] [--json]
~~~

`--worktree` defaults to the current directory; it resolves to the checkout root and normalizes
symlinks, so different spellings of one checkout are the same record. When the path is neither a
checkout nor has a manifest, it returns not_found.

- `list` lists in creation order and marks the current workplace with `●`; having no workplace is not
  an error, it is an empty list.
- `new` creates one and makes it current; a duplicate name returns conflict.
- `use` switches the current workplace — afterwards `oberth start` and `oberth agent stream` record into it.
- `show` with no argument reads the current workplace; `--limit N` takes only the newest N entries, and
  `truncated` says the record has more.
- `delete` removes a workplace and all its blocks; deleting the current one lets the newest remaining
  take over, and when none remain there is no current workplace.
- Selectors match in order: exact name → full id → id prefix → name prefix. Several matches at one
  level return ambiguous rather than picking one for the caller.

## Block JSON fields

~~~json
{"id":"…","workplace":"…","seq":1,"kind":"shell","source":"engine",
 "input":"npm test","status":"completed","run_id":"…","pid":1234,
 "log_path":"…","exit_code":0,"started_at":"…","ended_at":"…"}
~~~

- `kind`: `shell` (a command the engine ran) or `agent` (one round of work handed to an agent).
- `source`: `engine` (a run the engine observed itself — run_id, pid, log_path and exit code all come
  from the engine) or `reported` (an entry the engine did not start, written by a reporter). Only a
  `reported` block carries `output` text; an engine block points at `log_path`, and the log remains the
  single copy of the output.
- `status`: `running` · `completed` · `failed` · `interrupted`. An entry with no observed end stays
  running and is never treated as success; a run stopped by `oberth` or by the user is interrupted,
  regardless of the signal exit code it left behind.
- `seq` is the entry's position in its workplace; each workplace keeps at most 500 entries and drops
  the oldest beyond that.

## How records appear

Records need no manual work:

- `oberth start` writes this run into the current workplace; a detached (`-d`) run is closed by the
  daemon when it records that run's exit.
- `oberth agent stream` writes one entry per round: `turn.started` opens it, and `turn.completed`
  closes it with the outcome the provider reported (completed / interrupted / failed).
- Work the engine cannot see is written in with `oberth workplace record`: an agent's own attempts
  ("I changed X and the test still fails"), the front-end's own interactive shell, a native agent.
  Reporting the same `--id` again updates that entry rather than adding one, so a piece of work's start
  and end may arrive in any order; a repeated `running` never overwrites an already recorded result.

## Boundaries

`show` reads back what happened; it does not call the processes of that time back. Read commands still
read the database directly when the daemon is absent; `new` / `use` / `delete` / `record` need a daemon
(which auto-starts under the usual rules), while records from `oberth start` and `oberth agent stream`
never start one for you — no daemon means no record, stated once on stderr. Service start and stop
(`up` / `down`) writes no work record: that is a declared lifecycle, not a temporary work entry.

Implementation lives in `internal/workplace` (rules, selectors and RPC),
`internal/store/workplaces.go` (rows and migration 009) and `cmd/workplace*` (CLI and record wiring).
There is one writer: the daemon is the ledger's only writer, while the CLI's read path opens the same
database read-only when the daemon is absent and write commands go through RPC.
