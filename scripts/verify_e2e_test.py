"""Measurement/protocol checks plus bounded loopback-fixture construction."""
import importlib.util
from pathlib import Path
import unittest
from unittest import mock
import socket

spec = importlib.util.spec_from_file_location("verify_e2e", Path(__file__).with_name("verify-e2e.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class MeasurementTests(unittest.TestCase):
    def test_http_fixture_listens_without_reverse_dns(self):
        spec = importlib.util.spec_from_file_location("fixture", Path(__file__).with_name("p4_http_fixture.py"))
        fixture = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(fixture)
        with mock.patch("socket.getfqdn", side_effect=AssertionError("fixture consulted reverse DNS")):
            with fixture.Server(("127.0.0.1", 0), fixture.Handler) as server:
                self.assertEqual(server.server_name, "localhost")
                self.assertGreater(server.server_port, 0)
                # Accepting a real loopback connection is portable; macOS does
                # not implement the SO_ACCEPTCONN query exposed by Python.
                server.socket.settimeout(1)
                with socket.create_connection(server.server_address, timeout=1):
                    accepted, _ = server.get_request()
                    accepted.close()

    def test_quantiles_are_per_request_nearest_rank(self):
        values = list(range(300, 0, -1))
        result = verify.distribution(values)
        self.assertEqual((result["n"], result["p50"], result["p95"], result["max"]), (300, 150, 285, 300))
        self.assertEqual(result["raw"], values)
        self.assertEqual(values[0], 300)
        with self.assertRaises(ValueError):
            verify.distribution([])

    def test_stats_workload_requires_observed_stats(self):
        stats = {"memory_rss_bytes": 4096, "cpu_percent": 1}
        snapshot = {"ports": [{"port": 4000, "stats": stats}]}
        self.assertEqual(verify.visible_stats(snapshot, [4000]), {4000: stats})
        for invalid in (None, {}, {"memory_rss_bytes": 0}, {"memory_rss_bytes": True}):
            with self.subTest(invalid=invalid), self.assertRaises(RuntimeError):
                verify.visible_stats({"ports": [{"port": 4000, "stats": invalid}]}, [4000])
        with self.assertRaises(RuntimeError):
            verify.visible_stats(snapshot, [4001])

    def test_cpu_time_formats(self):
        for text, expected in (("00:01.25", 1.25), ("01:02:03", 3723), ("2-01:02:03.50", 176523.5)):
            with self.subTest(text=text):
                self.assertEqual(verify.cpu_seconds(text), expected)

    def test_delta_reconstructs_socket_rows_not_process_keys(self):
        a = {"host": "localhost", "port": 8000, "bind_address": "127.0.0.1", "pid": 1}
        b = dict(a, pid=2)
        remote = dict(a, host="other")
        rows = {verify.port_key(a): a, verify.port_key(remote): remote}
        verify.apply_ports(rows, {"ports": {"removed": ["8000:127.0.0.1"], "added": [b]}})
        self.assertEqual(rows["8000:127.0.0.1"]["pid"], 2)
        self.assertEqual(rows["other/8000:127.0.0.1"]["pid"], 1)
        verify.apply_ports(rows, {"ports": {"removed": ["other/8000:127.0.0.1"], "updated": [dict(b, pid=3)]}})
        self.assertEqual(list(rows), ["8000:127.0.0.1"])
        self.assertEqual(rows["8000:127.0.0.1"]["pid"], 3)


if __name__ == "__main__":
    unittest.main()
