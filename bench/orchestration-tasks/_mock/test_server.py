"""Unit tests for the deterministic provider's transcript and allowlists."""

import json
import unittest
from pathlib import Path

import server


HERE = Path(__file__).resolve().parent
TRAP = HERE / "traps" / "orch-settle-discard-mock.json"


class MockProviderTest(unittest.TestCase):
    def setUp(self):
        self.server = server.MockServer(("127.0.0.1", 0), TRAP)
        self.messages = [
            {"role": "system", "content": "You are yaah, a vendor-free harness."},
            {"role": "user", "content": "exercise the discard path"},
        ]

    def tearDown(self):
        self.server.server_close()

    def test_classifies_parent_and_child_turns(self):
        self.assertEqual(
            server._classify(self.messages), "orchestrator_delegate"
        )
        child = [
            {
                "role": "system",
                "content": "You are a DEVELOPER sub-agent on yaah's team.",
            },
            {"role": "user", "content": "create fizzbuzz"},
        ]
        self.assertEqual(server._classify(child), "subagent_write")
        child += [
            {
                "role": "tool",
                "tool_call_id": "call-1",
                "content": "Wrote 10 bytes to fizzbuzz.py",
            }
        ]
        self.assertEqual(server._classify(child), "subagent_final")

    def test_trap_is_ordered_and_closed(self):
        for expected in self.server.trap["steps"]:
            self.assertEqual(self.server._next_kind(), expected)
            self.server._advance(expected)
        with self.assertRaises(server.TrapError):
            self.server._next_kind()

    def test_discard_uses_the_real_proposal_id(self):
        messages = self.messages + [
            {
                "role": "tool",
                "tool_call_id": "call-1",
                "content": json.dumps(
                    {"status": "proposal", "proposal_id": "proposal:42:7"}
                ),
            }
        ]
        content, tool, args = self.server._response(
            "orchestrator_discard", messages
        )
        self.assertIsNone(content)
        self.assertEqual(tool, "proposal")
        self.assertEqual(
            args, {"action": "discard", "proposal_id": "proposal:42:7"}
        )

    def test_stream_has_a_terminal_reason_and_usage(self):
        events = server._stream_events(
            "mock-orchestrator", "done", None, None, prompt_tokens=10
        )
        self.assertEqual(events[-2]["choices"][0]["finish_reason"], "stop")
        self.assertEqual(events[-1]["usage"]["prompt_tokens"], 10)
        self.assertEqual(events[-1]["choices"], [])

    def test_prompt_tokens_ignore_dynamic_proposal_ids(self):
        first = {"messages": [{"content": "proposal:11:1"}]}
        second = {"messages": [{"content": "proposal:999999:7"}]}
        self.assertEqual(
            server._stable_prompt_tokens(first),
            server._stable_prompt_tokens(second),
        )


if __name__ == "__main__":
    unittest.main()
