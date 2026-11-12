"""Map changed paths to the CI areas that must run for them."""
import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("berth_changed_areas", Path(__file__).with_name("changed_areas.py"))
areas = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(areas)


def needed(result):
    return {name for name, required in result.items() if required}


class AreasTests(unittest.TestCase):
    def test_docs_only_needs_no_area(self):
        self.assertEqual(needed(areas.areas_for(["docs/product.md", "README.md"])), set())

    def test_cli_contract_docs_still_run_engine(self):
        self.assertEqual(needed(areas.areas_for(["docs/cli.zh.md"])), {"engine"})

    def test_engine_change_needs_every_engine_area(self):
        self.assertEqual(
            needed(areas.areas_for(["engine/internal/ports/listen.go"])),
            {"engine", "windows", "examples", "readiness"},
        )

    def test_git_change_needs_the_git_area_only_on_top_of_engine(self):
        self.assertEqual(
            needed(areas.areas_for(["engine/internal/git/worktree.go"])),
            {"engine", "windows", "git", "examples", "readiness"},
        )

    def test_examples_change_only_runs_the_example_smoke(self):
        self.assertEqual(needed(areas.areas_for(["examples/hello-worktree/README.md"])), {"examples"})

    def test_readiness_script_change_runs_engine_and_readiness(self):
        self.assertEqual(needed(areas.areas_for(["scripts/verify-reused-readiness.py"])), {"engine", "readiness"})

    def test_ci_change_runs_every_area(self):
        self.assertEqual(needed(areas.areas_for([".github/workflows/ci.yml"])), set(areas.AREAS))

    def test_file_rules_match_exactly_not_by_prefix(self):
        self.assertEqual(needed(areas.areas_for(["install.sh.bak"])), set())

    def test_output_names_match_the_area_names(self):
        result = areas.areas_for([])
        self.assertEqual(set(result), set(areas.AREAS))


if __name__ == "__main__":
    unittest.main()
