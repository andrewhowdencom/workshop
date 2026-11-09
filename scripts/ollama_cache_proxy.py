#!/usr/bin/env python3
"""Inspect Workshop's Ollama request prefixes without saving prompt text.

This is a diagnostic proxy. It buffers each upstream response before returning
it to Workshop, so use it for cache experiments rather than normal sessions.
"""

import argparse
import hashlib
import json
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def fingerprint(value):
    encoded = json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()
    return hashlib.sha256(encoded).hexdigest()[:16]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen", default="127.0.0.1:11436")
    parser.add_argument("--upstream", default="http://127.0.0.1:11434")
    parser.add_argument("--output", required=True, type=Path, help="JSONL summary path")
    args = parser.parse_args()
    host, port = args.listen.rsplit(":", 1)
    state = {"count": 0, "previous_messages": []}
    lock = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            try:
                request_body = json.loads(body)
            except ValueError:
                self.send_error(400, "request body is not JSON")
                return

            messages = request_body.get("messages") or []
            tools = request_body.get("tools") or []
            with lock:
                state["count"] += 1
                call = state["count"]
                previous = state["previous_messages"]
                state["previous_messages"] = messages
            matching = 0
            for earlier, current in zip(previous, messages):
                if earlier != current:
                    break
                matching += 1

            system_messages = [m.get("content") for m in messages if m.get("role") == "system"]
            summary = {
                "call": call,
                "model": request_body.get("model"),
                "prompt_cache_key_hash": fingerprint(request_body.get("prompt_cache_key")),
                "request_bytes": len(body),
                "message_count": len(messages),
                "message_roles": [m.get("role") for m in messages],
                "matching_initial_messages": matching,
                "system_message_count": len(system_messages),
                "system_hash": fingerprint(system_messages),
                "tool_count": len(tools),
                "tools_hash": fingerprint(tools),
                "max_tokens": request_body.get("max_tokens"),
            }

            headers = {"Content-Type": self.headers.get("Content-Type", "application/json")}
            if authorization := self.headers.get("Authorization"):
                headers["Authorization"] = authorization
            upstream = urllib.request.Request(args.upstream.rstrip("/") + self.path, data=body, headers=headers)
            start = time.monotonic()
            try:
                response = urllib.request.urlopen(upstream, timeout=180)
            except urllib.error.HTTPError as error:
                response = error
            except Exception as error:
                self.send_error(502, f"upstream request failed: {error}")
                return

            first = response.read(1)
            summary["upstream_first_byte_ms"] = round((time.monotonic() - start) * 1000, 1)
            payload = first + response.read()
            summary["upstream_total_ms"] = round((time.monotonic() - start) * 1000, 1)
            summary["status"] = response.status
            for line in payload.splitlines():
                if not line.startswith(b"data: "):
                    continue
                try:
                    chunk = json.loads(line[6:])
                except ValueError:
                    continue
                if usage := chunk.get("usage"):
                    summary["prompt_tokens"] = usage.get("prompt_tokens")
                    summary["cached_tokens"] = (usage.get("prompt_tokens_details") or {}).get("cached_tokens")
                    summary["completion_tokens"] = usage.get("completion_tokens")

            with lock:
                with args.output.open("a") as output:
                    output.write(json.dumps(summary, sort_keys=True) + "\n")
            print(json.dumps(summary, sort_keys=True), flush=True)
            self.send_response(response.status)
            self.send_header("Content-Type", response.headers.get("Content-Type", "text/event-stream"))
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, _format, *_args):
            return

    server = ThreadingHTTPServer((host, int(port)), Handler)
    print(f"Listening on {args.listen}; forwarding to {args.upstream}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
