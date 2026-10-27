#!/usr/bin/env python3
"""Minimal llama-server stand-in for maio integration and soak tests.

It implements only the endpoints maio uses: /health, /completion,
/v1/chat/completions, /v1/embeddings and /metrics. The reply is deterministic
and the process never touches a model file, so the whole data path can be
tested without a GPU.
"""
import argparse
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DELAY_MS = int(os.environ.get("MOCK_DELAY_MS", "0"))
CHUNKS = int(os.environ.get("MOCK_CHUNKS", "4"))


def parse_args():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=9931)
    p.add_argument("--version", action="store_true")
    # Ignore the rest of the llama-server flags maio passes.
    args, _ = p.parse_known_args()
    return args


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        if os.environ.get("MOCK_QUIET") != "1":
            sys.stderr.write("mock-llama: " + (fmt % args) + "\n")

    def _read_json(self):
        n = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(n) if n else b"{}"
        try:
            return json.loads(raw or b"{}")
        except json.JSONDecodeError:
            return {}

    def _json(self, code, obj, content_type="application/json"):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _sse_start(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

    def _sse(self, obj, raw=False):
        payload = obj if raw else "data: " + json.dumps(obj)
        self.wfile.write((payload + "\n\n").encode())
        self.wfile.flush()

    def do_GET(self):
        if self.path in ("/health", "/healthz"):
            self._json(200, {"status": "ok"})
        elif self.path == "/metrics":
            body = b"# mock engine metrics\nllama_up 1\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/slots":
            self._json(200, [{"id": 0, "state": "idle"}])
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        req = self._read_json()
        if DELAY_MS:
            time.sleep(DELAY_MS / 1000.0)
        if self.path == "/completion":
            self._completion(req)
        elif self.path == "/v1/chat/completions":
            self._chat(req)
        elif self.path == "/v1/embeddings":
            self._embeddings(req)
        elif self.path == "/tokenize":
            self._json(200, {"tokens": [1, 2, 3]})
        else:
            self._json(404, {"error": "not found"})

    def _completion(self, req):
        stream = bool(req.get("stream"))
        words = ["mock ", "reply ", "from ", "maio"]
        if stream:
            self._sse_start()
            for w in words[:CHUNKS]:
                self._sse({"content": w, "stop": False})
            self._sse({"content": "", "stop": True, "timings": {
                "prompt_n": 3, "prompt_ms": 1.0, "predicted_n": len(words[:CHUNKS]), "predicted_ms": 2.0}})
        else:
            self._json(200, {"content": "".join(words), "stop": True,
                             "timings": {"prompt_n": 3, "prompt_ms": 1.0,
                                         "predicted_n": len(words), "predicted_ms": 2.0}})

    def _chat(self, req):
        stream = bool(req.get("stream"))
        text = "mock chat reply"
        if stream:
            self._sse_start()
            for part in ("mock ", "chat ", "reply"):
                self._sse({"choices": [{"delta": {"content": part}, "finish_reason": None}]})
            self._sse({"choices": [{"delta": {}, "finish_reason": "stop"}],
                       "usage": {"prompt_tokens": 5, "completion_tokens": 3}})
            self._sse("[DONE]", raw=True)
        else:
            self._json(200, {
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": req.get("model", "mock"),
                "choices": [{"index": 0, "message": {"role": "assistant", "content": text},
                             "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
            })

    def _embeddings(self, req):
        inp = req.get("input", "")
        n = len(inp) if isinstance(inp, list) else 1
        data = [{"object": "embedding", "index": i, "embedding": [0.1, 0.2, 0.3]} for i in range(n)]
        self._json(200, {"object": "list", "data": data, "model": req.get("model", "mock"),
                         "usage": {"prompt_tokens": 1, "total_tokens": 1}})


def main():
    args = parse_args()
    if args.version:
        print("version: mock-llama (maio test engine)")
        return 0
    srv = ThreadingHTTPServer((args.host, args.port), Handler)
    srv.daemon_threads = True
    srv.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
