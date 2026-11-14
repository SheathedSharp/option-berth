"""Naming contracts only: reads source; never starts a product process."""
import pathlib
import re
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]

class RuntimeNamingContract(unittest.TestCase):
    def test_runtime_domain_and_service_command_tree(self):
        cmd = ROOT / "engine/internal/cmd"
        self.assertFalse((cmd / "daemon.go").exists())
        source = (cmd / "runtime_service.go").read_text()
        self.assertRegex(source, r'Use:\s*"runtime"')
        self.assertRegex(source, r'Use:\s*"service"')
        self.assertIn("runtimeCmd.AddCommand(runtimeServiceCmd)", source)
        self.assertNotIn("rootCmd.AddCommand(runtimeServiceCmd)", source)
        run = (cmd / "runtime_service_run.go").read_text()
        self.assertRegex(run, r'Use:\s*"run"')
        self.assertIn("runtimeServiceCmd.AddCommand(runtimeServiceRunCmd)", run)
        self.assertNotIn('exec.Command(binary, "serve")', run)

    def test_new_endpoint_and_configuration_contract(self):
        paths = (ROOT / "engine/internal/paths/paths.go").read_text()
        for name in ["runtime-service.sock", "runtime-service.log", "runtime-service.lock", "BERTH_RUNTIME_SERVICE_SOCKET"]:
            self.assertIn('"' + name + '"', paths)
        cfg = (ROOT / "engine/internal/config/config.go").read_text()
        self.assertIn('yaml:"runtime_service"', cfg)
        self.assertNotIn('yaml:"daemon"', cfg)

    def test_new_wire_major_and_no_old_method_registration(self):
        rpc = ROOT / "engine/internal/runtimesvc/rpc"
        self.assertIn('const ProtocolVersion = "3.0.0"', (rpc / "types.go").read_text())
        for source in rpc.glob("*.go"):
            if source.name.endswith("_test.go"):
                continue
            text = source.read_text()
            self.assertNotRegex(text, r'"daemon\.(hello|status|shutdown|schema|doctor)"', str(source))
        self.assertEqual((ROOT / "VERSION").read_text().strip(), "3.0.0")

    def test_doctor_public_identifiers_are_runtime_service(self):
        source = (ROOT / "engine/internal/doctor/doctor.go").read_text()
        for check in ["reachable", "version_matches", "build_matches", "protocol"]:
            self.assertIn('"runtime_service_' + check + '"', source)
            self.assertNotIn('"daemon_' + check + '"', source)

if __name__ == "__main__":
    unittest.main()
