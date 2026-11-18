"""Exercise the CI failure classifier: every branch, and fail-closed on unknown shapes."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("ci_failures.py")
sys.path.insert(0, str(SCRIPT.parent))

import ci_failures as failures  # noqa: E402

PACKAGE = "github.com/SheathedSharp/option-berth/engine/internal/foo"
OTHER = "github.com/SheathedSharp/option-berth/engine/internal/bar"


def job(name, conclusion="failure", steps=(), id=1):
    """One job as the REST API reports it: steps are (name, conclusion) pairs."""
    return {
        "id": id,
        "name": name,
        "conclusion": conclusion,
        "steps": [{"name": step, "conclusion": result} for step, result in steps],
    }


def payload(*jobs, total=None):
    return {"total_count": len(jobs) if total is None else total, "jobs": list(jobs)}


def stamp(*lines):
    """A raw runner log: every line carries the timestamp the API prepends."""
    return "".join(f"2026-01-02T03:04:05.1234567Z {line}\n" for line in lines)


UNIT_LOG = stamp(
    "##[group]Run set -o pipefail",
    "go test -count=1 -timeout=8m ./... 2>&1 | tee ../verification/unit.log",
    "--- FAIL: TestBar (0.00s)",
    "    foo_test.go:12: boom",
    "FAIL",
    f"FAIL\t{PACKAGE}\t0.123s",
)

COMPILE_LOG = stamp(
    "# github.com/SheathedSharp/option-berth/engine/internal/foo",
    "./foo.go:12:3: undefined: Missing",
    f"FAIL\t{PACKAGE} [build failed]",
)


def verdict_of(*jobs, allowlist=(), logs=None):
    return failures.classify(
        failures.parse_jobs(payload(*jobs)),
        frozenset(allowlist),
        {} if logs is None else logs,
    )


class AllowlistTests(unittest.TestCase):
    def test_nothing_is_a_candidate_by_default(self):
        self.assertEqual(failures.parse_allowlist(""), frozenset())
        self.assertEqual(failures.parse_allowlist("# nothing here yet\n"), frozenset())

    def test_comments_and_blank_lines_are_ignored(self):
        text = "# a package the whole test binary flakes on\n\n  \n" f"{PACKAGE}\n"
        self.assertEqual(failures.parse_allowlist(text), frozenset({PACKAGE}))

    def test_inline_comments_are_ignored(self):
        self.assertEqual(
            failures.parse_allowlist(f"{PACKAGE}/TestBar  # issue 404\n"),
            frozenset({f"{PACKAGE}/TestBar"}),
        )

    def test_a_bare_test_name_is_rejected(self):
        # An unqualified name would silently match nothing, which reads as
        # "no flake allowed" while looking like an entry.
        with self.assertRaises(ValueError) as caught:
            failures.parse_allowlist("TestBar\n")
        self.assertIn("line 1", str(caught.exception))

    def test_the_shipped_file_lists_no_candidate(self):
        shipped = Path(__file__).with_name("flaky-tests.txt")
        self.assertEqual(failures.load_allowlist(shipped), frozenset())


class GoOutputTests(unittest.TestCase):
    def test_a_failing_test_is_attributed_to_its_package(self):
        parsed = failures.parse_go_failures(UNIT_LOG)
        self.assertEqual(len(parsed), 1)
        self.assertEqual(parsed[0].package, PACKAGE)
        self.assertEqual(parsed[0].tests, ("TestBar",))
        self.assertFalse(parsed[0].build_failed)

    def test_several_packages_keep_their_own_tests(self):
        log = stamp(
            "--- FAIL: TestOne (0.00s)",
            f"FAIL\t{PACKAGE}\t0.1s",
            "--- FAIL: TestTwo (0.00s)",
            f"FAIL\t{OTHER}\t0.2s",
        )
        self.assertEqual(
            [(item.package, item.tests) for item in failures.parse_go_failures(log)],
            [(PACKAGE, ("TestOne",)), (OTHER, ("TestTwo",))],
        )

    def test_a_subtest_keeps_the_parent_name_too(self):
        log = stamp(
            "--- FAIL: TestBar (0.00s)",
            "    --- FAIL: TestBar/with_a_worktree (0.00s)",
            f"FAIL\t{PACKAGE}\t0.1s",
        )
        self.assertEqual(failures.parse_go_failures(log)[0].tests, ("TestBar", "TestBar/with_a_worktree"))

    def test_a_package_can_fail_without_a_test_name(self):
        log = stamp("panic: test timed out after 8m0s", "FAIL", f"FAIL\t{PACKAGE}\t480.1s")
        parsed = failures.parse_go_failures(log)
        self.assertEqual(parsed[0].tests, ())
        self.assertFalse(parsed[0].build_failed)

    def test_a_build_failure_is_marked(self):
        parsed = failures.parse_go_failures(COMPILE_LOG)
        self.assertEqual(parsed[0].package, PACKAGE)
        self.assertTrue(parsed[0].build_failed)

    def test_log_without_go_failures_parses_to_nothing(self):
        self.assertEqual(failures.parse_go_failures(""), ())
        self.assertEqual(failures.parse_go_failures(stamp("--- PASS: TestBar (0.00s)", "ok  \t" + PACKAGE)), ())


class ClassifyTests(unittest.TestCase):
    def test_a_job_that_never_ran_a_step_is_infrastructure(self):
        verdict = verdict_of(job("verify (ubuntu-24.04)", "cancelled"))
        self.assertEqual(verdict.kind, failures.INFRASTRUCTURE)
        self.assertTrue(verdict.retryable)
        self.assertIn("never ran a step", verdict.evidence)

    def test_steps_that_never_started_count_as_no_step(self):
        # A runner cancelled in the queue reports its steps with no conclusion.
        verdict = verdict_of(job("verify", "cancelled", steps=[("Set up job", None), ("Build", None)]))
        self.assertEqual(verdict.kind, failures.INFRASTRUCTURE)

    def test_a_tool_install_step_is_infrastructure(self):
        for name in ("Install package tools for tests", "Install actionlint", "go install nfpm"):
            with self.subTest(step=name):
                verdict = verdict_of(job("verify (ubuntu-24.04)", steps=[("Checkout", "success"), (name, "failure")]))
                self.assertEqual(verdict.kind, failures.INFRASTRUCTURE, verdict.evidence)
                self.assertIn(name, verdict.evidence)

    def test_an_allowlisted_test_is_a_flaky_candidate(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[f"{PACKAGE}/TestBar"],
            logs={1: UNIT_LOG},
        )
        self.assertEqual(verdict.kind, failures.FLAKY_CANDIDATE)
        self.assertTrue(verdict.retryable)
        self.assertIn(f"{PACKAGE}/TestBar", verdict.evidence)

    def test_an_allowlisted_package_is_a_flaky_candidate(self):
        log = stamp("panic: test timed out after 8m0s", f"FAIL\t{PACKAGE}\t480.1s")
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[PACKAGE],
            logs={1: log},
        )
        self.assertEqual(verdict.kind, failures.FLAKY_CANDIDATE)
        self.assertIn(PACKAGE, verdict.evidence)

    def test_a_test_outside_the_allowlist_is_deterministic(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[f"{OTHER}/TestBar"],
            logs={1: UNIT_LOG},
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)
        self.assertFalse(verdict.retryable)
        self.assertIn("TestBar", verdict.evidence)

    def test_a_build_failure_is_never_a_flake(self):
        # The allowlist pins a test that flakes, not a package that stopped compiling.
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[PACKAGE, f"{PACKAGE}/TestBar"],
            logs={1: COMPILE_LOG},
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)
        self.assertIn("did not compile", verdict.evidence)

    def test_a_step_that_runs_no_test_is_deterministic(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Native build", "failure")]),
            allowlist=[f"{PACKAGE}/TestBar"],
            logs={1: UNIT_LOG},
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)
        self.assertIn("Native build", verdict.evidence)

    def test_a_test_step_without_go_output_is_deterministic(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[f"{PACKAGE}/TestBar"],
            logs={1: stamp("no space left on device")},
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)
        self.assertIn("no Go test failure", verdict.evidence)

    def test_a_missing_log_is_treated_as_no_output(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")]),
            allowlist=[f"{PACKAGE}/TestBar"],
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)

    def test_deterministic_wins_over_infrastructure(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Native build", "failure")], id=1),
            job("windows-install-scripts", "cancelled", id=2),
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)

    def test_infrastructure_wins_over_a_flake(self):
        verdict = verdict_of(
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")], id=1),
            job("verify (macos-15)", steps=[("Install package tools for tests", "failure")], id=2),
            allowlist=[f"{PACKAGE}/TestBar"],
            logs={1: UNIT_LOG},
        )
        self.assertEqual(verdict.kind, failures.INFRASTRUCTURE)

    def test_skipped_area_jobs_are_ignored(self):
        verdict = verdict_of(
            job("Changes", "success", id=1),
            job("Examples", "skipped", id=2),
            job("verify (ubuntu-24.04)", steps=[("Install actionlint", "failure")], id=3),
        )
        self.assertEqual(verdict.kind, failures.INFRASTRUCTURE)
        self.assertIn("verify", verdict.evidence)

    def test_a_run_without_a_failed_job_is_deterministic(self):
        verdict = verdict_of(job("Changes", "success", id=1))
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)
        self.assertIn("no failed job", verdict.evidence)

    def test_a_job_that_failed_without_a_failed_step_is_deterministic(self):
        verdict = verdict_of(job("verify", "cancelled", steps=[("Native build", "success")]))
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)

    def test_an_unknown_conclusion_fails_closed(self):
        verdict = verdict_of(
            job("verify", "some_future_conclusion", steps=[("Full engine tests", "failure")]),
            allowlist=[f"{PACKAGE}/TestBar"],
            logs={1: UNIT_LOG},
        )
        self.assertEqual(verdict.kind, failures.DETERMINISTIC)

    def test_a_truncated_job_list_is_rejected(self):
        with self.assertRaises(ValueError) as caught:
            failures.parse_jobs(payload(job("verify"), total=101))
        self.assertIn("truncated", str(caught.exception))

    def test_an_unreadable_payload_is_rejected(self):
        for bad in ({}, {"jobs": "nope"}, {"jobs": [{"name": "x"}]}, []):
            with self.subTest(payload=bad):
                with self.assertRaises(ValueError):
                    failures.parse_jobs(bad)


class RetryTests(unittest.TestCase):
    def test_deterministic_is_never_retried(self):
        verdict = failures.Verdict(failures.DETERMINISTIC, "boom")
        for attempt in (1, 2):
            with self.subTest(attempt=attempt):
                self.assertFalse(failures.retry_allowed(verdict, attempt))

    def test_a_retryable_failure_is_retried_at_most_twice(self):
        for kind in (failures.INFRASTRUCTURE, failures.FLAKY_CANDIDATE):
            with self.subTest(kind=kind):
                verdict = failures.Verdict(kind, "boom")
                self.assertTrue(failures.retry_allowed(verdict, 1))
                self.assertTrue(failures.retry_allowed(verdict, 2))
                self.assertFalse(failures.retry_allowed(verdict, 3))

    def test_an_attempt_count_below_one_is_a_usage_error(self):
        with self.assertRaises(ValueError):
            failures.retry_allowed(failures.Verdict(failures.INFRASTRUCTURE, "boom"), 0)


class CliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def write(self, name, text):
        path = self.root / name
        path.write_text(text)
        return path

    def run_cli(self, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            capture_output=True, text=True, errors="replace", timeout=60,
        )

    def test_it_writes_the_verdict_for_the_workflow(self):
        jobs = self.write("jobs.json", json.dumps(payload(job("verify", steps=[("Full engine tests", "failure")]))))
        logs = self.root / "logs"
        logs.mkdir()
        (logs / "1.log").write_text(UNIT_LOG)
        allowlist = self.write("flaky.txt", f"{PACKAGE}/TestBar\n")
        output = self.root / "out"
        result = self.run_cli("--jobs", str(jobs), "--log-dir", str(logs),
                              "--allowlist", str(allowlist), "--attempt", "1", "--output", str(output))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            dict(line.split("=", 1) for line in output.read_text().splitlines()),
            {"kind": failures.FLAKY_CANDIDATE, "retry": "true", "max_attempts": "3",
             "evidence": f"verify: {PACKAGE}/TestBar is in the flaky allowlist"},
        )
        self.assertIn(failures.FLAKY_CANDIDATE, result.stdout)

    def test_it_reports_that_the_last_attempt_may_not_retry(self):
        jobs = self.write("jobs.json", json.dumps(payload(job("verify", "cancelled"))))
        output = self.write("out", "")
        result = self.run_cli("--jobs", str(jobs), "--attempt", "3", "--output", str(output))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("kind=infrastructure", output.read_text())
        self.assertIn("retry=false", output.read_text())

    def test_it_lists_the_jobs_whose_logs_matter(self):
        jobs = self.write("jobs.json", json.dumps(payload(
            job("Changes", "success", id=7),
            job("verify (ubuntu-24.04)", steps=[("Full engine tests", "failure")], id=8),
            job("Examples", "skipped", id=9),
        )))
        result = self.run_cli("--jobs", str(jobs), "--list-failed-jobs")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.split(), ["8"])

    def test_an_attempt_number_below_one_exits_non_zero(self):
        jobs = self.write("jobs.json", json.dumps(payload(job("verify", "cancelled"))))
        result = self.run_cli("--jobs", str(jobs), "--attempt", "0", "--output", str(self.root / "out"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("attempt must be 1 or greater", result.stderr)

    def test_an_unreadable_job_list_exits_non_zero(self):
        result = self.run_cli("--jobs", str(self.root / "missing.json"), "--output", str(self.root / "out"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing.json", result.stderr)

    def test_a_malformed_allowlist_exits_non_zero(self):
        jobs = self.write("jobs.json", json.dumps(payload(job("verify", steps=[("Full engine tests", "failure")]))))
        allowlist = self.write("flaky.txt", "TestBar\n")
        result = self.run_cli("--jobs", str(jobs), "--allowlist", str(allowlist), "--output", str(self.root / "out"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("line 1", result.stderr)

    def test_the_default_output_target_is_github_output(self):
        jobs = self.write("jobs.json", json.dumps(payload(job("verify", "cancelled"))))
        output = self.write("github_output", "")
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--jobs", str(jobs)],
            capture_output=True, text=True, errors="replace", timeout=60,
            env=dict(os.environ, GITHUB_OUTPUT=str(output)),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("kind=infrastructure", output.read_text())


if __name__ == "__main__":
    unittest.main()
