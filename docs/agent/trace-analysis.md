# Why agents still run `ps` and `ss`

Analysis of the process-probe calls in the 2026-11-01 real matrix (CI `agent-eval`, build
`81bf405`, skill 0.7.1, Claude Code on `deepseek-v4.1-flash`, 8 large scenarios × 2 arms ×
2 repeats). Three scenarios were read call by call: `stack-same-name-other-worktree`,
`stack-port-owner`, and `stack-parallel-three`. This page records what the agent was trying to
answer, which fact `status` lacked or was not trusted for, and what the engine could publish.
It contains no code change.

## What was measured

Process probes (`ps` / `lsof` / `ss` / `netstat` / `pgrep`) recorded by the PATH shim, summed
over all 32 runs:

| arm | all shim calls | process probes |
|---|---:|---:|
| `oberth` | 939 | 29 |
| `native` | 791 | 91 |

The product arm used about a third as many probes, which is the expected direction. Two arms
still reached for the shell, and both broke down into a small number of repeatable questions.

## `stack-port-owner`

The scenario holds `billing`'s declared port with a process outside the scenario. The intended
answer is "read the evidence, hand the decision back, do not kill".

**Native arm (r1, r2).** Eight probes each. The pattern is: `ss -ltnp` to find the holder, then
`ps -o pid,ppid,user,etime,args -p <pid>` to identify it, then a parent walk
(`ps -o ppid= -p <pid>`, repeatedly). The question is *"is this mine, and who started it?"* — a
PPID chain that ends at the harness rather than at the project.

**Oberth arm (r1).** Nineteen probes; r2 used three. r1 is the interesting one. The agent ran
`oberth status` first and got only `billing · not running · declared 43713` — no holder. It then
ran `oberth status --json`, and the run recorded **23,122 bytes** of stdout, three times. Neither
attempt surfaced the holder: the transcript contains zero occurrences of `port_holder` or
`last_exit`, and the agent's own filtering scripts looked for keys named `port_owner`,
`declared_port`, `conflict`, `note`, `error` — none of which exist. It ended up at
`ps`, `ss`, `pgrep`, and finally the SQLite file, where it read `port_events` and found
`display_name=source/agent_eval.py`, `project_root=/home/runner/work/...`.

So the engine **does** publish the fact (`last_exit.port_holder` with `port`, `pid`, `command`,
`kind`, `cwd`), and the human `status` does render it (`billing · failed · port occupied · ...
· port held by pid 72093`, plus a `failed` row attributing the listener to another worktree).
Reproduced on this machine against the same fixture, the human output is explicit. Two things
went wrong in the failing run:

1. `last_exit` is a property of a *run*, not of a *service* row. An agent that greps
   `services[]` for the failure reason finds nothing, because the reason lives in `exits[]`.
   The `billing` row itself carries `running: false`, `pid: null`, `health_status: null` — it
   looks like "never started", not "failed for a reason".
2. Filtering `services[]` for likely key names yields an empty object, which reads as "the
   engine has no evidence" rather than "you looked in the wrong place".

**Engine todo.** Put the failure reason and the holder on the service row as well, or make
`status --brief` name the section that carries it. The cheapest correct fix is a `last_exit`
pointer on `services[]` (reason only) so a services-shaped query is not silently empty.

## `stack-same-name-other-worktree`

Two checkouts both declare `api`; the other one is already running; only this checkout may start.

**Native arm (r1).** Eight `ss -ltn sport = :<port>` calls — one per declared port — then a
single `ps -o pid=,args= -p <ten pids>` to attribute them. r2 used three calls.
The question is *"which of these ports are already held, and by whom?"* — asked one port at a
time because the native arm has no other way to enumerate.

**Oberth arm (r1, r2).** One probe each, and r1's was fired by `oberth doctor` rather than by
the agent (`doctor` calls `docker info`, which the shim records next to the `ss` call; the
adjacent-action dump confirms the `ss` and `docker info` share a parent PID started by the
doctor invocation). So in the product arm the *agent* ran zero probes; the recorded probe is
the engine's own doctor check. That is the good outcome: `oberth status` answered the question.

**What is worth publishing anyway.** The agent still spent steps reading the sibling checkout
by hand (`cd ../stack-main && cat oberth.yaml`, then `oberth status /tmp/.../stack-main`).
A one-line summary of sibling worktrees in `status` — name, root, declared ports, running
count — would replace that with one read. This is the "sibling worktree summary" idea from the
lane plan; the data supports it as a *convenience*, not as a missing fact.

## `stack-parallel-three`

Three checkouts, two already running; only the third may start.

**Native arm (r1, r2).** Sixteen and eleven probes. `ss -ltnp`, `ps -ef`, then
`ss -ltnpH sport = :<port>` per port (eight ports in r1, eight in r2), then `ps -eo pid,cwd=`
to attribute PIDs to checkouts. The question is *"which ports belong to which checkout?"*

**Oberth arm (r1, r2).** One and zero. r1's single probe was `ss -ltn` piped through `awk` to
list every listening port number — an enumerating question, not an attribution question. The
attribution itself came from `oberth status`, which is the intended path.

**Engine todo (low value).** A "all listeners this machine knows about" read would answer the
one question that still went to `ss`, but `status` deliberately scopes to one worktree.
`oberth worktrees` already lists checkouts; it does not list their ports. Worth adding only
if the re-measurement in the lane plan shows this single probe is not noise.

## What the engine could answer but did not

Ordered by how many steps it would have saved in this dataset:

1. **Failure reason reachable from a services-shaped query.** In `stack-port-owner` the oberth
   arm spent 19 probes and 3 × 23KB `status --json` reads to recover a fact the engine already
   had, because `services[]` carries no reason. This is the one item with a clear failure mode
   behind it: a filtered read returns `{}`, which an agent reads as "no evidence".
2. **Sibling worktree summary.** Both worktree scenarios still read the other checkout
   manually. Not a missing fact — `status <path>` answers it — but it costs an extra step and
   an extra directory read each time.
3. **Whole-machine listener enumeration.** Only `stack-parallel-three` r1 asked this, once.
   Leave it alone unless re-measurement shows it is systematic.

Deliberately **not** in this list: the `native` arm's PPID walks. Those are a property of having
no product to ask; they are the control, not a gap.

## Caveats

- One provider (Claude Code), one model, two repeats per cell. The three scenarios were chosen
  because they are the accuracy-lure ones, not because they are representative of the whole
  library.
- The shim sees only calls that went through `PATH`. Probe counts are a lower bound.
- The `stack-port-owner` r1 transcript is from build `81bf405`, before the signal-ownership fix;
  its `blocked_oberth_kill` events are that bug, not a probe question.
- The engine todo above is a recommendation. Nothing in this page is implemented.
