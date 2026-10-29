#!/usr/bin/env python3
"""Deterministic OpenAI-compatible mock provider for orchestration tasks.

The mock speaks the same streaming Chat Completions wire shape as the real
provider, but every response is selected by the task's trap file. A trap names
the model(s) the task may use, the ordered request kinds it expects, and the
canned assistant turns. Requests that do not match the trap fail closed instead
of silently falling back to a real model.

The task container reaches this server over the compose network at
http://mock:8080/v1. It needs no API key; the harness still passes a fake one
because the installed-agent adapter requires a non-empty value.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


PROPOSAL_ID = re.compile(r'"proposal_id"\s*:\s*"([^"]+)"')
WRITE_FIZZBUZZ = "fizzbuzz.py"


class TrapError(RuntimeError):
    """A request did not match the task's deterministic transcript."""


def _message_text(message: dict[str, Any]) -> str:
    content = message.get("content")
    return content if isinstance(content, str) else ""


def _tool_messages(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [m for m in messages if m.get("role") == "tool"]


def _system_text(messages: list[dict[str, Any]]) -> str:
    return "\n".join(
        _message_text(m) for m in messages if m.get("role") == "system"
    )


def _is_subagent(messages: list[dict[str, Any]]) -> bool:
    """Return true for the developer sub-agent loop.

    The parent session's identity says "You are yaah"; a developer dispatch
    appends the role guidance, whose first line is "You are a DEVELOPER
    sub-agent on yaah's team." Keep the match scoped to that marker so a
    prompt-cache or scaffold change cannot accidentally route the parent into
    the child transcript.
    """

    system = _system_text(messages).lower()
    return "developer sub-agent on yaah's team" in system


def _proposal_id(messages: list[dict[str, Any]]) -> str | None:
    for message in reversed(_tool_messages(messages)):
        match = PROPOSAL_ID.search(_message_text(message))
        if match:
            return match.group(1)
    return None


def _classify(messages: list[dict[str, Any]]) -> str:
    tools = _tool_messages(messages)
    if _is_subagent(messages):
        if any(
            "Wrote" in _message_text(m) and WRITE_FIZZBUZZ in _message_text(m)
            for m in tools
        ):
            return "subagent_final"
        return "subagent_write"

    if not tools:
        return "orchestrator_delegate"

    latest = _message_text(tools[-1])
    if '"status":"proposal"' in latest or '"status": "proposal"' in latest:
        return "orchestrator_discard"
    if '"status":"settled"' in latest or '"status": "settled"' in latest:
        return "orchestrator_write"
    if "Wrote" in latest and "answer.txt" in latest:
        return "orchestrator_final"

    # A proposal tool result may be followed by an unrelated tool result in a
    # batched turn; inspect the whole tail rather than only the last row.
    for message in reversed(tools):
        text = _message_text(message)
        if '"status":"proposal"' in text or '"status": "proposal"' in text:
            return "orchestrator_discard"
        if '"status":"settled"' in text or '"status": "settled"' in text:
            return "orchestrator_write"
        if "Wrote" in text and "answer.txt" in text:
            return "orchestrator_final"
    raise TrapError("orchestrator request has no recognized tool result")


def _stream_events(
    model: str,
    content: str | None,
    tool_name: str | None,
    tool_arguments: dict[str, Any] | None,
    prompt_tokens: int,
) -> list[dict[str, Any]]:
    base = {
        "id": "chatcmpl-mock",
        "object": "chat.completion.chunk",
        "created": 1700000000,
        "model": model,
    }
    events: list[dict[str, Any]] = []
    if tool_name is not None:
        call = {
            "index": 0,
            "id": "call_mock_0",
            "type": "function",
            "function": {
                "name": tool_name,
                "arguments": json.dumps(tool_arguments or {}, sort_keys=True),
            },
        }
        events.append(
            {
                **base,
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant", "tool_calls": [call]},
                        "finish_reason": None,
                    }
                ],
            }
        )
        finish = "tool_calls"
    else:
        events.append(
            {
                **base,
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant", "content": content or ""},
                        "finish_reason": None,
                    }
                ],
            }
        )
        finish = "stop"

    events.append(
        {
            **base,
            "choices": [{"index": 0, "delta": {}, "finish_reason": finish}],
        }
    )
    events.append(
        {
            **base,
            "choices": [],
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": 16,
                "total_tokens": prompt_tokens + 16,
            },
        }
    )
    return events


class MockHandler(BaseHTTPRequestHandler):
    server: "MockServer"

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
        sys.stderr.write("mock: " + format % args + "\n")

    def _json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/health":
            self._json(200, {"status": "ok", "step": self.server.step_index})
            return
        self._json(404, {"error": "not found"})

    def send_stream(self, events: list[dict[str, Any]]) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        for event in events:
            self.wfile.write(b"data: " + json.dumps(event).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def do_POST(self) -> None:  # noqa: N802
        if self.path not in ("/chat/completions", "/v1/chat/completions"):
            self._json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            request = json.loads(self.rfile.read(length))
            model = request.get("model")
            if model not in self.server.trap["model_allowlist"]:
                raise TrapError(
                    f"model {model!r} is not in the task allowlist "
                    f"{self.server.trap['model_allowlist']}"
                )
            messages = request.get("messages") or []
            if not isinstance(messages, list):
                raise TrapError("messages must be a list")
            kind = _classify(messages)
            self.server._advance(kind)
            content, tool_name, args = self.server._response(kind, messages)
            if (
                tool_name is not None
                and tool_name not in self.server.trap["allowed_tools"]
            ):
                raise TrapError(
                    f"tool {tool_name!r} is not in the task allowlist"
                )
            prompt_tokens = max(1, len(json.dumps(request)) // 4)
            events = _stream_events(model, content, tool_name, args, prompt_tokens)
            self.send_stream(events)
        except TrapError as exc:
            self._json(400, {"error": str(exc)})
        except Exception as exc:
            self.log_message("request failed: %s", exc)
            self._json(500, {"error": str(exc)})


class MockServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address: tuple[str, int], trap_path: Path):
        super().__init__(address, MockHandler)
        self.trap = json.loads(trap_path.read_text())
        self.trap_path = trap_path
        self.step_index = 0
        self._validate_trap()

    def _validate_trap(self) -> None:
        required = {"task", "model_allowlist", "steps", "allowed_tools"}
        missing = required - set(self.trap)
        if missing:
            raise TrapError(f"trap is missing keys: {sorted(missing)}")
        self.trap["steps"] = list(self.trap["steps"])
        self.trap["allowed_tools"] = set(self.trap["allowed_tools"])

    def _next_kind(self) -> str:
        steps = self.trap["steps"]
        if self.step_index >= len(steps):
            raise TrapError(
                f"unexpected request after {len(steps)} steps; "
                f"trap={self.trap_path}"
            )
        return steps[self.step_index]

    def _advance(self, kind: str) -> None:
        expected = self._next_kind()
        if kind != expected:
            raise TrapError(
                f"request {self.step_index + 1}: got {kind!r}, expected {expected!r}; "
                f"trap={self.trap_path}"
            )
        self.step_index += 1

    def _response(
        self, kind: str, messages: list[dict[str, Any]]
    ) -> tuple[str | None, str | None, dict[str, Any] | None]:
        trap = self.trap
        if kind == "orchestrator_delegate":
            args = dict(trap["delegate"])
            return None, "delegate", args
        if kind == "subagent_write":
            return None, "write", {
                "filePath": trap["fizzbuzz_path"],
                "content": trap["fizzbuzz"],
            }
        if kind == "subagent_final":
            return trap["developer_report"], None, None
        if kind == "orchestrator_discard":
            pid = _proposal_id(messages)
            if not pid:
                raise TrapError("proposal result is missing proposal_id")
            return None, "proposal", {"action": "discard", "proposal_id": pid}
        if kind == "orchestrator_write":
            pid = _proposal_id(messages)
            if not pid:
                raise TrapError("settlement result is missing proposal_id")
            return None, "write", {
                "filePath": "/app/answer.txt",
                "content": trap["answer_template"].format(proposal_id=pid),
            }
        if kind == "orchestrator_final":
            return trap["final"], None, None
        raise TrapError(f"unknown step kind {kind!r}")

def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--trap",
        default=os.environ.get("MOCK_TRAP", "/traps/orch-settle-discard.json"),
        type=Path,
    )
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    server = MockServer(("0.0.0.0", args.port), args.trap)
    print(
        f"mock: serving {server.trap['task']} from {args.trap} "
        f"on port {args.port}",
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
