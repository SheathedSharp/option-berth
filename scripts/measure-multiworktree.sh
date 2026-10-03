#!/bin/bash
# Multi-worktree real-link measurement for option-berth.
#
#   scripts/measure-multiworktree.sh <label> <oberth-binary>
#
# Builds a throwaway repository with one `port: auto` service, adds a linked
# worktree of it, brings both up under an isolated BERTH_HOME, and reports:
#   - the two auto ports (they must differ: one service, two checkouts)
#   - daemon external commands over a 12 s idle window with a subscriber
#   - daemon RSS
# Everything is created under /tmp and removed on exit; the user's own projects
# are never touched.
set -u
LABEL="${1:?usage: measure-multiworktree.sh <label> <binary>}"
BIN="${2:?usage: measure-multiworktree.sh <label> <binary>}"
ROOT="/tmp/berth-mw-$LABEL"
SHIM="$ROOT/shim"
HOME_DIR="$ROOT/home"
REPO="$ROOT/repo"
WT="$ROOT/wt"

rm -rf "$ROOT"; mkdir -p "$SHIM" "$HOME_DIR" "$REPO"
for pair in "lsof:/usr/sbin/lsof" "ps:/bin/ps" "git:/usr/bin/git" "docker:/opt/homebrew/bin/docker"; do
  name="${pair%%:*}"; real="${pair#*:}"
  cat > "$SHIM/$name" <<EOF
#!/bin/sh
printf '%s %s\n' "\$\$" "\$PPID $name \$*" >> $ROOT/calls.log
exec "$real" "\$@"
EOF
  chmod +x "$SHIM/$name"
done

cat > "$REPO/oberth.yaml" <<'YAML'
name: mw
services:
  - name: server
    cmd: python3 server.py
    port: auto
YAML
cat > "$REPO/server.py" <<'PY'
import os, socket, time
port = int(os.environ["PORT"])
ln = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
ln.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
ln.bind(("127.0.0.1", port))
ln.listen(5)
print("listening on %d" % port, flush=True)
while True:
    time.sleep(3600)
PY
git -C "$REPO" init -q
git -C "$REPO" -c user.email=t@t -c user.name=t add -A
git -C "$REPO" -c user.email=t@t -c user.name=t commit -qm init
git -C "$REPO" worktree add -q --detach "$WT" HEAD

export BERTH_HOME="$HOME_DIR" SHELL=/nonexistent PATH="$SHIM:$PATH"
DAEMON=""
cleanup() {
  "$BIN" daemon stop >/dev/null 2>&1
  [ -n "$DAEMON" ] && kill "$DAEMON" 2>/dev/null
  git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1
  pkill -f "$ROOT" >/dev/null 2>&1
}
trap cleanup EXIT

"$BIN" serve -d >"$ROOT/serve.log" 2>&1
sleep 1.5
DAEMON=$(sed -n 's/.*daemon started (pid \([0-9]*\)).*/\1/p' "$ROOT/serve.log" | head -1)
if [ -z "$DAEMON" ]; then echo "$LABEL ERROR: daemon did not start"; cat "$ROOT/serve.log"; exit 1; fi

( cd "$REPO" && "$BIN" up --allow-outside-home --json >"$ROOT/up-repo.json" 2>&1 )
( cd "$WT" && "$BIN" up --allow-outside-home --json >"$ROOT/up-wt.json" 2>&1 )
sleep 2

port_of() {
  "$BIN" status --json --no-mark 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
for s in d.get('worktree',{}).get('services',[]):
    if s['name']=='server' and s.get('port_actual'):
        print(s['port_actual']); break
"
}
P_REPO=$(cd "$REPO" && port_of)
P_WT=$(cd "$WT" && port_of)
echo "$LABEL ports: repo=$P_REPO wt=$P_WT distinct=$([ "$P_REPO" != "$P_WT" ] && echo yes || echo no)"

: > "$ROOT/calls.log"
( cd "$REPO" && "$BIN" events --worktree "$REPO" >/dev/null 2>&1 & echo $! > "$ROOT/sub.pid" )
sleep 12
kill "$(cat "$ROOT/sub.pid")" 2>/dev/null
CALLS=$(awk -v d="$DAEMON" '$2==d' "$ROOT/calls.log" | wc -l | tr -d ' ')
SCANS=$(grep -c -- "-iTCP -sTCP:LISTEN" "$ROOT/calls.log")
PSA=$(awk -v d="$DAEMON" '$2==d && $3=="ps"' "$ROOT/calls.log" | wc -l | tr -d ' ')
RSS=$(ps -o rss= -p "$DAEMON" | tr -d ' ')
echo "$LABEL idle(2wt): scans=$SCANS daemon_calls=$CALLS ps=$PSA rss_kb=$RSS"

( cd "$REPO" && "$BIN" down --json >/dev/null 2>&1 )
( cd "$WT" && "$BIN" down --json >/dev/null 2>&1 )
