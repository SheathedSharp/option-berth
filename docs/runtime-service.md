# The local background service

English | [中文](runtime-service.zh.md)

Runtime Service is oberth's local background host. It owns observation, the live run registry and persistent storage for this machine. It is not the API or worker your manifest declares, and “Oberth Runtime” names the runtime domain rather than another process you install.

## Check or control it

`run --detach` starts the host; `stop` stops the host, not your project's services. To stop the services of a worktree, use `down`.

```sh
oberth runtime service status --json
oberth runtime service run --detach
oberth runtime service log
oberth runtime service stop
```

`status --json` exits nonzero when nothing is running. `doctor` diagnoses the installation and observation permissions; `doctor --fix` can change state, so read the proposed repair first. The [reference](cli/infrastructure.md) owns every flag and response field.

## The user-level service

The [installer](install.md) registers the host as a user-level unit: a launchd LaunchAgent
(`com.sheathedsharp.option-berth.runtime`) on macOS, a systemd user unit (`option-berth-runtime.service`)
on Linux. That unit runs `oberth runtime service run --persistent`, so one host stays up across logins and shells instead of being started by whichever command needed one. It needs no administrator rights.

Where the unit is installed it owns the host, and the CLI asks it instead of starting a process of its own: `runtime service restart` restarts the unit, and a write command that finds nothing running asks the unit to start. A supervisor that refuses is reported as it is; the CLI does not fall back to a detached process, because two owners would compete for the same socket and the same lock. While the unit file exists, `run --detach` refuses and points at `runtime service restart` for that same reason.

`install.sh --no-service` (or `BERTH_INSTALL_NO_SERVICE=1`) skips the registration, and a machine without the unit keeps the behaviour described below. Windows has no such unit: the host there is started with `run --detach` and follows the idle timeout.

## Why it can be absent

A host started by hand follows the default 30-minute idle timeout: with no connected keepalive client, active run or recent activity it may exit normally, so a missing socket is not by itself a crash. One started by the user-level unit passes `--persistent` and never idles out; there a missing socket means the unit is not running. In both cases read the host's own log and the supervisor's log, and distinguish “never started”, “stopped”, “idle exit” and “failed to start” from the evidence you find.

A project operation may autostart the matching host only when its observation preflight permits it. A sandbox that cannot inspect listeners should not create a replacement blind host: start one explicitly from a normal terminal. Do not weaken the sandbox or use repeated starts as a repair.

## The Runtime World projection

The host publishes the same reduced document you get from `oberth runtime inspect`: the worktree, its services, the machine dependencies, the coverage of this observation and the causal diagnosis, in one revision-stamped shape (`oberth.runtime.world/v1`). It answers the `runtime.world.read` method under the `runtime.world` capability, which is an additive protocol version `3.1.0`; a client that does not ask for it sees the older surface unchanged.

In-process components attach through one seam, `Server.RegisterWorldObserver(func(runtimecore.World))`, which receives each world the host reduces. The deterministic notes engine is such a plugin, wired in `cmd/runtime_service_run.go`; the optional model client is not, and a test asserts that the host, the scanner, the reducers and the control-path commands never depend on it. An observer therefore extends what the host already knows without becoming a second source of runtime truth.

## Storage and version switches

Only one host may own a database, even with different sockets or `BERTH_HOME` values pointing at the same file. Online history and doctor database reads go through that owner. `--no-runtime-service` does not secretly connect: an occupied database yields `runtime_service_busy`, and offline reads stay read-only.

Protocol 3 deliberately removed the old host command and endpoint names. Before replacing an older installation, stop its host normally with the matching old CLI. Do not clear lock files, delete sockets, reset the database or kill an unknown process. The exact migration names and settings belong to the [switch contract](cli/infrastructure.md#runtime-service-protocol-3-switch).
