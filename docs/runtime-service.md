# The local background service

English | [中文](runtime-service.zh.md)

Runtime Service is oberth's local background process. It owns observation, the live run registry and persistent storage. It is different from the API or worker declared in your project's manifest. “Oberth Runtime” names the runtime domain, not another process you must install.

## Check or control it

These are separate operations, not a script to run blindly. `run --detach` starts the background process; `stop` stops that process, not the project's services. Use `down` to stop project services.

```sh
oberth runtime service status --json
oberth runtime service run --detach
oberth runtime service log
oberth runtime service stop
```

`status --json` reports a nonzero exit when no host is running. `doctor` diagnoses the installation and observation permissions; `doctor --fix` may change state and should only be used after reading the proposed repair. The [reference](cli/infrastructure.md) owns all flags and response fields.

## The user-level service

The [installer](install.md) registers the runtime service as a user-level unit: a launchd LaunchAgent
(`com.sheathedsharp.option-berth.runtime`) on macOS, a systemd user unit (`option-berth-runtime.service`)
on Linux. That unit runs `oberth runtime service run --persistent`, so one runtime service stays up
across logins and shells instead of being started by whichever command needed one. It needs no
administrator rights.

Where the unit is installed it owns the runtime service, and the CLI asks it instead of starting a
process of its own: `runtime service restart` restarts the unit, and a write command that finds nothing
running asks the unit to start. A supervisor that refuses is reported as it is; the CLI does not fall
back to a detached process, because two owners would compete for the same socket and the same lock.
While the unit file exists, `run --detach` refuses and points at `runtime service restart` for the
same reason.

`install.sh --no-service` (or `BERTH_INSTALL_NO_SERVICE=1`) skips the registration, and a machine without
the unit keeps the behaviour described below. Windows has no such unit: the runtime service there is
started with `run --detach` and follows the idle timeout.

## Why it can be absent

A runtime service started by hand follows the default idle timeout of 30 minutes: with no connected keepalive client, active run or recent activity it may exit normally, so a missing socket does not by itself prove a crash. One started by the user-level unit passes `--persistent` and never idles out; there a missing socket means the unit is not running. In both cases inspect the runtime service's own log and the supervisor's log, and distinguish “never started”, “stopped”, “idle exit” and “failed to start” from evidence.

A project operation may autostart the matching host only when its observation preflight permits it. A sandbox that cannot inspect listeners should not create a replacement blind host: run it explicitly from a normal terminal instead. Do not weaken the sandbox or use repeated starts as a repair.

## Storage and version switches

Only one host may own a database, even with different sockets or `BERTH_HOME` values pointing to the same file. Online history and doctor database reads go through that owner. `--no-runtime-service` does not secretly connect: an occupied database yields `runtime_service_busy`; offline reads remain read-only.

Protocol 3 deliberately removed the old host command and endpoint names. Before replacing an older installation, stop its host normally using the matching old CLI. Do not clear lock files, delete sockets, reset the database or kill an unknown process. The exact migration names and settings belong to the [switch contract](cli/infrastructure.md#runtime-service-protocol-3-switch).
