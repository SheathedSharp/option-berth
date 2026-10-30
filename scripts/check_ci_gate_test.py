"""Check the required-check manifest against workflow jobs and rulesets."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("berth_ci_gate", Path(__file__).with_name("check_ci_gate.py"))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class JobNameTests(unittest.TestCase):
    def test_matrix_os_expands(self):
        text = """
jobs:
  verify:
    name: Engine / ${{ matrix.os }}
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-24.04, macos-15]
"""
        self.assertEqual(gate.job_names(text), {"Engine / ubuntu-24.04", "Engine / macos-15"})

    def test_explicit_job_name(self):
        text = """
jobs:
  native:
    name: Windows x64 native build and smoke
    runs-on: windows-2025
"""
        self.assertEqual(gate.job_names(text), {"Windows x64 native build and smoke"})


class ManifestTests(unittest.TestCase):
    def test_manifest_rejects_duplicates_and_empty_lists(self):
        with tempfile.TemporaryDirectory(prefix="berth-gate-") as temporary:
            root = Path(temporary)
            for payload, message in (({"required_checks": ["A", "A"]}, "duplicate"),
                                     ({"required_checks": []}, "non-empty")):
                path = root / "checks.json"
                path.write_text(json.dumps(payload))
                with self.assertRaisesRegex(ValueError, message):
                    gate.load_manifest(path)


class CompareTests(unittest.TestCase):
    def test_missing_workflow_job_fails(self):
        with self.assertRaisesRegex(ValueError, "no workflow job"):
            gate.compare(("A", "B"), {"A"})

    def test_ruleset_mismatch_fails_both_ways(self):
        with self.assertRaisesRegex(ValueError, "missing required checks"):
            gate.compare(("A", "B"), {"A", "B"}, {"A"})
        with self.assertRaisesRegex(ValueError, "checks missing from the manifest"):
            gate.compare(("A",), {"A"}, {"A", "B"})

    def test_required_contexts_only_reads_active_default_branch_rules(self):
        ruleset = {
            "enforcement": "active",
            "target": "branch",
            "conditions": {"ref_name": {"include": ["~DEFAULT_BRANCH"]}},
            "rules": [{"type": "required_status_checks",
                       "parameters": {"required_status_checks": [{"context": "A"}]}}],
        }
        inactive = dict(ruleset, enforcement="disabled")
        self.assertEqual(gate.required_contexts([ruleset, inactive]), {"A"})


if __name__ == "__main__":
    unittest.main()
