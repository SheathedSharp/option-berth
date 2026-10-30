"""Unit tests for the Codex Responses-to-chat bridge. No network calls."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import codex_responses_bridge as bridge  # noqa: E402


def request_with(items: list[dict], tools: list[dict] | None = None) -> dict:
    return {"model": "test-model", "input": items, "tools": tools or [], "tool_choice": "auto"}


class MessageMappingTests(unittest.TestCase):
    def test_developer_messages_become_one_system_message(self):
        request = request_with([
            {"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "rule one"}]},
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "task"}]},
            {"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "rule two"}]},
        ])
        messages = bridge.chat_messages(request)
        self.assertEqual(messages[0], {"role": "system", "content": "rule one\n\nrule two"})
        self.assertEqual(messages[1], {"role": "user", "content": "task"})

    def test_function_call_and_output_become_assistant_and_tool_messages(self):
        request = request_with([
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "run it"}]},
            {"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": '{"cmd":"true"}'},
            {"type": "function_call_output", "call_id": "call_1", "output": "done"},
        ])
        messages = bridge.chat_messages(request)
        self.assertEqual(messages[1]["role"], "assistant")
        self.assertIsNone(messages[1]["content"])
        self.assertEqual(messages[1]["tool_calls"][0]["id"], "call_1")
        self.assertEqual(messages[1]["tool_calls"][0]["function"]["name"], "exec_command")
        self.assertEqual(messages[2], {"role": "tool", "tool_call_id": "call_1", "content": "done"})

    def test_custom_tool_call_uses_input_as_arguments(self):
        request = request_with([
            {"type": "custom_tool_call", "call_id": "call_2", "name": "exec", "input": "return 1"}])
        messages = bridge.chat_messages(request)
        self.assertEqual(messages[0]["tool_calls"][0]["function"]["arguments"], "return 1")

    def test_reasoning_and_additional_tools_are_skipped(self):
        request = request_with([
            {"type": "additional_tools", "role": "developer", "tools": []},
            {"type": "reasoning", "summary": []},
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
        ])
        self.assertEqual(bridge.chat_messages(request), [{"role": "user", "content": "hi"}])


class ToolMappingTests(unittest.TestCase):
    def test_namespaces_are_flattened_and_custom_tools_are_marked(self):
        tools = [{
            "type": "namespace",
            "name": "functions",
            "tools": [
                {"type": "custom", "name": "exec", "description": "run JS",
                 "format": {"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}},
                {"type": "function", "name": "wait", "description": "wait",
                 "parameters": {"type": "object", "properties": {}}},
            ],
        }]
        converted, custom = bridge.chat_tools(request_with([], tools))
        self.assertEqual([tool["function"]["name"] for tool in converted], ["exec", "wait"])
        self.assertEqual(custom, {"exec"})
        self.assertEqual(converted[0]["function"]["parameters"]["required"], ["input"])
        self.assertNotIn("format", converted[0]["function"])

    def test_additional_tools_are_included(self):
        request = request_with([{
            "type": "additional_tools",
            "role": "developer",
            "tools": [{"type": "function", "name": "extra",
                       "parameters": {"type": "object", "properties": {}}}],
        }])
        converted, _ = bridge.chat_tools(request)
        self.assertEqual([tool["function"]["name"] for tool in converted], ["extra"])

    def test_hosted_tools_are_dropped(self):
        converted, _ = bridge.chat_tools(request_with([], [{"type": "web_search"}]))
        self.assertEqual(converted, [])

    def test_tool_choice_object_maps_to_chat_function(self):
        request = request_with([], [{"type": "function", "name": "wait",
                                     "parameters": {"type": "object", "properties": {}}}])
        request["tool_choice"] = {"type": "function", "name": "wait"}
        body, _ = bridge.chat_request(request)
        self.assertEqual(body["tool_choice"], {"type": "function", "function": {"name": "wait"}})


class StreamAggregationTests(unittest.TestCase):
    def test_text_and_tool_call_deltas_are_aggregated(self):
        lines = [
            b'data: {"choices":[{"delta":{"role":"assistant","content":"hel"}}]}\n',
            b'data: {"choices":[{"delta":{"content":"lo"}}]}\n',
            b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9",'
            b'"function":{"name":"exec_command","arguments":"{\\"cmd\\":"}}]}}]}\n',
            b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,'
            b'"function":{"arguments":"\\"true\\"}"}}]}}]}\n',
            b'data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}\n',
            b"data: [DONE]\n",
        ]
        text, calls, usage = bridge.aggregate_chat(lines)
        self.assertEqual(text, "hello")
        self.assertEqual(calls[0]["id"], "call_9")
        self.assertEqual(calls[0]["name"], "exec_command")
        self.assertEqual(calls[0]["arguments"], '{"cmd":"true"}')
        self.assertEqual(usage, {"input_tokens": 3, "output_tokens": 4, "total_tokens": 7})

    def test_non_streaming_json_is_accepted(self):
        payload = {
            "choices": [{"message": {
                "role": "assistant",
                "content": "ok",
                "tool_calls": [{"id": "call_1", "type": "function",
                                "function": {"name": "wait", "arguments": "{}"}}],
            }}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
        }
        text, calls, usage = bridge.aggregate_chat([json.dumps(payload).encode()])
        self.assertEqual(text, "ok")
        self.assertEqual(calls, [{"id": "call_1", "name": "wait", "arguments": "{}"}])
        self.assertEqual(usage["total_tokens"], 3)

    def test_upstream_error_is_raised(self):
        with self.assertRaises(bridge.BridgeError):
            bridge.aggregate_chat([b'data: {"error":{"message":"bad key"}}\n'])


class ResponseEventTests(unittest.TestCase):
    def test_message_and_function_call_events_end_in_completed(self):
        events = bridge.response_events(
            "hello",
            [{"id": "call_1", "name": "exec_command", "arguments": '{"cmd":"true"}'}],
            set(),
            {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )
        self.assertEqual(events[0]["type"], "response.created")
        self.assertEqual(events[1]["type"], "response.output_item.done")
        self.assertEqual(events[1]["item"]["type"], "message")
        self.assertEqual(events[1]["item"]["content"][0]["text"], "hello")
        self.assertEqual(events[2]["item"]["type"], "function_call")
        self.assertEqual(events[2]["item"]["call_id"], "call_1")
        self.assertEqual(events[-1]["type"], "response.completed")
        self.assertEqual(events[-1]["response"]["usage"]["total_tokens"], 3)

    def test_custom_call_input_is_unwrapped(self):
        events = bridge.response_events(
            "",
            [{"id": "call_2", "name": "exec", "arguments": '{"input":"return 1"}'}],
            {"exec"},
            {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
        )
        self.assertEqual(events[1]["item"]["type"], "custom_tool_call")
        self.assertEqual(events[1]["item"]["input"], "return 1")

    def test_custom_call_with_non_json_arguments_keeps_raw_text(self):
        events = bridge.response_events(
            "",
            [{"id": "call_3", "name": "exec", "arguments": "return 2"}],
            {"exec"},
            {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
        )
        self.assertEqual(events[1]["item"]["input"], "return 2")


class UpstreamUrlTests(unittest.TestCase):
    def test_base_without_v1_gets_v1(self):
        self.assertEqual(bridge.Upstream("https://example.test", "k").url,
                         "https://example.test/v1/chat/completions")

    def test_base_with_v1_is_unchanged(self):
        self.assertEqual(bridge.Upstream("https://example.test/v1/", "k").url,
                         "https://example.test/v1/chat/completions")


if __name__ == "__main__":
    unittest.main()
