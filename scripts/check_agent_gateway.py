#!/usr/bin/env python3
"""Check the gateway protocols the agent matrix depends on.

The manual agent-eval workflow calls three endpoints:

- Anthropic Messages for Claude Code;
- OpenAI chat completions as a streaming SSE response for OpenCode and Pi;
- OpenAI Responses as a streaming SSE response ending in ``response.completed``
  for Codex.

A gateway can accept all three paths and still break an agent. In particular,
Codex 0.166 only speaks Responses and treats a non-SSE body as a broken stream,
even when the request carried ``stream: true``. This check fails before any
coding agent runs, names the endpoint, and prints what the gateway returned.
It never prints the key.

    AGENT_GATEWAY_BASE_URL=https://gateway.example \
    AGENT_GATEWAY_API_KEY=... \
    python3 scripts/check_agent_gateway.py --agents claude,codex,opencode
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from typing import Any, Callable

TIMEOUT = 60.0
RETRY_STATUSES = {502, 503, 504}
USER_AGENT = "option-berth-gateway-check/1.0"
Post = Callable[[str, dict[str, str], dict[str, Any], float], tuple[int, str, bytes]]


class CheckError(RuntimeError):
    """A gateway endpoint did not satisfy the protocol an agent needs."""


def normalize_base(base: str) -> str:
    base = base.strip().rstrip("/")
    if not base:
        raise CheckError("gateway base URL is empty")
    if not base.endswith("/v1"):
        base += "/v1"
    return base


def post_json(url: str, headers: dict[str, str], body: dict[str, Any], timeout: float,
              *, attempts: int = 3) -> tuple[int, str, bytes]:
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "User-Agent": USER_AGENT, **headers},
        method="POST",
    )
    for attempt in range(1, attempts + 1):
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return response.status, response.headers.get("content-type", ""), response.read()
        except urllib.error.HTTPError as exc:
            status, content_type, body_bytes = exc.code, exc.headers.get("content-type", ""), exc.read()
            if status in RETRY_STATUSES and attempt < attempts:
                time.sleep(2)
                continue
            return status, content_type, body_bytes
        except urllib.error.URLError as exc:
            if attempt < attempts:
                time.sleep(2)
                continue
            raise CheckError(f"{url}: {exc.reason}") from exc
    raise CheckError(f"{url}: exhausted retries")


def _json_body(status: int, content_type: str, body: bytes) -> Any:
    if status != 200:
        raise CheckError(f"HTTP {status}, content-type={content_type or 'unknown'}: "
                         f"{body[:300].decode('utf-8', 'replace')}")
    try:
        return json.loads(body)
    except json.JSONDecodeError as exc:
        raise CheckError(f"expected JSON, got content-type={content_type or 'unknown'}: "
                         f"{body[:300].decode('utf-8', 'replace')}") from exc


def _sse_lines(status: int, content_type: str, body: bytes) -> list[str]:
    if status != 200:
        raise CheckError(f"HTTP {status}, content-type={content_type or 'unknown'}: "
                         f"{body[:300].decode('utf-8', 'replace')}")
    if "text/event-stream" not in content_type.lower():
        raise CheckError(
            f"expected text/event-stream, got content-type={content_type or 'unknown'}; "
            f"body starts with {body[:160].decode('utf-8', 'replace')!r}"
        )
    return [line[5:].strip() for line in body.decode("utf-8", "replace").splitlines()
            if line.startswith("data:")]


def check_anthropic(base: str, key: str, model: str, *, post: Post = post_json,
                    timeout: float = TIMEOUT) -> str:
    status, content_type, body = post(
        normalize_base(base) + "/messages",
        {"x-api-key": key, "anthropic-version": "2023-06-01"},
        {"model": model, "max_tokens": 8,
         "messages": [{"role": "user", "content": "Reply with ok"}]},
        timeout,
    )
    document = _json_body(status, content_type, body)
    if not isinstance(document, dict) or not (document.get("content") or document.get("type")):
        raise CheckError("Anthropic response did not contain a message body")
    return f"anthropic messages: ok (model={model})"


def check_chat(base: str, key: str, model: str, *, post: Post = post_json,
               timeout: float = TIMEOUT) -> str:
    status, content_type, body = post(
        normalize_base(base) + "/chat/completions",
        {"Authorization": "Bearer " + key},
        {"model": model, "stream": True,
         "messages": [{"role": "user", "content": "Reply with ok"}]},
        timeout,
    )
    events = _sse_lines(status, content_type, body)
    if not events:
        raise CheckError("chat completions stream contained no data events")
    return f"chat completions SSE: ok (model={model})"


def _responses_events(status: int, content_type: str, body: bytes) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    for line in _sse_lines(status, content_type, body):
        if line == "[DONE]":
            continue
        try:
            document = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(document, dict):
            events.append(document)
    return events


def check_responses(base: str, key: str, model: str, *, post: Post = post_json,
                    timeout: float = TIMEOUT) -> str:
    status, content_type, body = post(
        normalize_base(base) + "/responses",
        {"Authorization": "Bearer " + key, "Accept": "text/event-stream"},
        {"model": model, "input": "Reply with ok", "stream": True},
        timeout,
    )
    if not any(event.get("type") == "response.completed"
               for event in _responses_events(status, content_type, body)):
        raise CheckError(
            "Responses stream did not contain response.completed; "
            "Codex 0.166 requires Responses SSE, not a single JSON object"
        )
    return f"responses SSE: ok (model={model})"


def check_responses_tools(base: str, key: str, model: str, *, post: Post = post_json,
                          timeout: float = TIMEOUT) -> str:
    status, content_type, body = post(
        normalize_base(base) + "/responses",
        {"Authorization": "Bearer " + key, "Accept": "text/event-stream"},
        {
            "model": model,
            "input": "Call exec_command with cmd 'true'. Do not answer in text.",
            "stream": True,
            "tools": [{
                "type": "function",
                "name": "exec_command",
                "description": "Run a shell command.",
                "parameters": {
                    "type": "object",
                    "properties": {"cmd": {"type": "string"}},
                    "required": ["cmd"],
                },
            }],
            "tool_choice": {"type": "function", "name": "exec_command"},
        },
        timeout,
    )
    for event in _responses_events(status, content_type, body):
        kind = event.get("type")
        item = event.get("item") if isinstance(event.get("item"), dict) else {}
        if kind in ("response.function_call_arguments.delta",
                    "response.function_call_arguments.done"):
            return f"responses tool call: ok (model={model})"
        if kind in ("response.output_item.added", "response.output_item.done") \
                and item.get("type") == "function_call":
            return f"responses tool call: ok (model={model})"
    raise CheckError(
        "Responses stream did not contain a function_call; "
        "the gateway must translate standard Responses tools for Codex"
    )


CHECKS = {
    "anthropic": check_anthropic,
    "chat": check_chat,
    "responses": check_responses,
    "responses_tools": check_responses_tools,
}


def endpoints_for_agents(agents: list[str]) -> list[str]:
    needed: list[str] = []
    if "claude" in agents:
        needed.append("anthropic")
    if "codex" in agents:
        needed.extend(["responses", "responses_tools"])
    if "opencode" in agents or "pi" in agents:
        needed.append("chat")
    return needed


def run_checks(base: str, key: str, agents: list[str], models: dict[str, str], *,
               post: Post = post_json, timeout: float = TIMEOUT) -> tuple[list[str], list[str]]:
    """Check every endpoint the selected agents need; return successes and failures."""
    passed: list[str] = []
    failed: list[str] = []
    for endpoint in endpoints_for_agents(agents):
        model = models.get(endpoint, "")
        if not model:
            failed.append(f"{endpoint}: no model configured")
            continue
        try:
            passed.append(CHECKS[endpoint](base, key, model, post=post, timeout=timeout))
        except CheckError as exc:
            failed.append(f"{endpoint}: {exc}")
    return passed, failed


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Check the agent gateway protocol contract.")
    parser.add_argument("--base", default=os.environ.get("AGENT_GATEWAY_BASE_URL", ""),
                        help="gateway root, with or without /v1")
    parser.add_argument("--key-env", default="AGENT_GATEWAY_API_KEY",
                        help="environment variable holding the gateway key")
    parser.add_argument("--agents", default="claude,codex,opencode,pi",
                        help="comma list; each agent selects the endpoint it needs")
    parser.add_argument("--claude-model", default=os.environ.get("CLAUDE_MODEL", ""))
    parser.add_argument("--codex-model", default=os.environ.get("CODEX_MODEL", ""))
    parser.add_argument("--chat-model", default=os.environ.get("CHAT_MODEL", ""))
    parser.add_argument("--timeout", type=float, default=TIMEOUT)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if not args.base:
        print("AGENT_GATEWAY_BASE_URL is required", file=sys.stderr)
        return 2
    key = os.environ.get(args.key_env, "")
    if not key:
        print(f"{args.key_env} is required", file=sys.stderr)
        return 2
    agents = [item.strip() for item in args.agents.split(",") if item.strip()]
    models = {
        "anthropic": args.claude_model,
        "chat": args.chat_model,
        "responses": args.codex_model,
        "responses_tools": args.codex_model,
    }
    try:
        base = normalize_base(args.base)
    except CheckError as exc:
        print(f"gateway check failed: {exc}", file=sys.stderr)
        return 2
    passed, failed = run_checks(base, key, agents, models, timeout=args.timeout)
    for line in passed:
        print(line)
    for line in failed:
        print(f"gateway check failed: {line}", file=sys.stderr)
    return 0 if not failed else 2


if __name__ == "__main__":
    raise SystemExit(main())
