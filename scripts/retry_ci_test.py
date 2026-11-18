"""Exercise scripts/retry-ci.sh against a stub `gh`: when it re-runs, when it refuses.

The re-run itself cannot be tested here. These tests pin which branch calls the
command, what it is called with, and what the run summary and the annotations
say afterwards.
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
        # The script finds the classifier and the allowlist next to itself, so
        # the test copies the real layout instead of patching the paths.
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for name in ("retry-ci.sh", "ci_failures.py"):
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
        self.summary = self.root / "summary.md"
        self.summary.write_text("")
        self.jobs = self.root / "jobs.json"
        self.log = self.root / "1.log"

    def prepare(self, *jobs, log=UNIT_LOG):
        self.jobs.write_text(json.dumps({"total_count": len(jobs), "jobs": list(jobs)}))
        self.log.write_text(log)

    def run_script(self, attempt, areas_failed, extra_env=None):
        environment = dict(
            os.environ,
            PATH=f"{self.bin_dir}{os.pathsep}{os.environ['PATH']}",
            GH_TOKEN="stub-token",
            GITHUB_REPOSITORY="owner/repo",
            GITHUB_RUN_ID="1",
            GITHUB_RUN_ATTEMPT=str(attempt),
            AREAS_FAILED=areas_failed,
            GITHUB_STEP_SUMMARY=str(self.summary),
            CALLS=str(self.calls),
            JOBS=str(self.jobs),
            LOG=str(self.log),
        )
        environment.update(extra_env or {})
        return subprocess.run(
            ["bash", str(self.script)],
            capture_output=True, text=True, errors="replace", timeout=60, env=environment,
        )

    def rerun_called(self):
        return "run rerun 1 --repo owner/repo --failed" in self.calls.read_text()

    def test_a_flaky_candidate_is_re_run_with_a_visible_record(self):
        self.allowlist.write_text(f"{PACKAGE}/TestBar\n")
        self.prepare(job("verify", steps=[("Full engine tests", "failure")]))
        result = self.run_script(1, "true")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.rerun_called(), self.calls.read_text())
        summary = self.summary.read_text()
        self.assertIn("## 重跑记录", summary)
        self.assertIn("| 1/3 | flaky-candidate |", summary)
        self.assertIn(f"{PACKAGE}/TestBar", summary)
        self.assertIn("重跑失败任务", summary)
        self.assertIn("::warning title=CI 重跑::flaky-candidate", result.stdout)

    def test_a_deterministic_failure_is_never_re_run(self):
        self.prepare(job("verify", steps=[("Native build", "failure")]))
        result = self.run_script(1, "true")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertIn("## 重跑记录", self.summary.read_text())
        self.assertIn("确定性失败，不重跑", self.summary.read_text())
        self.assertIn("::error title=确定性失败，不重跑::", result.stdout)

    def test_a_job_that_never_ran_a_step_is_re_run(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(1, "true")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.rerun_called())
        self.assertIn("| 1/3 | infrastructure |", self.summary.read_text())

    def test_the_third_attempt_keeps_the_original_failure(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(3, "true")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertIn("重试次数用尽，保留原失败", self.summary.read_text())
        self.assertIn("::error title=重试次数用尽::", result.stdout)

    def test_a_green_re_run_after_a_flake_is_announced(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(2, "false")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertIn("| 2/3 | infrastructure |", self.summary.read_text())
        self.assertIn("重跑后通过", self.summary.read_text())
        self.assertIn("::warning title=CI 重跑后通过::第 2 次尝试通过", result.stdout)

    def test_a_first_attempt_with_nothing_failed_does_nothing(self):
        self.prepare(job("verify", "success"))
        result = self.run_script(1, "false")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertEqual(self.summary.read_text(), "")
        self.assertNotIn("::warning", result.stdout)

    def test_the_previous_attempt_is_explained_when_this_one_passed(self):
        # Nothing re-runs a deterministic failure, so a pass after one came from
        # a human. It is still announced, never silently green.
        self.prepare(job("verify", steps=[("Native build", "failure")]))
        result = self.run_script(2, "false")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("| 2/3 | deterministic |", self.summary.read_text())
        self.assertIn("重跑后通过，前一分类不可重跑", self.summary.read_text())
        self.assertIn("::warning title=CI 重跑后通过::", result.stdout)

    def test_a_re_run_of_an_already_green_run_says_nothing(self):
        self.prepare(job("verify", "success"))
        result = self.run_script(2, "false")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertEqual(self.summary.read_text(), "")
        self.assertNotIn("::warning", result.stdout)

    def test_a_classifier_error_never_re_runs(self):
        self.jobs.write_text("{not json")
        result = self.run_script(1, "true")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(self.rerun_called())
        self.assertIn("无法分类，不重跑", self.summary.read_text())

    def test_a_failed_rerun_call_keeps_the_run_red(self):
        self.prepare(job("verify", "cancelled"))
        result = self.run_script(1, "true", {"RERUN_STATUS": "1"})
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("::error title=无法重跑::", result.stdout)


if __name__ == "__main__":
    unittest.main()
