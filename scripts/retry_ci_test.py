"""Exercise scripts/retry-ci.sh against a stub `gh`: what it re-runs, what it records.

Neither half of the real thing can be tested here. The re-run is an API call,
and the `workflow_run` trigger only starts firing once
.github/workflows/flake-retry.yml is on the default branch. These tests pin the
decision the script makes from the completed run it is handed, the commands it
runs, and the record it leaves on the pull request.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest

SCRIPTS = Path(__file__).parent
PACKAGE = "github.com/SheathedSharp/option-berth/engine/internal/foo"
WORKFLOW = "CI"
RUN_URL = "https://github.com/owner/repo/actions/runs/1"

STUB = textwrap.dedent("""\
    #!/usr/bin/env bash
    printf '%s\\n' "$*" >> "$CALLS"
    if [ "$1" = api ]; then
        case "$2" in
            */jobs\\?per_page=100) cat "$JOBS" ;;
            */logs) if [ -n "${LOG:-}" ] && [ -f "$LOG" ]; then cat "$LOG"; else exit 1; fi ;;
            *) echo "stub: unexpected api call $2" >&2; exit 1 ;;
        esac
        exit 0
    fi
    if [ "$1 $2" = "run rerun" ]; then exit "${RERUN_STATUS:-0}"; fi
    if [ "$1 $2" = "pr comment" ]; then
        printf '%s\\n---\\n' "$7" >> "$COMMENTS"
        exit "${COMMENT_STATUS:-0}"
    fi
    # scripts/reconcile_repo.py --failure: no open issue yet, so it creates one.
    if [ "$1 $2" = "issue list" ]; then echo '[]'; exit 0; fi
    if [ "$1 $2" = "issue create" ]; then exit "${ISSUE_STATUS:-0}"; fi
    echo "stub: unexpected command $*" >&2
    exit 1
""")


def job(name, conclusion="failure", steps=(), id=1):
    return {
        "id": id,
        "name": name,
        "conclusion": conclusion,
        "steps": [{"name": step, "conclusion": result} for step, result in steps],
    }


UNIT_LOG = (
    "2026-01-02T03:04:05.1234567Z --- FAIL: TestBar (0.00s)\n"
    f"2026-01-02T03:04:05.1234567Z FAIL\t{PACKAGE}\t0.123s\n"
)


class RetryCiTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        # The script finds the classifier, the allowlist and the escalation tool
        # next to itself, so the test copies the real layout instead of patching
        # the paths.
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for name in ("retry-ci.sh", "ci_failures.py", "reconcile_repo.py", "check_ci_gate.py"):
            shutil.copy(SCRIPTS / name, self.scripts / name)
        self.allowlist = self.scripts / "flaky-tests.txt"
        self.allowlist.write_text("# nothing allowlisted\n")
        self.script = self.scripts / "retry-ci.sh"

        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        stub = bin_dir / "gh"
        stub.write_text(STUB)
        stub.chmod(0o755)
        self.bin_dir = bin_dir

        self.calls = self.root / "calls"
        self.calls.write_text("")
        self.comments = self.root / "comments"
        self.comments.write_text("")
        self.jobs = self.root / "jobs.json"
        self.log = self.root / "1.log"

    def prepare(self, *jobs, log=UNIT_LOG):
        self.jobs.write_text(json.dumps({"total_count": len(jobs), "jobs": list(jobs)}))
        self.log.write_text(log)

    def run_script(self, *, attempt=1, conclusion="failure", pr="7", extra_env=None):
        environment = dict(
            os.environ,
            PATH=f"{self.bin_dir}{os.pathsep}{os.environ['PATH']}",
            GH_TOKEN="stub-token",
            GITHUB_REPOSITORY="owner/repo",
            RUN_ID="1",
            RUN_ATTEMPT=str(attempt),
            RUN_CONCLUSION=conclusion,
            RUN_HTML_URL=RUN_URL,
            WORKFLOW_NAME=WORKFLOW,
            PR_NUMBER=pr,
            # The id of the flake-retry run itself. The script must never re-run
            # it: that run is in progress, and `gh run rerun` rejects it.
            GITHUB_RUN_ID="999",
            CALLS=str(self.calls),
            COMMENTS=str(self.comments),
            JOBS=str(self.jobs),
            LOG=str(self.log),
        )
        environment.update(extra_env or {})
        return subprocess.run(
            ["bash", str(self.script)],
            capture_output=True, text=True, errors="replace", timeout=60, env=environment,
        )

    def command_line(self):
        return " ".join(self.calls.read_text().split())

    def comment_body(self):
        return self.comments.read_text()

    def test_a_retryable_failure_is_recorded_and_re_run(self):
        self.allowlist.write_text(f"{PACKAGE}/TestBar\n")
        self.prepare(job("verify", steps=[("Full engine tests", "failure")]))
        result = self.run_script(attempt=1)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        # The completed run from the payload, never this workflow's own run.
        self.assertIn("run rerun 1 --repo owner/repo --failed", self.command_line())
        self.assertNotIn("999", self.command_line())
        body = self.comment_body()
        self.assertIn("flake: attempt 1/3", body)
        self.assertIn("flaky-candidate", body)
        self.assertIn(f"{PACKAGE}/TestBar", body)
        self.assertIn(RUN_URL, body)
        self.assertIn("::warning title=CI 重跑::flaky-candidate", result.stdout)

    def test_a_deterministic_failure_is_recorded_and_not_re_run(self):
        self.prepare(job("verify", steps=[("Native build", "failure")]))
        result = self.run_script(attempt=1)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        body = self.comment_body()
        self.assertIn("deterministic", body)
        self.assertIn("Native build", body)
        self.assertIn("::error title=确定性失败，不重跑::", result.stdout)

    def test_the_last_attempt_keeps_the_failure(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=3)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        body = self.comment_body()
        self.assertIn("flake: attempt 3/3", body)
        self.assertIn("infrastructure", body)
        self.assertIn("::error title=重试次数用尽，保留原失败::", result.stdout)

    def test_the_budget_comes_from_max_attempts(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=2, extra_env={"MAX_ATTEMPTS": "2"})
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        self.assertIn("flake: attempt 2/2", self.comment_body())

    def test_a_pass_after_a_retry_names_the_flake_on_the_pull_request(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=2, conclusion="success")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        # Attempt 2 is green, so the attempt that has to be explained is 1.
        self.assertIn("attempts/1/jobs", self.command_line())
        self.assertIn("flake: passed on attempt 2 after infrastructure: ", self.comment_body())

    def test_a_pass_after_a_failure_nothing_retried_is_still_recorded(self):
        # Nothing re-runs a deterministic failure, so a pass after one came from
        # a person. It is announced, never silently green.
        self.prepare(job("verify", steps=[("Native build", "failure")]))
        result = self.run_script(attempt=2, conclusion="success")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        body = self.comment_body()
        self.assertIn("flake: passed on attempt 2", body)
        self.assertIn("deterministic", body)

    def test_a_re_run_of_an_already_green_run_says_nothing(self):
        # Every job of attempt 1 passed, so there is no failure to explain and
        # nothing to write on the pull request.
        self.prepare(job("verify", "success"))
        result = self.run_script(attempt=2, conclusion="success")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        self.assertEqual(self.comment_body(), "")
        self.assertEqual(self.calls.read_text().count("\n"), 1, self.command_line())

    def test_a_pass_on_the_first_attempt_says_nothing(self):
        self.prepare(job("verify", "success"))
        result = self.run_script(attempt=1, conclusion="success")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.calls.read_text(), "")
        self.assertEqual(self.comment_body(), "")

    def test_a_cancelled_run_is_not_retried(self):
        # A person cancelled it; that is not a failure to explain.
        self.prepare(job("verify", "cancelled"))
        for conclusion in ("cancelled", "timed_out", "skipped", "startup_failure"):
            with self.subTest(conclusion=conclusion):
                self.calls.write_text("")
                self.comments.write_text("")
                result = self.run_script(attempt=2, conclusion=conclusion)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(self.calls.read_text(), "")
                self.assertEqual(self.comment_body(), "")

    def test_a_failure_without_a_pull_request_escalates_instead_of_commenting(self):
        self.prepare(job("verify", steps=[("Native build", "failure")]))
        result = self.run_script(attempt=1, pr="")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("pr comment", self.command_line())
        self.assertIn("issue create", self.command_line())
        self.assertIn("[automation] CI 失败", self.command_line())

    def test_a_retryable_failure_without_a_pull_request_is_re_run_without_an_issue(self):
        # The re-run may still go green; opening an issue for an attempt that is
        # about to be spent would page for a flake. The attempt that ends the
        # story is the one that escalates.
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=1, pr="")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("run rerun 1 --repo owner/repo --failed", self.command_line())
        self.assertNotIn("issue create", self.command_line())

    def test_a_pass_after_a_retry_without_a_pull_request_escalates(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=2, conclusion="success", pr="")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        # reconcile_repo.py routes `success` to a recovery comment, not an issue.
        self.assertIn("issue list", self.command_line())
        self.assertNotIn("issue create", self.command_line())

    def test_a_classifier_error_never_re_runs(self):
        self.jobs.write_text("{not json")
        result = self.run_script(attempt=1)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        self.assertIn("::error title=无法分类，不重跑::", result.stdout)

    def test_a_failed_comment_never_re_runs(self):
        # The record is the point: a re-run nobody can see would hide the flake.
        self.allowlist.write_text(f"{PACKAGE}/TestBar\n")
        self.prepare(job("verify", steps=[("Full engine tests", "failure")]))
        result = self.run_script(attempt=1, extra_env={"COMMENT_STATUS": "1"})
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertNotIn("run rerun", self.command_line())
        self.assertIn("::error title=无法写入 PR 评论::", result.stdout)

    def test_a_failed_rerun_call_keeps_the_run_red(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=1, extra_env={"RERUN_STATUS": "1"})
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("::error title=无法重跑::", result.stdout)

    def test_a_missing_input_is_a_usage_error(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(attempt=1, extra_env={"RUN_ID": ""})
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn("RUN_ID", result.stderr)
        self.assertEqual(self.calls.read_text(), "")


if __name__ == "__main__":
    unittest.main()
