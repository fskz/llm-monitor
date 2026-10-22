#!/usr/bin/env python3
"""Mock OpenAI-compatible SSE endpoint for smoke tests (200ms text latency).

--cache-mode {hit,zero,strip} (default: no usage details) simulates the three
endpoint behaviours the cache check distinguishes (task 10-22-cache-check):
  hit   prompt_tokens_details.cached_tokens = 1500 on every request
  zero  prompt_tokens_details.cached_tokens = 0 on every request
  strip usage never carries prompt_tokens_details (relay stripped it)
"""
import json, sys, time
from http.server import BaseHTTPRequestHandler, HTTPServer

CACHE_MODE = "none"

def usage_payload():
    u = {"prompt_tokens": 48, "completion_tokens": 24}
    if CACHE_MODE == "hit":
        u["prompt_tokens_details"] = {"cached_tokens": 1500}
    elif CACHE_MODE == "zero":
        u["prompt_tokens_details"] = {"cached_tokens": 0}
    return u

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(n))
        auth = self.headers.get("authorization", "")
        sys.stderr.write(f"[mock] model={body.get('model')} auth={'yes' if auth else 'no'} stream={body.get('stream')} cache-mode={CACHE_MODE}\n")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        def ev(obj):
            self.wfile.write(f"data: {json.dumps(obj, ensure_ascii=False)}\n\n".encode())
        ev({"choices": [{"delta": {"role": "assistant"}}]})
        time.sleep(0.2)
        ev({"choices": [{"delta": {"content": "pong，探测成功"}}]})
        time.sleep(0.4)  # decode span so smoke tests can observe decode_tps
        ev({"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage_payload()})
        self.wfile.write(b"data: [DONE]\n\n")
    def log_message(self, *a):
        pass

if __name__ == "__main__":
    argv = sys.argv[1:]
    if "--cache-mode" in argv:
        CACHE_MODE = argv[argv.index("--cache-mode") + 1]
    if CACHE_MODE not in ("none", "hit", "zero", "strip"):
        sys.exit(f"unknown --cache-mode {CACHE_MODE!r} (hit|zero|strip)")
    HTTPServer(("127.0.0.1", 18999), H).serve_forever()
