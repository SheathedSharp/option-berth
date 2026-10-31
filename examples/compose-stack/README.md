# Compose stack

Two PostgreSQL containers, one per worktree, with no shared port, project or
database. Run `oberth up --wait` in each checkout and `oberth status` shows the
assigned port and the `pg_isready` health verdict.

~~~sh
docker pull postgres:16-alpine   # first run only
oberth up --wait
oberth status
oberth down
~~~

The manifest uses four facts that matter together:

| Fact | Why |
|---|---|
| `cmd: docker compose ... up` | Foreground Compose gets SIGTERM from `oberth down` and stops its containers. |
| `-p app_${worktree}` | Containers, network and volumes are per checkout. |
| `port: auto` + `"${PORT}:5432"` | option-berth assigns the host port and passes it as `PORT`; Compose interpolates it. |
| `health: command:docker compose ... exec -T db pg_isready` | Health is checked inside the container, not by a host `pg_isready` that may not exist. |

`oberth down --force` sends SIGKILL. Compose never gets to stop the containers,
so they can survive the run. Use the normal `oberth down` for this stack.

A normal `down` waits 5 seconds after SIGTERM before escalating. Compose's own
default stop grace is 10 seconds, so a stack that needs longer than 5 seconds to
stop would be killed mid-shutdown and leave its port held. The example sets
`stop_grace_period: 2s`; a heavier stack should keep its stop time inside
oberth's window, or be stopped with `docker compose down` before `oberth down`.

## One shared stack instead

A single Compose project shared by every worktree is not a managed service in
each manifest: one worktree's `down` would stop the database another worktree is
using. Declare it as a read-only `machine:` dependency and start it outside
option-berth:

~~~yaml
machine:
  - name: db
    port: 5432
services:
  - name: api
    cmd: python3 -u api.py
    prepare: python3 prepare-db.py   # create this worktree's schema/database
    env:
      DB_NAME: app_${worktree}
~~~

The shared stack owns the port; each worktree owns only its database or schema.
`oberth up` and `oberth down` never start or stop a `machine:` entry.
