#!/bin/bash
# Real-link end-to-end measurement for option-berth.
#
#   scripts/measure-e2e.sh <label> <oberth-binary>
#
# Runs one isolated scenario against one binary and prints one line per
# measurement. It is the harness for the backlog's P4 acceptance: real daemon,
# real target worktree, isolated BERTH_HOME, and a PATH shim that counts every
# external command the daemon runs (lsof/ps/git/docker) by parent pid.
#
# Set BERTH_DEMO to an explicitly chosen, disposable project directory.
# There is no default pointing into a maintainer's personal workspace.
# The daemon is stopped at exit. The private temporary result directory is
# retained for inspection; it may contain local paths and command arguments.
#
# What it measures:
#   idle      — scans, daemon external calls by command, RSS, over a 12 s window
#               with one `events` subscriber connected
#   status_ms — p50/p95/max wall time of `status --json` over 30 calls
#   cycles    — average `up` and `down` wall time over five start/stop cycles
#               and the daemon's RSS afterwards
#
# Known gaps (recorded in docs/backlog.md): no CPU sampling, no multi-worktree
# or reconnect scenario yet, and 30 calls is too few for a stable p95.
set -u
LABEL="${1:?usage: measure-e2e.sh <label> <binary>}"
BIN="${2:?usage: measure-e2e.sh <label> <binary>}"
DEMO="${BERTH_DEMO:?set BERTH_DEMO to an explicit disposable project directory}"
if [ ! -d "$DEMO" ]; then
  echo "BERTH_DEMO must name an existing disposable project directory" >&2
  exit 1
fi
DEMO=$(cd "$DEMO" && pwd -P) || exit 1
ROOT=$(mktemp -d "${TMPDIR:-/tmp}/berth-e2e.XXXXXXXX") || exit 1
SHIM="$ROOT/shim"
HOME_DIR="$ROOT/home"

mkdir -p "$SHIM" "$HOME_DIR" || exit 1
for pair in "lsof:/usr/sbin/lsof" "ps:/bin/ps" "git:/usr/bin/git" "docker:/opt/homebrew/bin/docker"; do
  name="${pair%%:*}"; real="${pair#*:}"
  cat > "$SHIM/$name" <<EOF
#!/bin/sh
printf '%s %s\n' "\$\$" "\$PPID $name \$*" >> "$ROOT/calls.log"
exec "$real" "\$@"
EOF
  chmod +x "$SHIM/$name"
done

# The daemon takes PATH from the login shell unless that fails; a nonexistent
# SHELL keeps the shim in front so the counts are real.
export BERTH_HOME="$HOME_DIR" SHELL=/nonexistent PATH="$SHIM:$PATH"

DAEMON=""
cleanup() {
  "$BIN" daemon stop >/dev/null 2>&1
  [ -n "$DAEMON" ] && kill "$DAEMON" 2>/dev/null
  pkill -f "$ROOT" >/dev/null 2>&1
}
trap cleanup EXIT

"$BIN" serve -d >"$ROOT/serve.log" 2>&1
sleep 1.5
DAEMON=$(sed -n 's/.*daemon started (pid \([0-9]*\)).*/\1/p' "$ROOT/serve.log" | head -1)
if [ -z "$DAEMON" ]; then echo "$LABEL ERROR: daemon did not start"; cat "$ROOT/serve.log"; exit 1; fi

: > "$ROOT/calls.log"
( cd "$DEMO" && "$BIN" events --worktree "$DEMO" >/dev/null 2>&1 & echo $! > "$ROOT/sub.pid" )
sleep 12
kill "$(cat "$ROOT/sub.pid")" 2>/dev/null
CALLS=$(awk -v d="$DAEMON" '$2==d' "$ROOT/calls.log" | wc -l | tr -d ' ')
SCANS=$(grep -c -- "-iTCP -sTCP:LISTEN" "$ROOT/calls.log")
PSA=$(awk -v d="$DAEMON" '$2==d && $3=="ps"' "$ROOT/calls.log" | wc -l | tr -d ' ')
DOCKER=$(awk -v d="$DAEMON" '$2==d && $3=="docker"' "$ROOT/calls.log" | wc -l | tr -d ' ')
RSS=$(ps -o rss= -p "$DAEMON" | tr -d ' ')
echo "$LABEL idle: scans=$SCANS daemon_calls=$CALLS ps=$PSA docker=$DOCKER rss_kb=$RSS"

LAT=""
for _ in $(seq 1 30); do
  START=$(python3 -c 'import time;print(time.time())')
  ( cd "$DEMO" && "$BIN" status --json >/dev/null 2>&1 )
  LAT="$LAT $(python3 -c "import time;print((time.time()-$START)*1000)")"
done
echo "$LABEL status_ms: $(python3 -c "
v = sorted(float(x) for x in '$LAT'.split())
n = len(v)
print('p50=%.1f p95=%.1f max=%.1f' % (v[n//2], v[min(n-1, int(n*0.95))], v[-1]))
")"

UP_TOTAL=0; DOWN_TOTAL=0
for _ in 1 2 3 4 5; do
  START=$(python3 -c 'import time;print(time.time())')
  ( cd "$DEMO" && "$BIN" up --json >/dev/null 2>&1 )
  UP_TOTAL=$(python3 -c "print($UP_TOTAL + $(python3 -c 'import time;print(time.time())') - $START)")
  START=$(python3 -c 'import time;print(time.time())')
  ( cd "$DEMO" && "$BIN" down --json >/dev/null 2>&1 )
  DOWN_TOTAL=$(python3 -c "print($DOWN_TOTAL + $(python3 -c 'import time;print(time.time())') - $START)")
done
RSS2=$(ps -o rss= -p "$DAEMON" | tr -d ' ')
echo "$LABEL cycles: up_avg=$(python3 -c "print(round($UP_TOTAL/5,3))")s down_avg=$(python3 -c "print(round($DOWN_TOTAL/5,3))")s rss_kb_after=$RSS2"
