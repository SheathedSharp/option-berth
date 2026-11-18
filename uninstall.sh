#!/bin/sh
#
# Remove an option-berth CLI installed by install.sh.
#
#   curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh
#
# The script unregisters the user-level service unit install.sh registered, stops the
# Runtime Service if it can, removes oberth and the notices install.sh placed under
# ~/.local/share/option-berth, and removes the PATH lines the installer added. It
# keeps ~/.option-berth state unless --purge-state is passed.
#
# Flags:
#   --dir DIR        install directory (default: $HOME/.local/bin)
#   --data-dir DIR   notices directory (default: ${XDG_DATA_HOME:-$HOME/.local/share}/option-berth)
#   --keep-path      leave shell startup files untouched
#   --purge-state    also remove ~/.option-berth (or $BERTH_HOME)
set -eu

DIR="${BERTH_INSTALL_DIR:-$HOME/.local/bin}"
DATA_DIR="${BERTH_INSTALL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/option-berth}"
STATE_DIR="${BERTH_HOME:-$HOME/.option-berth}"
KEEP_PATH=0
PURGE_STATE=0

# Where install.sh registered the runtime service. macOS and Linux each have one
# location; any other platform has none, and the removal step is skipped.
os=$(uname -s)
SERVICE_FILE=""
case "$os" in
Darwin)
	SERVICE_LABEL=com.sheathedsharp.option-berth.runtime
	SERVICE_FILE="$HOME/Library/LaunchAgents/$SERVICE_LABEL.plist"
	;;
Linux)
	SERVICE_UNIT=option-berth-runtime.service
	SERVICE_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/$SERVICE_UNIT"
	;;
esac

fail() {
	echo "uninstall: $*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Remove an option-berth CLI installed by install.sh.

  curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.sh | sh

Flags:
  --dir DIR        install directory (default: $HOME/.local/bin)
  --data-dir DIR   notices directory (default: ~/.local/share/option-berth)
  --keep-path      leave shell startup files untouched
  --purge-state    also remove ~/.option-berth (or $BERTH_HOME)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir)
		[ $# -ge 2 ] || fail "--dir needs a value"
		DIR=$2
		shift 2
		;;
	--data-dir)
		[ $# -ge 2 ] || fail "--data-dir needs a value"
		DATA_DIR=$2
		shift 2
		;;
	--keep-path)
		KEEP_PATH=1
		shift
		;;
	--purge-state)
		PURGE_STATE=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		fail "unknown argument: $1"
		;;
	esac
done

case "$DIR" in
"~/"*) DIR="$HOME/${DIR#~/}" ;;
esac
case "$DATA_DIR" in
"~/"*) DATA_DIR="$HOME/${DATA_DIR#~/}" ;;
esac

# remove_service takes the unit away before anything else. A unit left behind would
# have the supervisor start a runtime service whose binary this script is about to
# delete, and it would keep doing that at every login. The supervisor being missing
# or refusing is not a failure: the file still has to go.
remove_service() {
	if [ -z "$SERVICE_FILE" ] || [ ! -f "$SERVICE_FILE" ]; then
		return 0
	fi
	case "$os" in
	Darwin)
		uid=$(id -u)
		launchctl bootout "gui/$uid/$SERVICE_LABEL" >/dev/null 2>&1 ||
			launchctl bootout "user/$uid/$SERVICE_LABEL" >/dev/null 2>&1 || true
		;;
	Linux)
		systemctl --user disable --now "$SERVICE_UNIT" >/dev/null 2>&1 || true
		;;
	esac
	rm -f "$SERVICE_FILE"
	echo "service    removed $SERVICE_FILE"
	if [ "$os" = Linux ]; then
		systemctl --user daemon-reload >/dev/null 2>&1 || true
	fi
}

remove_service

if [ -x "$DIR/oberth" ]; then
	# A retired CLI must never be silently treated as an absent new host.
	# Only metadata/help is probed; no old command or RPC is attempted.
	"$DIR/oberth" runtime service --help >/dev/null 2>&1 ||
		fail "the installed CLI predates runtime service; stop its host explicitly with its matching CLI and use its matching uninstaller"
	if stopped=$("$DIR/oberth" runtime service stop --json 2>/dev/null); then
		echo "runtime service stopped"
	else
		case "$stopped" in
		*'"running": false'*|*'"running":false'*) echo "runtime service not running" ;;
		*) fail "Runtime Service stop failed; installation and state were left unchanged" ;;
		esac
	fi
fi

removed=""
for name in oberth; do
	if [ -e "$DIR/$name" ]; then
		rm -f "$DIR/$name"
		removed="$removed $name"
	fi
done
if [ -n "$removed" ]; then
	echo "removed    ${removed# } from $DIR"
else
	echo "removed    nothing from $DIR"
fi

# Remove only the files this project put there, never the whole directory: a
# custom --data-dir may contain something the caller owns too.
if [ -f "$DATA_DIR/LICENSE" ] || [ -f "$DATA_DIR/build-manifest.json" ] || [ -d "$DATA_DIR/licenses" ]; then
	rm -f "$DATA_DIR/LICENSE" "$DATA_DIR/build-manifest.json"
	rm -rf "$DATA_DIR/licenses"
	rmdir "$DATA_DIR" 2>/dev/null || true
	echo "removed    notices from $DATA_DIR"
fi

if [ "$KEEP_PATH" = 1 ]; then
	echo "PATH       left alone (--keep-path)"
else
	case "$DIR" in
	"$HOME"/*) expr='$HOME'"${DIR#"$HOME"}" ;;
	*) expr="$DIR" ;;
	esac
	# Two forms: the bare prepend older installs wrote, and the guarded one that
	# keeps a shell reading several startup files from putting the directory on
	# PATH twice. Both belong to this project, so both are removed.
	line="export PATH=\"$expr:\$PATH\""
	guarded="case \":\$PATH:\" in *\":$expr:\"*) ;; *) export PATH=\"$expr:\$PATH\" ;; esac"
	comment="# option-berth CLI (installed in $expr)"
	changed=""
	for f in .zshrc .zshenv .bashrc .bash_profile .bash_login .profile; do
		[ -f "$HOME/$f" ] || continue
		tmp="$HOME/.$f.berth-uninstall.$$"
		if awk -v line="$line" -v guarded="$guarded" -v comment="$comment" -v expr="$expr" '
			$0 == line { next }
			$0 == guarded { next }
			$0 == comment { next }
			$0 ~ /^# option-berth/ && index($0, expr) { next }
			{ print }
		' "$HOME/$f" >"$tmp"; then
			if ! cmp -s "$HOME/$f" "$tmp"; then
				cat "$tmp" >"$HOME/$f"
				changed="$changed $f"
			fi
		else
			echo "PATH       could not update $f" >&2
		fi
		rm -f "$tmp"
	done
	if [ -n "$changed" ]; then
		echo "PATH       removed from ${changed# } — open a new terminal"
	else
		echo "PATH       no installer line found; nothing changed"
	fi
fi

if [ "$PURGE_STATE" = 1 ]; then
	case "$STATE_DIR" in
	/*) ;;
	*) fail "refusing to remove non-absolute state directory: $STATE_DIR" ;;
	esac
	if [ -z "$STATE_DIR" ] || [ "$STATE_DIR" = "/" ] || [ "$STATE_DIR" = "$HOME" ]; then
		fail "refusing to remove $STATE_DIR"
	fi
	rm -rf "$STATE_DIR"
	echo "state      removed $STATE_DIR"
elif [ -d "$STATE_DIR" ]; then
	echo "state      kept $STATE_DIR (remove with --purge-state)"
fi
