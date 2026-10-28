# Workplace record

Use this guide when the task is about what work has been done in a worktree, or
about which record new work belongs to. Reading it back is how you pick up work
someone else — or an earlier turn of your own — left behind.

A workplace is a named scope under one worktree. Its blocks are the work items:
the shell commands this engine ran and the agent turns it bridged. The record
is facts about what happened — it does not start, stop or resume anything.

## Read

```sh
oberth workplace list --json
oberth workplace show --json
oberth workplace show spike --limit 20 --json
```

`list` is ordered oldest first; `active` is the workplace new work lands in and
may be absent. `show` without a selector reads the active one. `truncated`
means the record holds more blocks than the response carries — read the rest
with a larger `--limit` rather than treating the page as the whole record.

A block's `status` is `running` until something observed an end. `running` is
not "probably finished": a block the engine never saw end stays running, and a
run stopped by `oberth` or the user is `interrupted` whatever exit code the
signal left. Do not report a `running` block as a success.

Engine blocks carry `run_id`, `pid` and `log_path`: the output lives in the
log, so read it with `oberth logs` or the path itself rather than expecting
text in the block. Only `source: reported` blocks carry `output` text, and that
text is the reporting front-end's account, not engine evidence.

## Write

Work this engine starts is recorded without being asked:

- `oberth start -- <command>` records the run, its run id, pid and log path,
  and its exit code when it ends.
- `oberth agent stream <provider>` records one block per turn: the prompt when
  the turn starts, the provider's outcome when it ends.

Write down what the engine cannot see — an interactive shell in someone's
terminal, a native agent, or an attempt of your own worth remembering ("I
changed the retry, the tests still fail") — with:

```sh
oberth workplace record --kind shell --id <id> --input 'npm test' --status running --json
oberth workplace record --kind shell --id <id> --status completed --exit-code 0 --json
oberth workplace record --kind agent --id <id> --input 'raised the pool size' \
  --status failed --output "$(tail -20 build.log)" --json
```

What you write is marked `source: reported`: your account, not the engine's
evidence. Reuse the same `--id` to report the same work again — it updates one
block rather than adding a second, so the start and the end can arrive in either
order, and a late `running` never overwrites an outcome already recorded. Keep
output text to the tail you want kept; the record refuses more than 64 KiB.

Recording never starts a daemon. With none listening, `oberth start` and
`oberth agent stream` say so once on stderr and record nothing — do not read an
empty workplace as "no work happened".

## Organise

```sh
oberth workplace new spike --json
oberth workplace use spike --json
oberth workplace delete spike --json
```

`new` and `use` change where new work lands; `delete` removes a workplace and
everything in it and is not undoable. Selectors match by exact name, then id,
then id prefix, then name prefix; `ambiguous` means more than one matched and
you must name it exactly.
