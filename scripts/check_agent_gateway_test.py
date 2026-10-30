"""Unit tests for the agent gateway protocol check. No network calls."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_agent_gateway as gateway  # noqa: E402


def fake_post(status: int, content_type: str, body: bytes):
    def post(url, headers, payload, timeout):
        post.seen = (url, headers, payload)
        return status, content_type, body
    post.seen = None
    return post


class BaseTests(unittest.TestCase):
    def test_normalize_base_adds_v1_once(self):
        self.assertEqual(gateway.normalize_base("https://example.test"), "https://example.test/v1")
        self.assertEqual(gateway.normalize_base("https://example.test/v1/"), "https://example.test/v1")

    def test_endpoints_follow_selected_agents(self):
        self.assertEqual(gateway.endpoints_for_agents(["claude"]), ["anthropic"])
        self.assertEqual(gateway.endpoints_for_agents(["codex"]), ["responses", "responses_tools"])
        self.assertEqual(gateway.endpoints_for_agents(["opencode", "pi"]), ["chat"])
        self.assertEqual(gateway.endpoints_for_agents(["claude", "codex", "opencode", "pi"]),
                         ["anthropic", "responses", "responses_tools", "chat"])


class RunChecksTests(unittest.TestCase):
    def test_all_selected_endpoints_are_checked_and_failures_are_collected(self):
        def post(url, headers, payload, timeout):
            if url.endswith("/messages"):
                return 200, "application/json", b'{"type":"message","content":[]}'
            if url.endswith("/chat/completions"):
                return 200, "text/event-stream", b'data: {"choices":[]}\n\n'
            return 200, "application/json", b'{"object":"response"}'

        passed, failed = gateway.run_checks(
            "https://example.test/v1", "k", ["claude", "codex", "opencode"],
            {"anthropic": "a", "chat": "c", "responses": "r", "responses_tools": "r"}, post=post,
        )
        self.assertEqual(len(passed), 2)
        self.assertEqual(len(failed), 2)
        self.assertTrue(any("responses:" in line for line in failed))
        self.assertTrue(any("responses_tools:" in line for line in failed))
        self.assertIn("text/event-stream", failed[0])


class AnthropicTests(unittest.TestCase):
    def test_json_message_passes(self):
        post = fake_post(200, "application/json", b'{"type":"message","content":[{"type":"text","text":"ok"}]}')
        self.assertIn("ok", gateway.check_anthropic("https://example.test", "k", "m", post=post))
        self.assertEqual(post.seen[0], "https://example.test/v1/messages")
        self.assertEqual(post.seen[1]["x-api-key"], "k")

    def test_error_status_is_reported(self):
        post = fake_post(401, "application/json", b'{"error":{"message":"bad key"}}')
        with self.assertRaises(gateway.CheckError) as caught:
            gateway.check_anthropic("https://example.test", "k", "m", post=post)
        self.assertIn("HTTP 401", str(caught.exception))


class ChatTests(unittest.TestCase):
    def test_sse_stream_passes(self):
        body = b'data: {"choices":[{"delta":{"content":"ok"}}]}\n\ndata: [DONE]\n'
        post = fake_post(200, "text/event-stream; charset=utf-8", body)
        self.assertIn("ok", gateway.check_chat("https://example.test", "k", "m", post=post))

    def test_json_body_fails(self):
        post = fake_post(200, "application/json", b'{"choices":[]}')
        with self.assertRaises(gateway.CheckError) as caught:
            gateway.check_chat("https://example.test", "k", "m", post=post)
        self.assertIn("text/event-stream", str(caught.exception))


class ResponsesTests(unittest.TestCase):
    def test_sse_with_completed_passes(self):
        body = (b'data: {"type":"response.created","response":{"id":"resp_1"}}\n\n'
                b'data: {"type":"response.completed","response":{"id":"resp_1"}}\n\n')
        post = fake_post(200, "text/event-stream", body)
        self.assertIn("responses SSE", gateway.check_responses("https://example.test", "k", "m", post=post))
        self.assertEqual(post.seen[0], "https://example.test/v1/responses")
        self.assertEqual(post.seen[2]["stream"], True)

    def test_non_streaming_json_fails_with_the_observed_shape(self):
        # This is the gateway behavior the check exists to catch: /responses
        # accepts stream=true but answers with one JSON object.
        body = json.dumps({"object": "response", "status": "completed", "output_text": "ok"}).encode()
        post = fake_post(200, "application/json; charset=utf-8", body)
        with self.assertRaises(gateway.CheckError) as caught:
            gateway.check_responses("https://example.test", "k", "m", post=post)
        message = str(caught.exception)
        self.assertIn("text/event-stream", message)
        self.assertIn("application/json", message)

    def test_sse_without_completed_fails(self):
        body = b'data: {"type":"response.created","response":{"id":"resp_1"}}\n\n'
        post = fake_post(200, "text/event-stream", body)
        with self.assertRaises(gateway.CheckError) as caught:
            gateway.check_responses("https://example.test", "k", "m", post=post)
        self.assertIn("response.completed", str(caught.exception))


class ResponsesToolTests(unittest.TestCase):
    def test_function_call_event_passes(self):
        body = (b'data: {"type":"response.output_item.added","item":{"type":"function_call",'
                b'"name":"exec_command"}}\n\n'
                b'data: {"type":"response.completed","response":{"id":"resp_1"}}\n\n')
        post = fake_post(200, "text/event-stream", body)
        result = gateway.check_responses_tools("https://example.test", "k", "m", post=post)
        self.assertIn("responses tool call: ok", result)
        self.assertEqual(post.seen[2]["tool_choice"],
                         {"type": "function", "name": "exec_command"})

    def test_text_only_stream_fails(self):
        body = (b'data: {"type":"response.output_text.delta","delta":"ok"}\n\n'
                b'data: {"type":"response.completed","response":{"id":"resp_1"}}\n\n')
        post = fake_post(200, "text/event-stream", body)
        with self.assertRaises(gateway.CheckError) as caught:
            gateway.check_responses_tools("https://example.test", "k", "m", post=post)
        self.assertIn("function_call", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
