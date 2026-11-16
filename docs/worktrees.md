# Use multiple worktrees

English | [中文](worktrees.zh.md)

A Git worktree is a separate checkout of the same repository. Each directory has its own working files; services must also be attributable to that directory. This page assumes you already have a reviewed `oberth.yaml` committed to the repository.

## Start a second branch

From the original checkout, the following example creates a new branch and worktree. Choose an unused branch and directory; these Git commands change the repository.

```sh
git worktree add ../project-feature -b feature/demo
cd ../project-feature
oberth up --wait
oberth status --json
oberth down
cd -
git worktree remove ../project-feature
```

Run `status`, `up`, `logs` and `down` in the worktree you intend to operate on. `port: auto` gives each worktree its own reservation. A fixed port can still conflict: oberth does not silently change it or stop the unrelated listener.

## What isolation means

Isolation covers run ownership and lifecycle targeting. It is not an OS sandbox: both checkouts still share your user account, filesystem, Docker and anything your service commands access. Use per-worktree data paths or database names as well; `${worktree}` is a stable identifier derived from the checkout path, not the branch name.

A `machine:` dependency is observed only. A `workspace:` address can read another explicitly related worktree's published address, but does not add that service to the current project's start/stop targets. See [integration recipes](integrations.md).

## Stop before removing the directory

Prefer `down` while the checkout still exists, then remove the worktree with Git. If the directory is already gone, `oberth worktrees --gone --json` lists retained identities; select the exact identity and use `down --identity` as described in the [command reference](cli.md). No command here deletes application data.

Do not choose by group name when multiple worktrees have the same name: an ambiguous selection is an error, not permission to stop all matches. For agents, use the [skill](https://github.com/SheathedSharp/option-berth-skill) and explicit worktree scopes.
