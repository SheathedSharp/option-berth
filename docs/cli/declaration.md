# Code and declaration

English | [中文](declaration.zh.md)

Read the code, generate this worktree's manifest, and the boundaries every command obeys when it reads a declaration. `git` only reads; `init` only produces a draft, and only `init adopt` writes `oberth.yaml`.

## oberth git [path]

Reads the code summary, file list or diff on demand outside status; `oberth git files --json` does not truncate, and `files_total` is the complete count. `oberth git graph [path] --json` returns the full commit topology, parent IDs, refs and worktrees. `--limit N` (N > 0) can still truncate, and sets `truncated` to true and `limit` to N in the JSON; `--limit 0` means the complete history just like omitting the parameter, in which case `limit` is 0 and `truncated` is false. Graph explicitly marks `observed_head` and stays read-only: no fetch, no index refresh. `oberth git diff [path] --commit <hex> [--file <repo-relative-path>] --json` reviews the patch of the given commit against its first parent; a root commit is against the empty tree, and merges do not use a combined diff. commit accepts only 7–64 hex-digit object IDs, and the response carries `commit` and `base` fields.

## oberth init

`init` does one thing only: generate a reviewable starter `oberth.yaml` from the current worktree's
facts. Listening ports and declarations found in project files appear only as comments, left for a
human to decide. `--dry-run` only previews, `--json` is for machine reading.

~~~text
oberth init [--dry-run] [--json]
~~~

A failed listener scan does not abort `init`: the project's own declarations and the shape of an entry
to copy are still there. Only one conclusion may not be written — "nothing was listening". The comment
says **listening unknown** instead, with the observation code and the next step, and stderr says so
once. This is the same rule `status` follows: cannot-look is not nothing-is-listening. A partially
readable scan still lists the ports it read and says the table is incomplete.

### oberth init draft [agent] [--dry-run] [--json] [--progress]

Has the local agent from the configuration draft a manifest. option-berth does not judge, does not go
online, and does not modify `oberth.yaml` directly: success only produces a draft (archived by default
under `~/.option-berth/drafts/`), which a human reviews and then adopts. When several agents are
available, the positional argument can name `claude` or `codex`; with no argument the configured
agent, or the only one available on PATH, is used. `--progress` outputs NDJSON progress events,
suitable for a client to show the current stage while the agent works; the last line is the full
result with `type: "done"`.

### oberth init adopt [draft|path] [--replace|--merge] [--dry-run] [--diff] [--json]

Writes the draft a human selected — or any manifest named by path — into the current worktree's
`oberth.yaml`. Without a path the project's latest draft is adopted; a path is how a new worktree
inherits a declaration from another checkout, and the two are independent afterwards. This is the
step that writes to disk. An existing manifest requires an explicit `--replace` or `--merge`.
`--dry-run` only previews the complete manifest that would be written; adding `--diff` outputs a
read-only unified diff of the current manifest against the adopted result (the JSON output puts it in
the `diff` field), writing no manifest and moving no draft.

## Manifest and state boundaries

- The manifest is written by humans; an agent may only draft, and only `oberth init adopt` adopts it.
- The manifest belongs to the worktree directory, not to the branch and not to the repository: the
  engine finds it by walking the filesystem and never asks git whether it is tracked. Whether a
  project commits it is the project's decision; the one behavior that follows from it: a tracked
  manifest under a declared `sources:` path is part of that fingerprint, an ignored one never is.
- A new checkout starts without one. `oberth init adopt <path>` copies a declaration from any
  existing checkout into this worktree; after that the two are independent.
- `oberth.yaml` is the current name. `.oberth.yaml` and `oberth.yml` are still read, nothing new is
  created under them, and `oberth doctor` reports a shadowed or legacy spelling.
- option-berth only reads git; it does not commit, does not stage, does not switch branches.
- machine: is a read-only reference to a project dependency; up / down do not start or stop it on its behalf.
- workspace: is an address reference to a service in another checkout; up / down / restart do not start or stop the other side on its behalf, and do not establish a
  depends_on relationship.
- The `worktrees` ledger records only "which paths an identity has been seen at" and the most recent group name; it does not record whether services are running,
  and does not automatically delete data after a checkout disappears.
- Listening ports, pids, exit codes and logs are a Run's runtime facts. The product does not provide a machine-wide port radar, a machine service registry, model
  judgment, an MCP service, or automatic skill installation.

### Manifest references

`prepare:`, `cmd:`, `env:` values and command-form `health:` can reference ports and checkout
identity; the runtime service expands them before starting services:

| Reference | Expands to |
|---|---|
| `${port}` / `${url}` | This service's own port / `http://localhost:<port>` |
| `${<name>.port}` / `${<name>.url}` | The port / URL of a service in this manifest, a `machine:` reference, or a `workspace:` reference |
| `${worktree}` | The current checkout's stable identity, of the form `wt_0123456789ab` |

`<name>` is looked up among this manifest's `services:` first; when there is no service of that name
it then checks `machine:` (fixed port) and `workspace:` (another checkout's address). Service, machine
and workspace names therefore may not duplicate one another. Command-form `health:` may reference
services and machines but not workspaces: the scanner does not go read another checkout's address on
every health check.

`${worktree}` is derived from the canonicalized root path of the checkout the manifest lives in: it is
the same on every start for the same worktree, and different for the main checkout and each linked
worktree. It is an identifier rather than a path, suitable for composing into database names
(`app_${worktree}`) or relative data directories (`.data/${worktree}`). Moving a checkout directory
yields a new identity, and old data is not moved along with it. Read the current checkout's value
directly from `oberth status --json`'s `worktree.identity`; for which identities this machine has seen
and which checkouts are gone, use `oberth worktrees`. Deleting data is still done by a human; the
engine only reports the correspondence between identities and paths.

Only the five shapes in the table above are references; other `${...}` (for example `${HOME}`,
`${FOO:-x}`) are kept as-is. `${worktree}` does not point at a service, so it neither needs nor
produces `depends_on`; undeclared services and port references to a service with no port are still
rejected by manifest validation. For a runnable sample see
[`examples/stack-workshop`](../../examples/stack-workshop/README.md): it puts `${worktree}` into both a
SQLite path and a per-worktree Compose project, and its README shows `port: auto` reaching Compose as
`PORT`, the `pg_isready` health check and `down` letting Compose stop its containers.

### Shared environment files

A service may declare `env_file:` pointing at a dotenv-style file; relative paths resolve against the
directory containing `oberth.yaml`, so `../shared/dev.env` can live outside the checkout and let
several worktrees share one configuration and set of secrets. The file is read only when that service
starts or runs its `prepare:`; option-berth does not create or modify it.

Blank lines and whole-line comments starting with `#` are ignored; the optional `export ` prefix,
single-quoted literal values, and double quotes (escaping `\n`, `\r`, `\t`, `\\`, `\"`) are supported.
Values are not interpolated, and `${...}` is kept as-is. The precedence when writing into the
environment, from lowest to highest, is: the runtime service's own environment, the caller's environment,
`env_file`, `PORT`, the service's own `env:`. A missing file, a non-regular file, or a malformed line
makes that service fail to start; the error names only the file and the line number and does not echo
the value. File contents do not enter `status`, logs or `spec_hash`; changing it does not require
restarting the runtime service, but a started service must be restarted to read it.

### Cross-worktree address references

`workspace:` lists services in another checkout, providing addresses only and taking no part in
start/stop:

```yaml
workspace:
  - name: backend
    worktree: ../backend
    service: api
```

`worktree` is the other checkout's root directory (relative to this manifest's directory, or an
absolute path), and may also point directly at that checkout's manifest file; `service` is a service
name in that manifest. Afterwards `prepare:`, `cmd:` and `env:` can use `${backend.port}` /
`${backend.url}`. Resolution reads the other manifest and the runtime service's runtime facts: if the other
service is running, its actual port is used (including `port: auto`); if it is not running but
declares a fixed port, the declared address is used; with `port: auto` and not running, the reference
cannot be resolved, and the service using it fails to start with an explanation. `up` / `down` /
`restart` do not start or stop the other side's services and do not establish `depends_on`
relationships. A `workspace:` name may not duplicate a service or `machine:` reference in this
manifest; health checks do not accept workspace references.

### Ambiguity rejection in manifest parsing

`oberth.yaml` accepts only one YAML document; a second document (including an empty one) is an error
rather than being ignored. The same service may not declare `port` twice, including mixing `auto` with
a number; the error carries the YAML line number. Single documents, comments, normal numeric ports and
`port: auto` remain compatible.

### Manifest schema and unknown-field strict mode

The manifest's JSON Schema is published at `engine/docs/schema/manifest.schema.json`, generated from
the declaration types in `internal/groups`; `go generate ./...` updates it together with the protocol
schema. The schema rejects unknown keys (except top-level `x-` extensions and the `$schema` key), and
ports accept only 1–65535 or `auto`. Files written by `oberth init` and `oberth init draft` carry a
`# yaml-language-server: $schema=...` modeline on the first line, so editors flag misspelled keys
before drafting and adopting; handwritten manifests can add the same line themselves.

Default parsing still ignores unknown keys for compatibility. With `BERTH_STRICT_MANIFEST=1` set (it
also accepts `true`, `yes`, `on`), unknown keys become load errors with line and column positions,
covering every manifest read path in the CLI, the runtime service and `oberth init adopt`; top-level `x-` and
`$schema` remain legal metadata.

`doctor --only project_config` reports ignored manifest keys with line and column positions as a warn
in default mode and does not echo field values; custom `env` keys and top-level `x-` extensions are
not falsely reported. It does not rewrite the manifest, does not run commands, and does not connect to
the runtime service to supplement report metadata.
