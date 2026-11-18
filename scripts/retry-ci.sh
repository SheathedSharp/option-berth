#!/usr/bin/env bash
# Re-run a failed CI run only when the failure is outside this change's control.
#
# Called by the `retry` job in .github/workflows/ci.yml, which needs every area
# job. It runs when an area failed or was cancelled, and again on every attempt
# the re-run produces, so a flake leaves a record instead of turning green.
#
# The policy has two halves. `scripts/ci_failures.py` classifies the failing
# attempt and decides whether another attempt is allowed; this script owns the
# visible outcome:
#
#   retry=true    `gh run rerun <id> --failed`: only the failed jobs and the
#                 jobs that depend on them run again, at most twice.
#   retry=false   the job fails with the classification and the evidence line.
#                 A deterministic failure never reaches the re-run branch, and
#                 neither does a third attempt.
#
# Every attempt appends a `## 重跑记录` section to the run summary. A run that
# only went green on a later attempt still gets a warning annotation: a flake is
# never silently green.
#
# `Gate / required checks` keeps its meaning. It fails on the attempt that
# failed and turns green on the attempt the re-run produces, because the gate
# job depends on the area jobs and is re-run with them.
#
# Inputs, all set by the workflow:
#   AREAS_FAILED          "true" when an area job did not pass on this attempt
#   GITHUB_REPOSITORY     owner/repo
#   GITHUB_RUN_ID         the run to re-run
#   GITHUB_RUN_ATTEMPT    1-based attempt number of this job
#   GH_TOKEN              token with `actions: write`
#   GITHUB_STEP_SUMMARY   where the record goes
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
classifier="$root/scripts/ci_failures.py"
allowlist="$root/scripts/flaky-tests.txt"

: "${GH_TOKEN:?GH_TOKEN is required: reading jobs and re-running needs the actions API}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
: "${GITHUB_RUN_ATTEMPT:?GITHUB_RUN_ATTEMPT is required}"
: "${AREAS_FAILED:?AREAS_FAILED is required}"

repo=$GITHUB_REPOSITORY
run_id=$GITHUB_RUN_ID
attempt=$GITHUB_RUN_ATTEMPT

case $attempt in
    '' | *[!0-9]*)
        echo "retry-ci: GITHUB_RUN_ATTEMPT must be a positive integer, got '$attempt'" >&2
        exit 2
        ;;
esac

# The attempt that has to be explained. On an attempt that already fixed the
# failure this job still runs (see the job's `if`), and the answer comes from
# the attempt before it.
target=$attempt
if [ "$AREAS_FAILED" != "true" ]; then
    if [ "$attempt" -le 1 ]; then
        echo "retry-ci: no area failed and this is the first attempt; nothing to do"
        exit 0
    fi
    target=$((attempt - 1))
fi

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

# --- classify the failing attempt -------------------------------------------

# Display-only fallbacks for the case where the classifier cannot answer at all.
# ci_failures.py owns the policy value it overwrites below.
kind=unclassified
retry=false
max_attempts=3
evidence="第 $target 次尝试没有被分类"

classify_attempt() {
    # 0: classified, 2: nothing failed in that attempt, 1: every other reason,
    # and the caller must not re-run anything.
    local failed_ids id
    if ! gh api "repos/$repo/actions/runs/$run_id/attempts/$target/jobs?per_page=100" > "$work/jobs.json"; then
        evidence="读取第 $target 次尝试的任务列表失败，见本步骤日志"
        return 1
    fi
    if ! failed_ids=$(python3 "$classifier" --jobs "$work/jobs.json" --list-failed-jobs); then
        evidence="第 $target 次尝试的任务列表无法解析"
        return 1
    fi
    if [ -z "$failed_ids" ]; then
        evidence="第 $target 次尝试没有失败的任务"
        return 2
    fi
    mkdir -p "$work/logs"
    for id in $failed_ids; do
        # A job that never started has no log. The classifier reads a missing
        # log as no output, which never turns a failure into a candidate.
        if ! gh api "repos/$repo/actions/jobs/$id/logs" > "$work/logs/$id.log" 2> /dev/null; then
            : > "$work/logs/$id.log"
        fi
    done
    if ! python3 "$classifier" --jobs "$work/jobs.json" --log-dir "$work/logs" \
        --allowlist "$allowlist" --attempt "$attempt" --output "$work/verdict"; then
        evidence="第 $target 次尝试的失败无法分类"
        return 1
    fi
    kind=$(sed -n 's/^kind=//p' "$work/verdict")
    retry=$(sed -n 's/^retry=//p' "$work/verdict")
    max_attempts=$(sed -n 's/^max_attempts=//p' "$work/verdict")
    evidence=$(sed -n 's/^evidence=//p' "$work/verdict")
    return 0
}

status=0
classify_attempt || status=$?
echo "retry-ci: attempt $target is $kind: $evidence"

if [ "$status" -eq 2 ] && [ "$AREAS_FAILED" != "true" ]; then
    echo "retry-ci: attempt $target has no failed job; nothing to explain"
    exit 0
fi

# --- record it ---------------------------------------------------------------

record() {  # attempt kind evidence action
    [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
    {
        echo "## 重跑记录"
        echo
        echo "| 尝试 | 分类 | 证据 | 处理 |"
        echo "|---|---|---|---|"
        printf '| %s/%s | %s | %s | %s |\n' "$1" "$max_attempts" "$2" "${3//|/\\|}" "$4"
        echo
    } >> "$GITHUB_STEP_SUMMARY"
}

# --- decide ------------------------------------------------------------------

if [ "$AREAS_FAILED" != "true" ]; then
    # This attempt passed. Say what the failing attempt was, so the green result
    # is never mistaken for a clean one.
    if [ "$kind" = infrastructure ] || [ "$kind" = flaky-candidate ]; then
        record "$attempt" "$kind" "$evidence" "重跑后通过"
        echo "::warning title=CI 重跑后通过::第 $attempt 次尝试通过；第 $target 次尝试是 ${kind}：${evidence}"
    else
        record "$attempt" "$kind" "$evidence" "重跑后通过，前一分类不可重跑"
        echo "::warning title=CI 重跑后通过::第 $attempt 次尝试通过，但第 $target 次尝试的分类是 ${kind}（不重跑）：${evidence}。这次通过来自手工重跑或无法分类的失败，请确认它不是被掩盖的真实失败"
    fi
    exit 0
fi

if [ "$retry" != "true" ]; then
    case $kind in
        deterministic)
            record "$attempt" "$kind" "$evidence" "确定性失败，不重跑"
            echo "::error title=确定性失败，不重跑::${evidence}"
            ;;
        infrastructure | flaky-candidate)
            record "$attempt" "$kind" "$evidence" "重试次数用尽，保留原失败"
            echo "::error title=重试次数用尽::${kind} 已重跑 $((max_attempts - 1)) 次仍未通过：${evidence}"
            ;;
        *)
            record "$attempt" "$kind" "$evidence" "无法分类，不重跑"
            echo "::error title=无法分类，不重跑::${evidence}"
            ;;
    esac
    exit 1
fi

record "$attempt" "$kind" "$evidence" "重跑失败任务"
echo "::warning title=CI 重跑::${kind}，第 $attempt 次尝试，重跑失败任务：${evidence}"
if ! gh run rerun "$run_id" --repo "$repo" --failed; then
    # Name the state the call found, so the first real failure diagnoses itself:
    # a run this job is part of is still `in_progress` until this job ends.
    state=$(gh api "repos/$repo/actions/runs/$run_id" --jq .status 2> /dev/null || echo unknown)
    echo "::error title=无法重跑::gh run rerun $run_id --failed 失败，重跑时运行状态为 ${state}；本次运行保持失败，没有任务被重跑。只读令牌（来自 fork 的 PR）、缺少 actions: write、或运行尚未完成都会走到这里"
    exit 1
fi
echo "retry-ci: re-ran the failed jobs of run $run_id"
