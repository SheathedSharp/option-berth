"""A loopback-only demo API. No dependencies, file serving, or external calls."""
import http.server
import json
import os
import socketserver
from urllib.parse import urlsplit


class Server(http.server.ThreadingHTTPServer):
    def server_bind(self):
        # Avoid HTTPServer's reverse DNS lookup; the address is explicitly local.
        socketserver.TCPServer.server_bind(self)
        self.server_name = "localhost"
        self.server_port = self.server_address[1]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = urlsplit(self.path).path
        code = 200 if path in ("/", "/health") else 404
        body = json.dumps({"service": "api", "ok": code == 200,
                           "port": self.server.server_port}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


if __name__ == "__main__":
    port = int(os.environ["BERTH_PORT"])
    if not 1 <= port <= 65535:
        raise SystemExit("BERTH_PORT must be between 1 and 65535; start with oberth up")
    with Server(("127.0.0.1", port), Handler) as server:
        print(f"hello-worktree API listening on http://127.0.0.1:{port}", flush=True)
        server.serve_forever()
