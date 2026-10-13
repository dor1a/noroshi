"""Receives webhook posts for local testing and lists them at GET /.

A path ending in /fail answers 400, to try how failed deliveries look.
"""
import json
from collections import deque
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

received = deque(maxlen=50)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            body = json.loads(raw)
        except ValueError:
            body = raw.decode("utf-8", "replace")
        entry = {
            "at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "path": self.path,
            "body": body,
        }
        received.appendleft(entry)
        print(json.dumps(entry, ensure_ascii=False), flush=True)
        if self.path.endswith("/fail"):
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'{"message": "rejected by webhook-sink"}')
            return
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        out = json.dumps(list(received), ensure_ascii=False, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("", 8000), Handler).serve_forever()
