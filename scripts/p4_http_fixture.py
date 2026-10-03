"""Loopback-only HTTP fixture; startup must not depend on reverse DNS."""
import http.server
import os
import socketserver


class Server(http.server.ThreadingHTTPServer):
    def server_bind(self):
        # HTTPServer otherwise calls socket.getfqdn between bind and listen.
        # The fixture has a fixed loopback identity, so no DNS fact is needed.
        socketserver.TCPServer.server_bind(self)
        self.server_name = "localhost"
        self.server_port = self.server_address[1]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    with Server(("127.0.0.1", int(os.environ["BERTH_PORT"])), Handler) as server:
        server.serve_forever()
