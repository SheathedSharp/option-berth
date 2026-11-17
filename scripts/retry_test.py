"""Exercise scripts/retry.sh: attempt count, backoff lines, and exit status."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("retry.sh")


def run(args, timeout=30, env=None):
    return subprocess.run(
        ["bash", "-c", 'source "$1"; shift; retry "$@"', "retry-test", str(SCRIPT), *args],
        capture_output=True, text=True, errors="replace", timeout=timeout,
        env=None if env is None else dict(os.environ, **env),
    )


class RetryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.counter = Path(self.tmp.name) / "count"
        self.counter.write_text("0")

    def attempts_seen(self):
        return int(self.counter.read_text())

    def flaky(self, succeed_on):
        """Fail until the Nth attempt, then succeed; count every invocation."""
        return ["bash", "-c",
                f'n=$(( $(cat {self.counter}) + 1 )); echo $n > {self.counter}; '
                f'echo "run $n"; [ "$n" -ge {succeed_on} ]']

    def test_success_does_not_sleep_and_runs_once(self):
        result = run(["4", "0", "--", *self.flaky(1)])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.attempts_seen(), 1)
        self.assertIn("retry: attempt 1/4", result.stderr)
        self.assertNotIn("sleeping", result.stderr)

    def test_failure_is_retried_until_it_passes(self):
        result = run(["4", "0", "--", *self.flaky(3)])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.attempts_seen(), 3)
        self.assertIn("sleeping", result.stderr)

    def test_gives_up_after_the_last_attempt_with_the_command_status(self):
        result = run(["3", "0", "--", "bash", "-c", "exit 7"])
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn("retry: attempt 3/3", result.stderr)
        self.assertIn("giving up after 3 attempt(s); last exit status 7", result.stderr)
        # Three attempts sleep twice; the final failure never sleeps.
        self.assertEqual(result.stderr.count("sleeping"), 2)

    def test_backoff_doubles_between_attempts(self):
        result = run(["3", "1", "--", "bash", "-c", "exit 1"])
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("sleeping 1s", result.stderr)
        self.assertIn("sleeping 2s", result.stderr)

    def test_backoff_is_capped(self):
        result = run(["3", "4", "--", "bash", "-c", "exit 1"], env={"RETRY_MAX_SLEEP": "1"})
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stderr.count("sleeping 1s"), 2)
        self.assertNotIn("sleeping 4s", result.stderr)

    def test_output_goes_to_the_command_not_the_helper(self):
        result = run(["2", "0", "--", "bash", "-c", "echo payload"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "payload\n")

    def test_missing_separator_is_a_usage_error(self):
        result = run(["3", "0", "true"])
        self.assertEqual(result.returncode, 2)
        self.assertIn("expected -- before the command", result.stderr)

    def test_non_numeric_arguments_are_rejected(self):
        for args in (["x", "0", "--", "true"], ["3", "y", "--", "true"], ["0", "0", "--", "true"]):
            with self.subTest(args=args):
                result = run(args)
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_no_command_is_a_usage_error(self):
        result = run(["3", "0", "--"])
        self.assertEqual(result.returncode, 2)
        self.assertIn("no command given", result.stderr)

    def test_too_few_arguments_is_a_usage_error(self):
        result = run([])
        self.assertEqual(result.returncode, 2)
        self.assertIn("usage: retry", result.stderr)

    def test_sourcing_defines_the_function_without_running_anything(self):
        result = subprocess.run(
            ["bash", "-c", f'source {SCRIPT}; command -v retry >/dev/null && echo defined'],
            capture_output=True, text=True, errors="replace", timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "defined\n")
        self.assertEqual(result.stderr, "")

    def test_sourced_use_survives_errexit_like_a_workflow_step(self):
        # GitHub runs bash steps with -e; a bare test failing inside the helper
        # must not abort the whole step.
        result = subprocess.run(
            ["bash", "-e", "-c",
             'source "$1"; shift; retry 3 0 -- bash -c "exit 4"; echo "unreachable"',
             "retry-test", str(SCRIPT)],
            capture_output=True, text=True, errors="replace", timeout=30,
        )
        self.assertEqual(result.returncode, 4, result.stderr)
        self.assertNotIn("unreachable", result.stdout)
        self.assertIn("giving up after 3 attempt(s)", result.stderr)

    def test_running_it_as_a_script_uses_the_same_arguments(self):
        result = subprocess.run(
            ["bash", str(SCRIPT), "2", "0", "--", "bash", "-c", "exit 3"],
            capture_output=True, text=True, errors="replace", timeout=30,
        )
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("retry: attempt 2/2", result.stderr)


if __name__ == "__main__":
    unittest.main()
