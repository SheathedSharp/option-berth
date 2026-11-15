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

`status --json` reports a nonzero exit when no host is running. `doctor` diagnoses the installation and observation permissions; `doctor --fix` may change state and should only be used after reading the proposed repair. The [reference](cli.md) owns all flags and response fields.

## Why it can be absent

The default idle timeout is 30 minutes. With no connected keepalive client, active run or recent activity, the host may exit normally. A missing socket therefore does not by itself prove a crash. Inspect its own log and distinguish “never started”, “stopped”, “idle exit” and “failed to start” from evidence.

A project operation may autostart the matching host only when its observation preflight permits it. A sandbox that cannot inspect listeners should not create a replacement blind host: run it explicitly from a normal terminal instead. Do not weaken the sandbox or use repeated starts as a repair.

## Storage and version switches

Only one host may own a database, even with different sockets or `BERTH_HOME` values pointing to the same file. Online history and doctor database reads go through that owner. `--no-runtime-service` does not secretly connect: an occupied database yields `runtime_service_busy`; offline reads remain read-only.

Protocol 3 deliberately removed the old host command and endpoint names. Before replacing an older installation, stop its host normally using the matching old CLI. Do not clear lock files, delete sockets, reset the database or kill an unknown process. The exact migration names and settings belong to the [switch contract](cli.md#runtime-service-protocol-3-switch).
