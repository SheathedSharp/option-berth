# Service operations

Use this guide after the initial `oberth status --json` when the task concerns
service lifecycle or status reporting.

## Read and operate

```sh
oberth up --json
oberth up --wait --json
oberth up --only api,worker --json
oberth restart --only api --json
oberth down --json
oberth logs api --once --json
```

Use `up --wait` (or `--ready`) when readiness matters. It waits for listeners
and configured health checks; a portless worker is ready when its run is active.
Use `--force` only when a graceful stop failed and the person asked for it.
Use `logs --follow` for a live log stream. A port is accepted for scripts, but
an ambiguous listener must be narrowed with `--ip ADDRESS`.

Use `oberth kill` only for a named port or PID, or for the explicitly requested
all-listener exception. It is not a replacement for `down`.

After every control command, reread `oberth status --json`. Report the current
worktree path and branch, declared services and states, attributed runtime
facts, the last failure evidence, and the next bounded command.

Keep facts separate from guesses. A missing or ambiguous service/port result is
actionable output; it is not permission to scan the whole machine.
