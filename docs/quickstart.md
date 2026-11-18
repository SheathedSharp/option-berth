# Run your first service

English | [中文](quickstart.zh.md)

## Before you start

Install [oberth](install.md) and Python 3. This example uses only Python's standard library on macOS/Linux: no model account, container or package installation. Use a normal terminal that can inspect listeners. The commands below start a local HTTP server; they are instructions to run intentionally, not actions performed by this document.

## Create a small project

Make an empty directory and save the following as `oberth.yaml`. This is an example, not a proposed configuration for an existing project.

```yaml
name: oberth-demo
services:
  - name: api
    cmd: python3 -u -m http.server ${port} --bind 127.0.0.1
    port: auto
```

`services` lists what this directory should run. `cmd` is the command; `port: auto` reserves a port for this worktree, and `${port}` passes that number to Python. Do not replace it with a shared hard-coded port when making a second worktree.

## Start, inspect and stop

Run these commands in that directory:

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

`up --wait` returns successfully only after the configured readiness conditions hold. With this manifest, readiness means the HTTP process is listening; it is not an application-level health guarantee. Read the assigned port from `status --json` before opening it locally. `logs` reads the captured output; `down` stops the declared service and returns its automatic port reservation.

If startup fails, keep the error and read [troubleshooting](troubleshooting.md). Do not use a guessed PID or a broad process-kill command.

## Use your own project

Run `oberth init` for a starter configuration, then review `oberth.yaml`: working directories, commands, dependencies, ports and health checks must match the project. A model can draft a manifest; it cannot approve its adoption. See [manifest reference](cli/declaration.md#manifest-and-state-boundaries) and [integrations](integrations.md).

The repository's [Hello Worktree](../examples/hello-worktree/README.md) adds an API health check and a worker without a port. For two branches, continue with [multiple worktrees](worktrees.md).
