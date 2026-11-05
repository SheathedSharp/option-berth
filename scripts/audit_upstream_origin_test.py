"""Check the upstream audit counts copied runs and only clears an rewritten tree."""
import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "audit_upstream_origin", Path(__file__).with_name("audit-upstream-origin.py"))
audit = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(audit)

COPIED = """package ports

func Collect(rows []Row) []Row {
\tkept := make([]Row, 0, len(rows))
\tfor _, row := range rows {
\t\tif row.Port == 0 {
\t\t\tcontinue
\t\t}
\t\tkept = append(kept, row)
\t}
\treturn kept
}
"""

REWRITTEN = """package ports

// Collect drops rows without a port and preserves the caller's order.
func Collect(rows []Row) []Row {
\tswitch len(rows) {
\tcase 0:
\t\treturn nil
\t}
\tout := rows[:0]
\tfor i := range rows {
\t\tif rows[i].Port != 0 {
\t\t\tout = append(out, rows[i])
\t\t}
\t}
\treturn out
}
"""

SHARED_HEADER = """package ports

import "sort"
"""


class AuditTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.engine = root / "engine"
        self.upstream = root / "sonar"
        (self.engine / "internal/ports").mkdir(parents=True)
        (self.upstream / "internal/ports").mkdir(parents=True)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, side, name, text):
        (side / "internal/ports" / name).write_text(text)

    def test_identical_file_is_reported_and_fails_the_gate(self):
        self.write(self.upstream, "collect.go", COPIED)
        self.write(self.engine, "collect.go", COPIED)
        result = audit.audit(self.engine, self.upstream)
        self.assertEqual([item["path"] for item in result["files"]], ["internal/ports/collect.go"])
        self.assertGreater(result["lines"], 0)
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            status = audit.main(["--engine", str(self.engine), "--upstream", str(self.upstream)])
        self.assertEqual(status, 1)
        self.assertIn("collect.go", out.getvalue())

    def test_rewritten_file_with_only_shared_boilerplate_is_clean(self):
        self.write(self.upstream, "collect.go", SHARED_HEADER + COPIED)
        self.write(self.engine, "collect.go", SHARED_HEADER + REWRITTEN)
        result = audit.audit(self.engine, self.upstream)
        self.assertEqual(result["files"], [])
        with contextlib.redirect_stdout(io.StringIO()):
            status = audit.main(["--engine", str(self.engine), "--upstream", str(self.upstream)])
        self.assertEqual(status, 0)

    def test_only_same_path_go_files_are_compared(self):
        self.write(self.upstream, "collect.go", COPIED)
        self.write(self.engine, "unrelated.go", COPIED)
        (self.upstream / "internal/ports/notes.md").write_text(COPIED)
        (self.engine / "internal/ports/notes.md").write_text(COPIED)
        result = audit.audit(self.engine, self.upstream)
        self.assertEqual(result["files"], [])

    def test_shared_import_list_is_not_evidence(self):
        imports = """import (
\t"fmt"
\t"os"
\t"sort"
\t"strings"
\t"time"
)
"""
        body = "\nfunc Mine() {\n\tprintln(1)\n\tprintln(2)\n\tprintln(3)\n\tprintln(4)\n\tprintln(5)\n}\n"
        self.write(self.upstream, "collect.go", imports + body)
        self.write(self.engine, "collect.go", imports + body)
        # The five shared import lines do not count; the six-line function body does.
        result = audit.audit(self.engine, self.upstream)
        self.assertEqual(result["lines"], 6)

    def test_min_run_sets_the_evidence_threshold(self):
        self.write(self.upstream, "collect.go", COPIED)
        self.write(self.engine, "collect.go", SHARED_HEADER + REWRITTEN)
        self.assertEqual(audit.audit(self.engine, self.upstream, min_run=3)["files"], [])
        # The shared function signature is one line: below the default threshold, counted at 1.
        self.assertEqual(audit.audit(self.engine, self.upstream, min_run=1)["lines"], 1)

    def test_missing_trees_are_named_in_the_error(self):
        with self.assertRaises(SystemExit) as caught:
            audit.audit(self.engine / "absent", self.upstream)
        self.assertIn("no engine tree", str(caught.exception))

    def test_json_report_is_machine_readable(self):
        self.write(self.upstream, "collect.go", COPIED)
        self.write(self.engine, "collect.go", COPIED)
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            audit.main(["--engine", str(self.engine), "--upstream", str(self.upstream), "--json"])
        self.assertIn('"path": "internal/ports/collect.go"', out.getvalue())


if __name__ == "__main__":
    unittest.main()
