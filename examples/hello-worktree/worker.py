"""A portless worker showing an explicitly injected inter-service URL."""
import os
import time
from urllib.error import URLError
from urllib.parse import urlsplit
from urllib.request import build_opener, ProxyHandler


if __name__ == "__main__":
    url = os.environ["API_URL"]
    parts = urlsplit(url)
    if parts.scheme != "http" or parts.hostname not in ("localhost", "127.0.0.1"):
        raise SystemExit("this demo only contacts a loopback API")
    # A local demo should not forward local traffic through an inherited proxy.
    http = build_opener(ProxyHandler({}))
    while True:
        try:
            with http.open(url + "/health", timeout=2) as response:
                print(f"worker: API health={response.status}", flush=True)
        except (URLError, OSError) as error:
            print(f"worker: API unavailable ({type(error).__name__})", flush=True)
        time.sleep(3)
