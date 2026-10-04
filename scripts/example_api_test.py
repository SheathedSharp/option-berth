"""The public example must not accidentally become a directory/file server."""
import importlib.util
import json
from pathlib import Path
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import build_opener, ProxyHandler

SOURCE = Path(__file__).resolve().parent.parent / "examples/hello-worktree/api.py"
spec = importlib.util.spec_from_file_location("example_api", SOURCE)
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class ExampleAPITests(unittest.TestCase):
    def setUp(self):
        self.server = api.Server(("127.0.0.1", 0), api.Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.close)
        self.url = f"http://127.0.0.1:{self.server.server_port}"
        self.http = build_opener(ProxyHandler({}))

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        self.assertFalse(self.thread.is_alive())

    def test_health(self):
        with self.http.open(self.url + "/health", timeout=3) as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(json.load(response), {"service": "api", "ok": True, "port": self.server.server_port})

    def test_root_is_json_not_file_listing(self):
        with self.http.open(self.url, timeout=3) as response:
            self.assertEqual(response.headers.get_content_type(), "application/json")
            self.assertTrue(json.load(response)["ok"])

    def test_private_path_is_not_served(self):
        with self.assertRaises(HTTPError) as caught:
            self.http.open(self.url + "/.env", timeout=3)
        with caught.exception as response:
            self.assertEqual(response.code, 404)
            self.assertFalse(json.load(response)["ok"])
