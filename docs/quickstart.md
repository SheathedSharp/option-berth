# Run your first service

English | [中文](quickstart.zh.md)

One file, one HTTP server: start it, read what oberth observed, stop it again.

## Before you start

Install [oberth](install.md) and Python 3. The example uses only Python's standard library on macOS/Linux: no model account, no container, no package install. Run the commands in a normal terminal that can inspect local listeners.

## Create a small project

Make an empty directory and save this as `oberth.yaml`:

```yaml
name: oberth-demo
services:
  - name: api
    cmd: python3 -u -m http.server ${port} --bind 127.0.0.1
    port: auto
```

`services` lists what this directory should run. `cmd` is the command it runs; `port: auto` reserves a port for this worktree and `${port}` passes the assigned number to Python. Keep the reservation when you create a second worktree instead of replacing it with a shared hard-coded port.

## Start it, then read what oberth saw

Run these in that directory:

```sh
oberth up --wait
oberth runtime inspect --json
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` returns only after the configured readiness conditions hold. Here readiness means the HTTP process is listening; it is not an application health guarantee. `runtime inspect` prints the whole worktree as one document: `coverage` says how much this round could observe, `health` summarizes the worktree, and `diagnosis.next_actions` lists what could be done next with a `safety` class. `read_only` actions are safe to run, `local_restart` restarts a local service, and `human_decision` is yours to make. `status` gives the same facts per service, and its `diagnosis` field carries the same answer.

Read the assigned port from `status --json` before opening the service locally. `logs api --once` prints one pass of the captured output. `down` stops the declared service and hands the automatic port reservation back.

## When something goes wrong

Keep the error text and work through [troubleshooting](troubleshooting.md). Do not guess a PID and do not reach for a broad process-kill command; a port that belongs to someone else is a fact to read, not a process to remove.

## Use your own project

`oberth init` writes a starter configuration. Then review `oberth.yaml` against the project: working directories, commands, dependencies, ports and health checks. A model can draft a manifest; only a person adopts it. Field by field references are in the [manifest reference](cli/declaration.md#manifest-and-state-boundaries), and orchestrator setups are in [integrations](integrations.md).

The repository's [Hello Worktree](../examples/hello-worktree/README.md) adds an API health check and a worker without a port. To run two branches side by side, continue with [multiple worktrees](worktrees.md).
