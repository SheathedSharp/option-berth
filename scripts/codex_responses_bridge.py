#!/usr/bin/env python3
"""Translate Codex Responses requests to an OpenAI chat-completions gateway.

Codex 0.166 speaks only the Responses API and expects a server-sent-event
stream ending in ``response.completed``. Some gateways expose OpenAI-compatible
chat completions and a non-streaming Responses endpoint, but not Responses SSE.
This loopback bridge is an evaluation fixture for the agent matrix: it maps the
request to chat completions, calls the gateway, and emits the Responses events
Codex consumes.

It is not a security boundary. Bind it to loopback (the default) and keep the
gateway key in this process's environment; never pass that key to the agent.
The bridge also drops tools it cannot represent (for example hosted web search)
and forwards only function and custom tools.

    CODE_BRIDGE_GATEWAY_BASE=https://gateway.example/v1 \\
    CODE_BRIDGE_API_KEY=... \\
    python3 scripts/codex_responses_bridge.py --port 18080
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Iterable

DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 18080
MAX_BODY = 16 * 1024 * 1024
UPSTREAM_TIMEOUT = 300.0
USER_AGENT = "option-berth-codex-bridge/1.0"
LOOPBACK_HOSTS = {"127.0.0.1", "::1", "localhost"}


class BridgeError(RuntimeError):
    """A request could not be translated or the upstream refused it."""


def content_text(content: Any) -> str:
    """Return the text carried by a Responses content value."""
    if isinstance(content, str):
        return content
    if isinstance(content, dict):
        return content_text(content.get("content", content.get("text", "")))
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    for part in content:
        if not isinstance(part, dict):
            continue
        text = part.get("text")
        if isinstance(text, str):
            parts.append(text)
        elif part.get("type") in ("input_image", "image_url"):
            parts.append("[image omitted by bridge]")
    return "\n".join(parts)


def _iter_tools(raw: Iterable[Any]):
    for tool in raw:
        if not isinstance(tool, dict):
            continue
        kind = tool.get("type")
        if kind == "namespace":
            yield from _iter_tools(tool.get("tools") or [])
        elif kind in ("function", "custom"):
            yield tool


def chat_tools(request: dict[str, Any]) -> tuple[list[dict[str, Any]], set[str]]:
    """Convert Codex function/custom tools, flattening namespace tools."""
    raw: list[Any] = list(request.get("tools") or [])
    for item in request.get("input") or []:
        if isinstance(item, dict) and item.get("type") == "additional_tools":
            raw.extend(item.get("tools") or [])
    tools: list[dict[str, Any]] = []
    custom: set[str] = set()
    for tool in _iter_tools(raw):
        name = tool.get("name")
        if not isinstance(name, str) or not name:
            continue
        if tool.get("type") == "custom":
            custom.add(name)
            parameters = {
                "type": "object",
                "properties": {
                    "input": {
                        "type": "string",
                        "description": "The complete raw input for this tool.",
                    }
                },
                "required": ["input"],
                "additionalProperties": False,
            }
        else:
            parameters = tool.get("parameters") or {"type": "object", "properties": {}}
        function: dict[str, Any] = {"name": name, "parameters": parameters}
        if isinstance(tool.get("description"), str):
            function["description"] = tool["description"]
        if tool.get("type") == "function" and "strict" in tool:
            function["strict"] = tool["strict"]
        tools.append({"type": "function", "function": function})
    return tools, custom


def _call_item(item: dict[str, Any]) -> dict[str, Any]:
    call_id = item.get("call_id") or item.get("id") or "call_" + uuid.uuid4().hex
    if item.get("type") == "custom_tool_call":
        arguments = item.get("input")
    else:
        arguments = item.get("arguments")
    if not isinstance(arguments, str):
        arguments = json.dumps(arguments if arguments is not None else {})
    return {
        "id": call_id,
        "type": "function",
        "function": {"name": item.get("name") or "", "arguments": arguments},
    }


def chat_messages(request: dict[str, Any]) -> list[dict[str, Any]]:
    """Map Responses input items to chat messages."""
    system: list[str] = []
    messages: list[dict[str, Any]] = []
    for item in request.get("input") or []:
        if not isinstance(item, dict):
            continue
        kind = item.get("type")
        if kind == "message":
            role = item.get("role") or "user"
            text = content_text(item.get("content"))
            if role in ("developer", "system"):
                if text:
                    system.append(text)
            elif role == "assistant":
                messages.append({"role": "assistant", "content": text})
            else:
                messages.append({"role": "user", "content": text})
        elif kind in ("function_call", "custom_tool_call"):
            call = _call_item(item)
            if (
                messages
                and messages[-1].get("role") == "assistant"
                and messages[-1].get("tool_calls") is not None
                and messages[-1].get("content") is None
            ):
                messages[-1]["tool_calls"].append(call)
            else:
                messages.append({"role": "assistant", "content": None, "tool_calls": [call]})
        elif kind in ("function_call_output", "custom_tool_call_output"):
            messages.append({
                "role": "tool",
                "tool_call_id": item.get("call_id") or "",
                "content": content_text(item.get("output")),
            })
        # reasoning, additional_tools and unknown items carry no chat message.
    if system:
        messages.insert(0, {"role": "system", "content": "\n\n".join(system)})
    return messages


def _tool_choice(value: Any) -> Any:
    if isinstance(value, str):
        return value
    if isinstance(value, dict):
        name = value.get("name") or (value.get("function") or {}).get("name")
        if isinstance(name, str) and name:
            return {"type": "function", "function": {"name": name}}
    return "auto"


def chat_request(request: dict[str, Any]) -> tuple[dict[str, Any], set[str]]:
    tools, custom = chat_tools(request)
    body: dict[str, Any] = {
        "model": request.get("model"),
        "messages": chat_messages(request),
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    if tools:
        body["tools"] = tools
    if request.get("tool_choice") is not None:
        body["tool_choice"] = _tool_choice(request["tool_choice"])
    if request.get("parallel_tool_calls") is not None:
        body["parallel_tool_calls"] = request["parallel_tool_calls"]
    return body, custom


def _usage(raw: Any) -> dict[str, int]:
    if not isinstance(raw, dict):
        return {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    return {
        "input_tokens": int(raw.get("prompt_tokens") or 0),
        "output_tokens": int(raw.get("completion_tokens") or 0),
        "total_tokens": int(raw.get("total_tokens") or 0),
    }


def _consume_chunk(
    chunk: dict[str, Any],
    text: list[str],
    calls: dict[int, dict[str, str]],
) -> dict[str, int] | None:
    if isinstance(chunk.get("error"), dict):
        message = chunk["error"].get("message") or chunk["error"].get("type") or "upstream error"
        raise BridgeError(str(message))
    usage = chunk.get("usage")
    for choice in chunk.get("choices") or []:
        message = choice.get("message") if isinstance(choice.get("message"), dict) else None
        delta = choice.get("delta") if isinstance(choice.get("delta"), dict) else None
        source = message if message is not None else delta
        if not source:
            continue
        content = source.get("content")
        if isinstance(content, str):
            text.append(content)
        for index, call in enumerate(source.get("tool_calls") or []):
            if not isinstance(call, dict):
                continue
            key = call.get("index")
            if not isinstance(key, int):
                key = index
            slot = calls.setdefault(key, {"id": "", "name": "", "arguments": ""})
            if isinstance(call.get("id"), str) and call["id"]:
                slot["id"] = call["id"]
            function = call.get("function")
            if isinstance(function, dict):
                if isinstance(function.get("name"), str):
                    slot["name"] += function["name"]
                if isinstance(function.get("arguments"), str):
                    slot["arguments"] += function["arguments"]
    return _usage(usage) if usage is not None else None


def aggregate_chat(lines: Iterable[bytes]) -> tuple[str, list[dict[str, str]], dict[str, int]]:
    """Aggregate chat SSE chunks, or a single non-streaming completion."""
    text: list[str] = []
    calls: dict[int, dict[str, str]] = {}
    usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    for raw in lines:
        line = raw.decode("utf-8", "replace").strip() if isinstance(raw, (bytes, bytearray)) else raw.strip()
        if not line:
            continue
        if line.startswith("data:"):
            data = line[5:].strip()
            if data == "[DONE]":
                break
            try:
                chunk = json.loads(data)
            except json.JSONDecodeError:
                continue
        else:
            try:
                chunk = json.loads(line)
            except json.JSONDecodeError:
                continue
        if not isinstance(chunk, dict):
            continue
        seen = _consume_chunk(chunk, text, calls)
        if seen is not None:
            usage = seen
    ordered = [calls[key] for key in sorted(calls) if calls[key]["name"]]
    return "".join(text), ordered, usage


def response_events(
    text: str,
    calls: list[dict[str, str]],
    custom: set[str],
    usage: dict[str, int],
    *,
    response_id: str | None = None,
) -> list[dict[str, Any]]:
    """Build the Responses events Codex needs to finish a turn."""
    rid = response_id or "resp_" + uuid.uuid4().hex[:24]
    events: list[dict[str, Any]] = [
        {"type": "response.created", "response": {"id": rid, "status": "in_progress"}},
    ]
    if text:
        events.append({
            "type": "response.output_item.done",
            "item": {
                "type": "message",
                "id": "msg_" + uuid.uuid4().hex[:16],
                "role": "assistant",
                "status": "completed",
                "content": [{"type": "output_text", "text": text}],
            },
        })
    for call in calls:
        if call["name"] in custom:
            raw_input = call["arguments"]
            try:
                parsed = json.loads(raw_input)
            except json.JSONDecodeError:
                parsed = None
            if isinstance(parsed, dict) and isinstance(parsed.get("input"), str):
                raw_input = parsed["input"]
            events.append({
                "type": "response.output_item.done",
                "item": {
                    "type": "custom_tool_call",
                    "id": "ctc_" + uuid.uuid4().hex[:16],
                    "call_id": call["id"] or "call_" + uuid.uuid4().hex[:16],
                    "name": call["name"],
                    "input": raw_input,
                    "status": "completed",
                },
            })
        else:
            events.append({
                "type": "response.output_item.done",
                "item": {
                    "type": "function_call",
                    "id": "fc_" + uuid.uuid4().hex[:16],
                    "call_id": call["id"] or "call_" + uuid.uuid4().hex[:16],
                    "name": call["name"],
                    "arguments": call["arguments"] or "{}",
                    "status": "completed",
                },
            })
    events.append({
        "type": "response.completed",
        "response": {"id": rid, "status": "completed", "usage": usage},
    })
    return events


class Upstream:
    """Call an OpenAI-compatible chat completions endpoint."""

    def __init__(self, base_url: str, api_key: str, timeout: float = UPSTREAM_TIMEOUT):
        base = base_url.rstrip("/")
        if not base.endswith("/v1"):
            base += "/v1"
        self.url = base + "/chat/completions"
        self.api_key = api_key
        self.timeout = timeout

    def _post(self, body: dict[str, Any]) -> tuple[str, list[dict[str, str]], dict[str, int]]:
        request = urllib.request.Request(
            self.url,
            data=json.dumps(body).encode(),
            headers={
                "Authorization": "Bearer " + self.api_key,
                "Content-Type": "application/json",
                "User-Agent": USER_AGENT,
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return aggregate_chat(response)
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace")[:500]
            raise BridgeError(f"upstream HTTP {exc.code}: {detail}") from exc
        except urllib.error.URLError as exc:
            raise BridgeError(f"upstream unavailable: {exc.reason}") from exc

    def complete(self, request: dict[str, Any]) -> tuple[str, list[dict[str, str]], dict[str, int], set[str]]:
        body, custom = chat_request(request)
        try:
            text, calls, usage = self._post(body)
        except BridgeError:
            # Some chat endpoints reject stream_options even when they stream.
            if "stream_options" in body:
                body = dict(body)
                body.pop("stream_options", None)
                text, calls, usage = self._post(body)
            else:
                raise
        return text, calls, usage, custom


class BridgeHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "CodexResponsesBridge/1.0"

    def log_message(self, format: str, *args: Any) -> None:
        if self.server.verbose:  # type: ignore[attr-defined]
            super().log_message(format, *args)

    def _send_json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path.split("?", 1)[0] == "/healthz":
            self._send_json(200, {"ok": True})
            return
        self._send_json(404, {"error": {"message": "not found"}})

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0]
        if path not in ("/v1/responses", "/responses"):
            self._send_json(404, {"error": {"message": "not found"}})
            return
        try:
            length = int(self.headers.get("content-length") or 0)
        except ValueError:
            length = 0
        if length <= 0 or length > MAX_BODY:
            self._send_json(413, {"error": {"message": "request body too large or missing"}})
            return
        raw = self.rfile.read(length)
        try:
            request = json.loads(raw)
            if not isinstance(request, dict):
                raise ValueError("request must be an object")
        except (json.JSONDecodeError, ValueError) as exc:
            self._send_json(400, {"error": {"message": f"invalid request: {exc}"}})
            return

        started = time.monotonic()
        try:
            text, calls, usage, custom = self.server.upstream.complete(request)  # type: ignore[attr-defined]
        except BridgeError as exc:
            self._log_error(request, exc)
            self._send_json(502, {"error": {"message": str(exc)}})
            return

        events = response_events(text, calls, custom, usage)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            for event in events:
                self.wfile.write(b"data: " + json.dumps(event).encode() + b"\n\n")
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            # The agent gave up; the upstream call already succeeded.
            pass
        self.close_connection = True
        self.server.note(  # type: ignore[attr-defined]
            request,
            text=text,
            calls=calls,
            elapsed=time.monotonic() - started,
        )

    def _log_error(self, request: dict[str, Any], exc: BridgeError) -> None:
        self.server.note(request, error=str(exc), elapsed=0.0)  # type: ignore[attr-defined]


class BridgeServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address: tuple[str, int], upstream: Upstream, log_path: Path | None, verbose: bool):
        super().__init__(address, BridgeHandler)
        self.upstream = upstream
        self.log_path = log_path
        self.verbose = verbose
        self._lock = threading.Lock()
        self._count = 0

    def note(self, request: dict[str, Any], *, text: str = "", calls: list[dict[str, str]] | None = None,
             elapsed: float = 0.0, error: str | None = None) -> None:
        with self._lock:
            self._count += 1
            count = self._count
        line = (
            f"request {count}: model={request.get('model')!r} "
            f"items={len(request.get('input') or [])} "
            f"tools={len(chat_tools(request)[0])} "
            f"text_chars={len(text)} calls={len(calls or [])} "
            f"elapsed_ms={elapsed * 1000:.0f}"
        )
        if error:
            line += f" error={error!r}"
        self.log(line)

    def log(self, line: str) -> None:
        if self.log_path is None:
            print(line, file=sys.stderr, flush=True)
            return
        with self._lock:
            with self.log_path.open("a", encoding="utf-8") as handle:
                handle.write(line + "\n")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Loopback Responses-to-chat-completions bridge for Codex.")
    parser.add_argument("--host", default=DEFAULT_HOST, help=f"bind address (default {DEFAULT_HOST})")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help=f"bind port (default {DEFAULT_PORT})")
    parser.add_argument("--gateway-base", default=os.environ.get("CODE_BRIDGE_GATEWAY_BASE"),
                        help="OpenAI-compatible gateway base URL; defaults to CODE_BRIDGE_GATEWAY_BASE")
    parser.add_argument("--log", default=None, help="append request summaries to this file")
    parser.add_argument("--verbose", action="store_true", help="log every HTTP request")
    parser.add_argument("--allow-non-loopback", action="store_true",
                        help="allow binding a non-loopback address (not recommended)")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.host not in LOOPBACK_HOSTS and not args.allow_non_loopback:
        print(f"refusing to bind non-loopback address {args.host!r}; pass --allow-non-loopback",
              file=sys.stderr)
        return 2
    api_key = os.environ.get("CODE_BRIDGE_API_KEY")
    if not args.gateway_base or not api_key:
        print("CODE_BRIDGE_GATEWAY_BASE and CODE_BRIDGE_API_KEY are required", file=sys.stderr)
        return 2
    upstream = Upstream(args.gateway_base, api_key)
    log_path = Path(args.log).expanduser() if args.log else None
    if log_path is not None:
        log_path.parent.mkdir(parents=True, exist_ok=True)
    server = BridgeServer((args.host, args.port), upstream, log_path, args.verbose)

    def stop(signum: int, _frame: Any) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()
        print(f"stopping on signal {signum}", file=sys.stderr, flush=True)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    print(f"listening on http://{args.host}:{args.port}/v1/responses -> {upstream.url}",
          file=sys.stderr, flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
