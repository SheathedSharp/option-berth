"""Check the skill-contract extractor against the current contract documents."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "berth_skill_contract", Path(__file__).with_name("check_skill_contract.py")
)
contract = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(contract)

CLI = (ROOT / "docs" / "cli.md").read_text(encoding="utf-8")
SCHEMA = json.loads((ROOT / "engine" / "docs" / "schema" / "manifest.schema.json").read_text())
OBSERVATION = (ROOT / "engine" / "internal" / "state" / "observation.go").read_text()


class ExtractionTests(unittest.TestCase):
    def test_stable_error_codes_include_every_observation_code(self):
        codes = contract.stable_error_codes(CLI)
        for code in (
            "observation_empty",
            "observation_unrecognized",
            "observation_incomplete",
            "observation_blind",
            "observation_bind_denied",
        ):
            self.assertIn(code, codes)
        self.assertIn("invalid_params", codes)
        self.assertIn("internal", codes)

    def test_reference_tokens_match_the_contract_table(self):
        self.assertEqual(
            set(contract.reference_tokens(CLI)),
            {"${port}", "${url}", "${<name>.port}", "${<name>.url}", "${worktree}"},
        )

    def test_json_field_anchors_match_the_agent_field_table(self):
        self.assertEqual(
            set(contract.json_field_anchors(CLI)),
            {
                "last_exit.reason",
                "last_exit.port_hint",
                "last_exit.port_holder",
                "exits[].hint",
                "services[].ready",
                "services[].port_holder",
                "health_status",
                "manifest_runtime_mismatch",
                "worktree.identity",
                "observation.state",
                "attention_path",
            },
        )

    def test_manifest_anchors_include_the_agent_facing_fields(self):
        keys = set(contract.manifest_anchors(SCHEMA))
        for key in (
            "prepare_inputs",
            "health",
            "http",
            "tcp",
            "command",
            "machine",
            "services",
            "worktree_ports",
            "workspace",
            "env_file",
        ):
            self.assertIn(key, keys)

    def test_observation_states_come_from_the_constants(self):
        self.assertEqual(
            set(contract.observation_states(OBSERVATION)),
            {"observable", "partial", "blind", "bind_denied"},
        )

    def test_qualitative_anchors_still_exist_in_the_contract(self):
        contract.validate_qualitative_sources(CLI)


class CoverageTests(unittest.TestCase):
    def test_missing_anchors_are_each_reported(self):
        anchors = [("error code", "observation_blind"), ("manifest key", "prepare_inputs")]
        self.assertEqual(contract.missing_anchors(anchors, "status only"), anchors)

    def test_present_anchors_pass(self):
        anchors = [("error code", "observation_blind"), ("reference", "${worktree}")]
        text = "observation_blind and ${worktree}"
        self.assertEqual(contract.missing_anchors(anchors, text), [])

    def test_manifest_key_needs_field_context(self):
        anchors = [("manifest key", "workspace")]
        self.assertEqual(
            contract.missing_anchors(anchors, "subscribe with --workspace NAME"),
            anchors,
        )
        self.assertEqual(contract.missing_anchors(anchors, "workspace: []"), [])
        self.assertEqual(contract.missing_anchors(anchors, "`workspace`"), [])

    def test_json_field_needs_the_exact_path(self):
        anchors = [("json field", "last_exit.port_holder")]
        self.assertEqual(
            contract.missing_anchors(anchors, "read `port_holder` instead"),
            anchors,
        )
        self.assertEqual(
            contract.missing_anchors(anchors, "read `last_exit.port_holder`"),
            [],
        )

    def test_read_skill_requires_skill_and_references(self):
        with tempfile.TemporaryDirectory(prefix="berth-skill-") as temporary:
            root = Path(temporary)
            with self.assertRaises(ValueError):
                contract.read_skill(root)
            (root / "SKILL.md").write_text("# skill")
            with self.assertRaises(ValueError):
                contract.read_skill(root)
            (root / "references").mkdir()
            (root / "references" / "operations.md").write_text("# operations")
            text, files = contract.read_skill(root)
            self.assertIn("# operations", text)
            self.assertEqual(len(files), 2)


class CurrentContractTests(unittest.TestCase):
    def test_contract_anchors_are_extracted_from_the_repository(self):
        anchors = contract.build_anchors(CLI, SCHEMA, OBSERVATION)
        self.assertGreater(len(anchors), 20)
        self.assertIn(("reference", "${worktree}"), anchors)
        self.assertIn(("error code", "observation_bind_denied"), anchors)
        self.assertIn(("manifest key", "prepare_inputs"), anchors)
        self.assertIn(("json field", "last_exit.port_holder"), anchors)


if __name__ == "__main__":
    unittest.main()
