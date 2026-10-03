"""A bounded transport fixture proves the pressure harness keeps consuming."""
import importlib.util
import json
from collections import deque
from pathlib import Path
import socket
import threading
import unittest

spec = importlib.util.spec_from_file_location("verify_e2e_drain", Path(__file__).with_name("verify-e2e.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class StatsDrainTests(unittest.TestCase):
    def connection(self):
        client, server = socket.socketpair()
        server.settimeout(5)
        server.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 4096)
        rpc = verify.RPC.__new__(verify.RPC)
        rpc.socket, rpc.file, rpc.pending, rpc.number = client, client.makefile("rb"), deque(), 2
        return rpc, server

    def test_consumes_beyond_transport_buffer_and_proves_same_connection(self):
        rpc, server = self.connection()
        drain = verify.StatsDrain(rpc, {"/fixture"})
        failures = []

        def produce():
            try:
                for seq in range(1, 3001):
                    server.sendall((json.dumps({"jsonrpc": "2.0", "method": "state.delta", "params": {
                        "seq": seq, "ports": {"updated": [{"project_root": "/fixture", "stats": {
                            "memory_rss_bytes": seq}}]}}}) + "\n").encode())
                with server.makefile("rb") as reader:
                    request = json.loads(reader.readline())
                    if request["method"] != "daemon.status":
                        raise AssertionError(request)
                    server.sendall((json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": {
                        "subscribers": 1}}) + "\n").encode())
            except BaseException as exc:
                failures.append(exc)

        writer = threading.Thread(target=produce, daemon=True)
        writer.start()
        try:
            result = drain.finish()
            self.assertTrue(result["same_connection_barrier"])
            self.assertEqual(result["frames_consumed"], 3000)
            self.assertEqual(result["fixture_stats_updates"], 3000)
            self.assertEqual(result["observed_roots"], ["/fixture"])
            writer.join(timeout=5)
            self.assertFalse(writer.is_alive())
            self.assertEqual(failures, [])
        finally:
            drain.close()
            server.close()
            writer.join(timeout=5)

    def test_closed_subscription_cannot_pass_barrier(self):
        rpc, server = self.connection()
        drain = verify.StatsDrain(rpc, {"/fixture"})
        server.close()
        try:
            with self.assertRaises((OSError, RuntimeError)):
                drain.finish()
        finally:
            drain.close()

    def test_close_unparks_a_quiet_reader(self):
        rpc, server = self.connection()
        drain = verify.StatsDrain(rpc, set())
        try:
            drain.close()
            self.assertFalse(drain.thread.is_alive())
        finally:
            server.close()


if __name__ == "__main__":
    unittest.main()
