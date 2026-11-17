#!/usr/bin/env bash
# Retry a network-dependent command a few times before failing.
#
#   retry <attempts> <base-seconds> -- <command...>
#
# Use it for fetching tools and packages (go install, apt-get update/install),
# where a mirror or proxy hiccup is not a statement about this change. Do not
# wrap a deterministic check with it: retrying go test, go vet, a build or a
# release verification only buries a real failure behind a slower red build and
# spends the runner's time proving the same result again.
#
# Source it from a bash step to get the function, or run it as a script with
# the same arguments. Sleeps base, 2*base, 4*base ... capped at
# $RETRY_MAX_SLEEP (default 30 seconds), and never sleeps after the final
# attempt. Every attempt writes one line to stderr.
#
# Usage in a workflow step:
#   source scripts/retry.sh
#   retry 4 5 -- go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.46.0

retry() {
    if [ "$#" -lt 3 ]; then
        echo "usage: retry <attempts> <base-seconds> -- <command...>" >&2
        return 2
    fi

    attempts=$1
    base=$2
    shift 2
    if [ "${1:-}" != "--" ]; then
        echo "retry: expected -- before the command, got '$1'" >&2
        return 2
    fi
    shift

    case $attempts in
        '' | *[!0-9]*)
            echo "retry: attempts must be a positive integer, got '$attempts'" >&2
            return 2
            ;;
    esac
    case $base in
        '' | *[!0-9]*)
            echo "retry: base-seconds must be a non-negative integer, got '$base'" >&2
            return 2
            ;;
    esac
    if [ "$attempts" -lt 1 ]; then
        echo "retry: attempts must be at least 1" >&2
        return 2
    fi
    if [ "$#" -eq 0 ]; then
        echo "retry: no command given" >&2
        return 2
    fi

    max_sleep=${RETRY_MAX_SLEEP:-30}
    attempt=1
    while :; do
        echo "retry: attempt $attempt/$attempts: $*" >&2
        if "$@"; then
            return 0
        else
            status=$?
        fi
        if [ "$attempt" -ge "$attempts" ]; then
            echo "retry: giving up after $attempts attempt(s); last exit status $status" >&2
            return "$status"
        fi
        delay=$((base << (attempt - 1)))
        # Written as a condition: a bare `[ ... ]` that returns false would trip
        # `set -e` in the calling step instead of just skipping the clamp.
        if [ "$delay" -gt "$max_sleep" ]; then
            delay=$max_sleep
        fi
        echo "retry: attempt $attempt/$attempts failed with exit status $status; sleeping ${delay}s" >&2
        sleep "$delay"
        attempt=$((attempt + 1))
    done
}

# Only run when executed, so `source scripts/retry.sh` just defines the function.
if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then
    retry "$@"
fi
