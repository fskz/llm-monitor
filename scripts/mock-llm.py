#!/usr/bin/env python3
"""Mock OpenAI-compatible SSE endpoint for smoke tests (200ms text latency)."""
import json, sys, time
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(n))
        auth = self.headers.get("authorization", "")
        sys.stderr.write(f"[mock] model={body.get('model')} auth={'yes' if auth else 'no'} stream={body.get('stream')}\n")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        def ev(obj):
            self.wfile.write(f"data: {json.dumps(obj, ensure_ascii=False)}\n\n".encode())
        ev({"choices": [{"delta": {"role": "assistant"}}]})
        time.sleep(0.2)
        ev({"choices": [{"delta": {"content": "pong，探测成功"}}]})
        ev({"choices": [{"delta": {}, "finish_reason": "stop"}]})
        self.wfile.write(b"data: [DONE]\n\n")
    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", 18999), H).serve_forever()
