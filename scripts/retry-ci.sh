#!/usr/bin/env bash
# Say what a completed verification run means, and re-run it only for a failure
# this change cannot fix.
#
# Called by .github/workflows/flake-retry.yml, which starts when a verification
# workflow completes. It has to be a separate workflow: `gh run rerun` only
# accepts a completed run, and a job inside the run it would re-run is by
# definition still going.
#
# The policy has two halves. `scripts/ci_failures.py` classifies one attempt and
# decides whether another attempt is allowed; this script owns the visible
# outcome:
#
#   failure                The attempt is classified. `infrastructure` and
#                          `flaky-candidate` below MAX_ATTEMPTS re-run the
#                          failed jobs; a deterministic failure, a shape the
#                          classifier does not recognise, and an attempt with
#                          none left all stand and fail this job.
#   success, attempt > 1   The re-run went green. The attempt before it is
#                          classified and reported, so a flake is never
#                          silently green.
#   anything else          A person cancelled the run, or it timed out, or it
#                          never started. Nothing is retried and nothing is
#                          written.
#
# Everything is fail-closed: an unreadable job list, an unparsable verdict and a
# record that could not be written all end the same way — no re-run.
#
# Where the record goes: the pull request the run belongs to. Without one (a
# nightly run, a workflow_dispatch, a pull request from a fork, whose
# `workflow_run.pull_requests` is empty) a failure that stands is escalated
# through `scripts/reconcile_repo.py --failure`, the single deduplicated issue
# per workflow. A retryable failure is re-run without opening that issue: the
# attempt that ends the story is the one that escalates.
#
# Inputs, all set by the workflow from the `workflow_run` payload:
#   RUN_ID             the completed run to classify and re-run. Never
#                      GITHUB_RUN_ID: that names the flake-retry run itself.
#   RUN_ATTEMPT        1-based attempt number of that run
#   RUN_CONCLUSION     failure | success | cancelled | ...
#   RUN_HTML_URL       the run's page, quoted in every record
#   WORKFLOW_NAME      the workflow that completed
#   PR_NUMBER          pull request number, may be empty
#   MAX_ATTEMPTS       attempts a run may spend in total; defaults to 3
#   GH_TOKEN           token with `actions: write` and `pull-requests: write`
#   GITHUB_REPOSITORY  owner/repo
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
classifier="$root/scripts/ci_failures.py"
reconcile="$root/scripts/reconcile_repo.py"
allowlist="$root/scripts/flaky-tests.txt"

required() {  # name value why
    if [ -z "$2" ]; then
        echo "retry-ci: $1 is required: $3" >&2
        exit 2
    fi
}

required GH_TOKEN "${GH_TOKEN:-}" "reading jobs, recording the outcome and re-running need it"
required GITHUB_REPOSITORY "${GITHUB_REPOSITORY:-}" "owner/repo"
required RUN_ID "${RUN_ID:-}" "the completed run to classify"
required RUN_ATTEMPT "${RUN_ATTEMPT:-}" "1-based attempt number of that run"
required RUN_CONCLUSION "${RUN_CONCLUSION:-}" "failure, success or another GitHub conclusion"
required RUN_HTML_URL "${RUN_HTML_URL:-}" "the record quotes it"
required WORKFLOW_NAME "${WORKFLOW_NAME:-}" "the record names it"
pr_number=${PR_NUMBER:-}
max_attempts=${MAX_ATTEMPTS:-3}

repo=$GITHUB_REPOSITORY

positive_integer() {  # value name
    case $1 in
        '' | *[!0-9]*)
            echo "retry-ci: $2 must be a positive integer, got '$1'" >&2
            exit 2
            ;;
    esac
    if [ "$1" -eq 0 ]; then
        echo "retry-ci: $2 must be 1 or greater, got '$1'" >&2
        exit 2
    fi
}

positive_integer "$RUN_ATTEMPT" RUN_ATTEMPT
positive_integer "$max_attempts" MAX_ATTEMPTS
if [ -n "$pr_number" ]; then
    positive_integer "$pr_number" PR_NUMBER
fi

attempt=$RUN_ATTEMPT

# The attempt that has to be explained: the failed one, or the one before a
# passing re-run.
target=$attempt
case $RUN_CONCLUSION in
    failure) ;;
    success)
        if [ "$attempt" -le 1 ]; then
            echo "retry-ci: $WORKFLOW_NAME run $RUN_ID passed on its first attempt; nothing to explain"
            exit 0
        fi
        target=$((attempt - 1))
        ;;
    *)
        echo "retry-ci: $WORKFLOW_NAME run $RUN_ID concluded '$RUN_CONCLUSION'; not a failure to explain, nothing retried"
        exit 0
        ;;
esac

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

# --- classify the attempt that has to be explained ---------------------------

# Display-only fallbacks for the case where the classifier cannot answer at all.
# ci_failures.py owns the policy value it overwrites below.
kind=unclassified
retry=false
evidence="第 $target 次尝试没有被分类"

classify_attempt() {
    # 0: classified, 2: nothing failed in that attempt, 1: every other reason,
    # and the caller must not re-run anything.
    local failed_ids id
    if ! gh api "repos/$repo/actions/runs/$RUN_ID/attempts/$target/jobs?per_page=100" > "$work/jobs.json"; then
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
        --allowlist "$allowlist" --attempt "$target" --max-attempts "$max_attempts" \
        --output "$work/verdict"; then
        evidence="第 $target 次尝试的失败无法分类"
        return 1
    fi
    kind=$(sed -n 's/^kind=//p' "$work/verdict")
    retry=$(sed -n 's/^retry=//p' "$work/verdict")
    evidence=$(sed -n 's/^evidence=//p' "$work/verdict")
    return 0
}

status=0
classify_attempt || status=$?
echo "retry-ci: $WORKFLOW_NAME run $RUN_ID attempt $target is $kind: $evidence"

if [ "$status" -eq 2 ]; then
    if [ "$RUN_CONCLUSION" = success ]; then
        echo "retry-ci: attempt $target has no failed job; there is nothing to explain"
        exit 0
    fi
    # A run can be marked failed with no failed job (a `startup_failure` never
    # creates one). The fallback kind already says it cannot be classified, and
    # an unclassified failure is never re-run.
    echo "retry-ci: attempt $target failed without a failed job; treating it as unclassified"
fi

# --- record it ---------------------------------------------------------------

comment_pr() {  # headline
    local headline=$1 body
    body=$(printf '%s\n\n- workflow: %s\n- run: %s\n- classification: %s\n- evidence: %s' \
        "$headline" "$WORKFLOW_NAME" "$RUN_HTML_URL" "$kind" "$evidence")
    if ! gh pr comment "$pr_number" --repo "$repo" --body "$body"; then
        echo "::error title=无法写入 PR 评论::gh pr comment $pr_number 失败；没有记录的重跑会掩盖失败，因此本次不重跑"
        return 1
    fi
}

escalate() {
    # The tool routes `success` to a recovery comment on the open issue and any
    # other conclusion to the issue itself; `cancelled` writes nothing, and this
    # script never gets that far with a cancellation.
    if ! python3 "$reconcile" --failure --repo "$repo" \
        --workflow-name "$WORKFLOW_NAME" --run-url "$RUN_HTML_URL" \
        --conclusion "$RUN_CONCLUSION"; then
        echo "::error title=无法写入 issue::scripts/reconcile_repo.py --failure 失败；没有记录的重跑会掩盖失败，因此本次不重跑"
        return 1
    fi
}

record() {  # headline
    if [ -n "$pr_number" ]; then
        comment_pr "$1" || exit 1
    else
        escalate || exit 1
    fi
}

# --- decide ------------------------------------------------------------------

if [ "$RUN_CONCLUSION" = success ]; then
    # This attempt passed. Say what the failing attempt was, so the green result
    # is never mistaken for a clean one.
    case $kind in
        infrastructure | flaky-candidate)
            record "flake: passed on attempt $attempt after $kind: $evidence"
            ;;
        *)
            record "flake: passed on attempt $attempt, but attempt $target was $kind and nothing here retries that: $evidence. This pass came from a manual re-run; confirm it is not a real failure being hidden"
            ;;
    esac
    echo "retry-ci: attempt $attempt passed; attempt $target was $kind"
    exit 0
fi

if [ "$retry" = true ]; then
    # A pull request gets the record first, so the flake is visible before the
    # green attempt exists. Without one the re-run is still spent: the attempt
    # that ends the story is the one that escalates.
    if [ -n "$pr_number" ]; then
        comment_pr "flake: attempt $attempt/$max_attempts concluded failure as $kind; re-running the failed jobs" || exit 1
    fi
    echo "::warning title=CI 重跑::${kind}，第 $attempt/$max_attempts 次尝试，重跑失败任务：${evidence}"
    if ! gh run rerun "$RUN_ID" --repo "$repo" --failed; then
        state=$(gh api "repos/$repo/actions/runs/$RUN_ID" --jq .status 2> /dev/null || echo unknown)
        echo "::error title=无法重跑::gh run rerun $RUN_ID --failed 失败，重跑时运行状态为 ${state}；本次运行保持失败，没有任务被重跑。只读令牌（来自 fork 的 PR）、缺少 actions: write、或运行尚未结束都会走到这里"
        exit 1
    fi
    echo "retry-ci: re-ran the failed jobs of $WORKFLOW_NAME run $RUN_ID (attempt $attempt/$max_attempts, $kind)"
    exit 0
fi

case $kind in
    deterministic)
        reason="确定性失败，不重跑"
        headline="flake: attempt $attempt/$max_attempts concluded failure as deterministic; not re-running"
        ;;
    unclassified)
        reason="无法分类，不重跑"
        headline="flake: attempt $attempt/$max_attempts failed and could not be classified; not re-running"
        ;;
    *)
        reason="重试次数用尽，保留原失败"
        headline="flake: attempt $attempt/$max_attempts concluded failure as $kind and no attempt is left; the failure stands"
        ;;
esac
record "$headline"
echo "::error title=$reason::$evidence"
exit 1
