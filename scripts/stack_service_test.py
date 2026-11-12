"""The stack service's container and database evidence must stay observable."""
import importlib.util
import os
import socket
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "examples/stack-workshop/service.py"
SCHEMA = ROOT / "examples/stack-workshop/schema.sql"


def load_service(**env: str):
    defaults = {"SERVICE_NAME": "api", "PORT": "1", "DB_PATH": "", "REQUIRE": "", "TCP_REQUIRE": ""}
    with patch.dict(os.environ, {**defaults, **env}, clear=False):
        spec = importlib.util.spec_from_file_location("stack_service", SOURCE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    return module


class StackServiceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.db = Path(self.tmp.name) / "app.db"
        connection = sqlite3.connect(self.db)
        try:
            connection.executescript(SCHEMA.read_text(encoding="utf-8"))
            connection.execute("INSERT INTO notes (body) VALUES ('seed')")
            connection.commit()
        finally:
            connection.close()

    def test_tcp_target_accepts_url_and_host_port(self):
        service = load_service()
        self.assertEqual(service.tcp_target("http://127.0.0.1:5432"), ("127.0.0.1", 5432))
        self.assertEqual(service.tcp_target("127.0.0.1:5432"), ("127.0.0.1", 5432))

    def test_health_reports_this_worktrees_database(self):
        service = load_service(DB_PATH=str(self.db))
        facts = service.db_facts()
        self.assertEqual(facts["db"], str(self.db.resolve()))
        self.assertEqual(facts["migrations"], 1)
        self.assertEqual(facts["notes"], 1)

    def test_unreachable_tcp_dependency_is_not_healthy(self):
        with socket.socket() as closed:
            closed.bind(("127.0.0.1", 0))
            port = closed.getsockname()[1]
        service = load_service(TCP_REQUIRE=f"127.0.0.1:{port}")
        self.assertEqual(service.waiting_on(), [f"127.0.0.1:{port}"])

    def test_reachable_tcp_dependency_is_healthy(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen(1)
            port = listener.getsockname()[1]
            service = load_service(TCP_REQUIRE=f"127.0.0.1:{port}")
            self.assertEqual(service.waiting_on(), [])


if __name__ == "__main__":
    unittest.main()
