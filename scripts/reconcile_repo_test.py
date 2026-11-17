"""Reconcile the quality gate, open pull requests and failed runs without writing."""
import importlib.util
import json
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("berth_reconcile", Path(__file__).with_name("reconcile_repo.py"))
reconcile = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reconcile)

DRIFT = reconcile.Drift(
    missing_workflows=("Gate / required checks",),
    missing_ruleset=("Gate integrity / maintainer-owned CI changes",),
    extra_ruleset=("Legacy / retired check",),
)


def pull(number, author="octocat", head="feat/x", state="CLEAN", draft=False, age=1.0, checks=()):
    return reconcile.pull_request(
        {
            "number": number,
            "title": f"title {number}",
            "author": {"login": author},
            "headRefName": head,
            "mergeStateStatus": state,
            "isDraft": draft,
        },
        age_hours=age,
        reported_checks=checks,
    )


def failure(conclusion="failure", jobs=("Nightly / ubuntu-24.04",)):
    return reconcile.failure(
        workflow="Nightly full verification",
        conclusion=conclusion,
        run_url="https://github.com/SheathedSharp/option-berth/actions/runs/42",
        failed_jobs=jobs,
    )


class FakeGitHub(reconcile.GitHub):
    """A real client with a recording `gh` instead of a subprocess."""

    def __init__(self, open_issues=(), bodies=None):
        self.commands = []
        self.issues = [{"number": number, "title": title} for number, title in open_issues]
        self.bodies = bodies or {}
        super().__init__("SheathedSharp/option-berth", run=self.record)

    def record(self, argv, allow_failure=False):
        self.commands.append(list(argv))
        if argv[:2] == ["issue", "list"]:
            return json.dumps(self.issues)
        if argv[:2] == ["issue", "view"]:
            return json.dumps({"body": self.bodies.get(int(argv[2]), "")})
        return ""

    def mutations(self):
        return [command for command in self.commands if command[:2] in (["issue", "create"], ["issue", "comment"])]


class DriftTests(unittest.TestCase):
    def test_drift_lists_every_side(self):
        manifest = ("Gate / required checks", "Gate integrity / maintainer-owned CI changes")
        drift = reconcile.gate_drift(manifest, {"Gate / required checks"}, {"Gate / required checks", "Old"})
        self.assertEqual(drift.missing_workflows, ("Gate integrity / maintainer-owned CI changes",))
        self.assertEqual(drift.missing_ruleset, ("Gate integrity / maintainer-owned CI changes",))
        self.assertEqual(drift.extra_ruleset, ("Old",))
        self.assertTrue(drift.drifted)

    def test_agreement_is_not_drift(self):
        drift = reconcile.gate_drift(("A",), {"A"}, {"A"})
        self.assertFalse(drift.drifted)
        self.assertEqual(reconcile.drift_body(drift), "")

    def test_drift_body_names_each_check(self):
        body = reconcile.drift_body(DRIFT)
        for check in DRIFT.missing_workflows + DRIFT.missing_ruleset + DRIFT.extra_ruleset:
            self.assertIn(check, body)
        self.assertIn("workflow job", body)
        self.assertIn("ruleset", body)

    def test_drift_body_skips_empty_sections(self):
        body = reconcile.drift_body(reconcile.Drift((), (), ("Legacy",)))
        self.assertNotIn("缺少对应", body)
        self.assertIn("ruleset 多出", body)
        self.assertIn("Legacy", body)

    def test_drift_issue_is_not_rewritten_when_nothing_changed(self):
        github = FakeGitHub(open_issues=[(9, reconcile.DRIFT_TITLE)], bodies={9: reconcile.drift_body(DRIFT)})
        self.assertEqual(reconcile.apply_drift(github, DRIFT, dry_run=False), "unchanged")
        self.assertEqual(github.mutations(), [])

    def test_changed_drift_comments_on_the_same_issue(self):
        github = FakeGitHub(open_issues=[(9, reconcile.DRIFT_TITLE)], bodies={9: "旧内容"})
        self.assertEqual(reconcile.apply_drift(github, DRIFT, dry_run=False), "changed-comment")
        comment, = github.mutations()
        self.assertEqual(comment[:3], ["issue", "comment", "9"])

    def test_clean_drift_without_an_issue_writes_nothing(self):
        github = FakeGitHub()
        self.assertEqual(reconcile.apply_drift(github, reconcile.Drift((), (), ()), dry_run=False), "clean")
        self.assertEqual(github.mutations(), [])


class FailureTests(unittest.TestCase):
    def test_title_is_exact_and_dedupable(self):
        self.assertEqual(reconcile.failure_title("Nightly full verification"), "[automation] Nightly full verification 失败")

    def test_body_carries_run_facts(self):
        body = reconcile.failure_body(failure(jobs=("Nightly / ubuntu-24.04", "Nightly / macos-15")))
        self.assertIn("Nightly full verification", body)
        self.assertIn("https://github.com/SheathedSharp/option-berth/actions/runs/42", body)
        self.assertIn("`failure`", body)
        self.assertIn("Nightly / ubuntu-24.04", body)
        self.assertIn("Nightly / macos-15", body)

    def test_payload_accepts_workflow_name_alias(self):
        parsed = reconcile.failure_from_payload(
            {
                "workflow_name": "Verified release",
                "conclusion": "timed_out",
                "run_url": "https://example.test/run/1",
                "failed_jobs": ["verify (ubuntu-24.04)"],
            }
        )
        self.assertEqual(parsed.workflow, "Verified release")
        self.assertEqual(parsed.failed_jobs, ("verify (ubuntu-24.04)",))

    def test_payload_rejects_incomplete_facts(self):
        with self.assertRaisesRegex(ValueError, "run_url"):
            reconcile.failure_from_payload({"workflow": "Nightly full verification", "conclusion": "failure"})

    def test_failed_jobs_split_lines_and_commas(self):
        self.assertEqual(
            reconcile.parse_failed_jobs("Nightly / ubuntu-24.04\nNightly / macos-15, Nightly / ubuntu-24.04\n"),
            ("Nightly / ubuntu-24.04", "Nightly / macos-15"),
        )

    def test_success_never_escalates(self):
        self.assertEqual(reconcile.outcome_route("success"), reconcile.PASSED)
        self.assertEqual(reconcile.outcome_route("failure"), reconcile.ESCALATE)
        self.assertEqual(reconcile.outcome_route("timed_out"), reconcile.ESCALATE)
        self.assertEqual(reconcile.outcome_route("cancelled"), reconcile.IGNORED)

    def test_escalation_creates_one_issue_for_a_new_title(self):
        github = FakeGitHub()
        action = reconcile.escalate(github, failure(), dry_run=False)
        self.assertEqual(action, "created")
        create, = github.mutations()
        self.assertEqual(create[:2], ["issue", "create"])
        self.assertIn(reconcile.failure_title("Nightly full verification"), create)

    def test_escalation_comments_instead_of_opening_a_second_issue(self):
        github = FakeGitHub(open_issues=[(7, reconcile.failure_title("Nightly full verification"))])
        action = reconcile.escalate(github, failure(), dry_run=False)
        self.assertEqual(action, "commented")
        comment, = github.mutations()
        self.assertEqual(comment[:3], ["issue", "comment", "7"])
        self.assertIn("https://github.com/SheathedSharp/option-berth/actions/runs/42", comment[-1])

    def test_dedupe_matches_the_exact_title_only(self):
        github = FakeGitHub(open_issues=[(8, "[automation] Nightly full verification 失败（旧）")])
        self.assertEqual(reconcile.escalate(github, failure(), dry_run=False), "created")

    def test_passed_run_comments_and_never_closes(self):
        github = FakeGitHub(open_issues=[(7, reconcile.failure_title("Nightly full verification"))])
        action = reconcile.escalate(github, failure(conclusion="success", jobs=()), dry_run=False)
        self.assertEqual(action, "passed-comment")
        comment, = github.mutations()
        self.assertEqual(comment[:3], ["issue", "comment", "7"])
        self.assertTrue(all("close" not in part for part in comment))

    def test_passed_run_without_issue_writes_nothing(self):
        github = FakeGitHub()
        self.assertEqual(reconcile.escalate(github, failure(conclusion="success", jobs=()), dry_run=False), "passed")
        self.assertEqual(github.mutations(), [])

    def test_cancelled_run_writes_nothing(self):
        github = FakeGitHub(open_issues=[(7, reconcile.failure_title("Nightly full verification"))])
        self.assertEqual(reconcile.escalate(github, failure(conclusion="cancelled"), dry_run=False), "ignored")
        self.assertEqual(github.mutations(), [])

    def test_dry_run_never_writes(self):
        github = FakeGitHub(open_issues=[(7, reconcile.failure_title("Nightly full verification"))])
        self.assertEqual(reconcile.escalate(github, failure(), dry_run=True), "commented")
        self.assertEqual(github.mutations(), [])


class PullRequestTests(unittest.TestCase):
    def test_dependabot_is_recognised_by_author_or_branch(self):
        self.assertTrue(reconcile.is_dependabot(pull(1, author="dependabot[bot]")))
        self.assertTrue(reconcile.is_dependabot(pull(2, head="dependabot/go_modules/engine/x")))
        self.assertFalse(reconcile.is_dependabot(pull(3, author="SheathedSharp", head="feat/x")))

    def test_behind_prs_carry_the_update_branch_command(self):
        behind = reconcile.behind_prs([pull(12, author="dependabot[bot]", state="BEHIND"), pull(13, state="CLEAN")])
        self.assertEqual([entry.number for entry in behind], [12])
        self.assertEqual(behind[0].command, "gh pr update-branch 12")
        self.assertTrue(behind[0].automated)

    def test_human_behind_pr_is_a_recommendation_only(self):
        behind, = reconcile.behind_prs([pull(13, author="SheathedSharp", state="BEHIND")])
        self.assertEqual(behind.command, "gh pr update-branch 13")
        self.assertFalse(behind.automated)
        self.assertIn("建议", reconcile.behind_line(behind))
        self.assertIn("gh pr update-branch 13", reconcile.behind_line(behind))

    def test_stale_checks_wait_for_the_limit(self):
        required = ("Gate / required checks", "Gate integrity / maintainer-owned CI changes")
        reported = ("Gate / required checks",)
        self.assertEqual(reconcile.unreported_checks(required, reported, age_hours=2.0, limit_hours=24.0), ())
        self.assertEqual(
            reconcile.unreported_checks(required, reported, age_hours=25.0, limit_hours=24.0),
            ("Gate integrity / maintainer-owned CI changes",),
        )
        self.assertEqual(reconcile.unreported_checks(required, required, age_hours=99.0, limit_hours=24.0), ())

    def test_check_names_read_both_rollup_kinds(self):
        rollup = [
            {"__typename": "CheckRun", "name": "Gate / required checks"},
            {"__typename": "StatusContext", "context": "ci/legacy"},
            {"__typename": "CheckRun", "name": None},
        ]
        self.assertEqual(reconcile.check_names(rollup), ("Gate / required checks", "ci/legacy"))

    def test_stale_prs_skip_drafts_and_report_missing_checks(self):
        prs = [
            pull(1, age=30.0, checks=("Gate / required checks",)),
            pull(2, age=30.0, draft=True, checks=()),
            pull(3, age=30.0, checks=None),
        ]
        stale = reconcile.stale_prs(prs, ("Gate / required checks", "Gate integrity / maintainer-owned CI changes"), 24.0)
        self.assertEqual([entry.number for entry in stale], [1])
        self.assertEqual(stale[0].missing, ("Gate integrity / maintainer-owned CI changes",))


class SummaryTests(unittest.TestCase):
    def test_summary_is_stable_json(self):
        summary = reconcile.build_summary(drift=DRIFT, behind=(), stale=(), open_prs=0)
        line = reconcile.format_summary(summary)
        self.assertTrue(line.startswith("reconcile-summary: "))
        decoded = json.loads(line[len("reconcile-summary: "):])
        self.assertEqual(decoded, summary)
        self.assertEqual(list(summary), sorted(summary))

    def test_summary_of_a_clean_repository_has_every_key(self):
        summary = reconcile.build_summary(drift=reconcile.Drift((), (), ()), behind=(), stale=(), open_prs=2)
        self.assertEqual(summary["drift"], {"extra_ruleset": [], "missing_ruleset": [], "missing_workflows": []})
        self.assertEqual(summary["behind"], [])
        self.assertEqual(summary["open_prs"], 2)


if __name__ == "__main__":
    unittest.main()
