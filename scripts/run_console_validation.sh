#!/usr/bin/env bash
# Run console-related native and engine checks from docs/validation.md (isolated HOME).
# Usage: bash scripts/run_console_validation.sh [--with-client]
#   QUIET=1  passes BERTH_CONSOLE_TEST_QUIET=1 to ConsoleChecks (local focus-friendly).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WITH_CLIENT=0
for arg in "$@"; do
  case "$arg" in
    --with-client) WITH_CLIENT=1 ;;
    -h|--help)
      echo "Usage: bash scripts/run_console_validation.sh [--with-client]"
      echo "  QUIET=1  quieter ConsoleChecks (see docs/validation.md)"
      exit 0
      ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

cd "$ROOT/client/macos"
swift run WorkspaceChecks
swift run AgentChecks
swift run TerminalChecks

isolation="$(mktemp -d)"
cleanup() { rm -rf "$isolation"; }
trap cleanup EXIT
mkdir -p "$isolation/home/.option-berth"

console_env=(
  HOME="$isolation/home" CFFIXED_USER_HOME="$isolation/home"
  BERTH_HOME="$isolation/home/.option-berth" SHELL=/bin/zsh ZDOTDIR="$isolation/home"
  BERTH_CONSOLE_TEST=1
)
if [[ "${QUIET:-}" == "1" ]]; then
  console_env+=(BERTH_CONSOLE_TEST_QUIET=1)
fi
env "${console_env[@]}" swift run ConsoleChecks

cd "$ROOT/engine"
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" go test ./internal/agentstream/... -race -count=1

cd "$ROOT"
python3 scripts/check_client_configuration.py

if [[ "$WITH_CLIENT" == 1 ]]; then
  binary_dir="$(swift build --package-path client/macos --show-bin-path)"
  isolation2="$(mktemp -d)"
  trap 'rm -rf "$isolation" "$isolation2"' EXIT
  mkdir -p "$isolation2/home" "$isolation2/berth"
  HOME="$isolation2/home" CFFIXED_USER_HOME="$isolation2/home" BERTH_HOME="$isolation2/berth" \
    "$binary_dir/ClientChecks"
fi

echo "PASS: console validation bundle (see docs/validation.md; hand tests still required)"
