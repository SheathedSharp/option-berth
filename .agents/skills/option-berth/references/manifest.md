# Manifest governance

Use this guide when a worktree has no `oberth.yaml`, or when a person asks for
help changing one.

Preview a starter without writing it:

```sh
oberth init --dry-run
```

If a person asks the local agent to draft a manifest, use `oberth init draft`
and show the result. Adoption is a separate human decision:

```sh
oberth init adopt
oberth init adopt --replace
oberth init adopt --merge
```

Use `--replace` or `--merge` only after the person chooses that write. Do not
edit `oberth.yaml`, invent a service command, install hooks/MCP, or adopt a
draft on the agent's own authority. A `prepare` command must rebuild the
current checkout before the long-lived `cmd`; never rely on a stale artifact.
