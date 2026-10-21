# ACP (Agent Client Protocol)

How `yaah acp-serve` exposes yaah as an Agent Client Protocol v1 agent over
stdio: the session lifecycle, where resumed sessions live, and what is
deliberately out of scope (Tier 3).

- Spec: <https://agentclientprotocol.com> (this server speaks v1)
- Implementation: `internal/acp` (protocol + server), `cmd/yaah/acp_cmd.go`
  (composition), `internal/acp/view.go` (event → update translation)

## Overview

`yaah acp-serve` runs yaah as an ACP **agent** (server side) speaking
newline-delimited JSON-RPC 2.0 on stdin/stdout. All diagnostics go to stderr
so stdout stays protocol-clean.

```
ACP client (Zed, ...)                    yaah acp-serve
─────────────────────                    ───────────────
 ── initialize ─────────────────────────▶ version + agentCapabilities
 ── session/new {cwd} ──────────────────▶ agentSession built; ACP session id
 ── session/load {sessionId} ──────────▶ conversation restored from state.db
 ── session/prompt ─────────────────────▶ session/update stream, then stopReason
 ◀──────── session/request_permission ── tool approvals
```

One process serves any number of independent conversations. Each
`session/new` or `session/load` builds its own `agentSession` with the
request's cwd; sessions never share a working directory and the server never
calls `os.Chdir`. The cwd is passed into construction, so tools, MCP servers,
sub-agents, and prompts all resolve against it.

## Protocol surface

| Method | Status | Notes |
|---|---|---|
| `initialize` | supported | advertises `agentCapabilities.loadSession: true`; `authMethods` is empty so `authenticate` is rejected |
| `session/new` | supported | requires the client's absolute cwd; every call creates a distinct session |
| `session/load` | supported | resume + replay; see below |
| `session/prompt` | supported | streams updates, then answers with a `stopReason` |
| `session/cancel` | supported | notification; cancels the in-flight turn |
| `session/set_mode` | acknowledged | mode switching is not modeled; the response keeps mode-picker clients from hanging |
| `session/update` | supported | variants below |
| `session/request_permission` | supported | tool approvals delegated to the client |

`session/update` variants: `agent_message_chunk`, `agent_thought_chunk`,
`tool_call`, `tool_call_update`, `plan`, `usage_update`, and
`user_message_chunk` (replay only, on `session/load`).

## Session lifecycle

### session/new

`session/new` requires an absolute, existing `cwd`, builds a new agent
session for it, and returns a unique session id. The **ACP session id is the
underlying persisted conversation id** (the memory-DB session id recorded in
`agentSession.AgentSessionID()`), so anything that can name a yaah session
can also re-attach to it in a fresh process.

Every call creates a separate conversation; two calls return two distinct
ids, and prompts on different sessions may run concurrently. A missing or
relative cwd is rejected with invalid params (`-32602`).

### session/load and where the state lives

`yaah acp-serve` is disposable: the client can kill and restart it at any
time, then call `session/load` to re-attach. Persisted state lives in the
existing SQLite session store — `~/.yaah/state.db` via `internal/memory` —
which every yaah process already writes through (`SessionPersister` records
each user/assistant/tool message). **No ACP-specific state file exists**:
the ACP session id *is* the persisted memory-DB session id, so a load needs
nothing beyond the id the client already holds.

Load flow (`internal/acp/server.go` `handleSessionLoad`):

1. The client sends `{sessionId, cwd, mcpServers}`. The cwd is required,
   absolute, and must match the session's original cwd when the id is
   already live in this process.
2. The underlying session is built bound to that id — the same
   `restoreSession` path `yaah --resume <id>` uses (`cmd/yaah/resume.go`),
   which replays stored messages into the conversation and rebuilds the
   system prompt. An id the store never issued fails the build and the
   request is rejected with invalid params. Sessions yaah created through
   other frontends (REPL, TUI) are resumable too; they are yaah's own
   sessions in the same store.
3. Before responding, the agent **streams the conversation back** as
   `session/update` notifications: each persisted user message replays as
   `user_message_chunk`, each persisted assistant text as
   `agent_message_chunk`, and each persisted tool call as a `tool_call`
   (with its provider `toolCallId`, kind, and raw input). Conversation
   internals are skipped: system rows (compaction summaries and
   post-interrupt continuations), tool results, the assistant
   tool-call-only rows whose text is the synthetic `[tool:…]` summary, and
   user-role rows that carry agent-internal guidance rather than user
   input (`[STEER] ` steering/loop-detection nudges, `SUPERVISOR: `
   checkpoint-recovery guidance, `CONFLICT: ` sub-agent conflict reports,
   `[BACKGROUND <role>]` background-job result deliveries).
4. The request then answers with `{}` (empty object), per spec.

Reloading the **already-established id** replays the live conversation
again and answers `{}` (load always replays). Loading a **different id** is
allowed: sessions are independent and may coexist in one process.

Clients that hold old ids must tolerate both outcomes: ids the store
never issued (e.g. after `~/.yaah/state.db` was deleted) are rejected
with an invalid params error naming the id, while a state store that
failed to open surfaces as its own invalid params error (the message
describes the store problem; it does not name the id).

## Approvals and questions

- **Tool approvals** bridge to the client via `session/request_permission`
  (`internal/acp/approval.go`). The pending call is announced as a
  `tool_call` first and the permission request carries the same provider
  `toolCallId`; after approval the execution updates that row instead of
  creating a second one. A timeout or a rejection denies the call and
  closes the row as `failed`.
- **Question-tool prompts** are surfaced as status text and auto-answered
  with the first option when `AutoAnswerQuestions` (default on), rather
  than an ACP elicitation round-trip.

## Client setup (Zed)

`~/.config/zed/settings.json` — Zed wraps custom agents in a top-level
`agent_servers` entry (per
[zed.dev/docs/ai/external-agents](https://zed.dev/docs/ai/external-agents#custom-agents)):

```json
{
  "agent_servers": {
    "yaah": {
      "type": "custom",
      "command": "/path/to/yaah",
      "args": ["acp-serve"],
      "env": {}
    }
  }
}
```

Or the generic stdio client loop (any language; one JSON object per line):

```jsonc
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientInfo":{"name":"my-client","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo","mcpServers":[]}}
{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"<id>","prompt":[{"type":"text","text":"hi"}]}}
// later, after restarting the agent process:
{"jsonrpc":"2.0","id":4,"method":"session/load","params":{"sessionId":"<id>","cwd":"/repo","mcpServers":[]}}
```

## Tier 3: deliberately unsupported

- **`authenticate`** — `authMethods` is empty; authenticate requests are
  rejected with method-not-found.
- **HTTP/SSE MCP transports** — not advertised (`mcpCapabilities` omits
  them). Client-declared **stdio** MCP servers are started and merged with
  the config-declared set for that session; their tools join the session's
  tool registry.
- **Context/content blocks** — only text content is produced and replayed;
  image and audio blocks are not modeled.
- **`fs/` and `terminal/` client capabilities** — a read-only terminal and
  file system access that ACP allows agents to request from clients are
  not used; yaah's tools act in the session cwd only.
- **Mode switching** — `session/set_mode` is acknowledged but does nothing;
  modes are not part of the session result.
- **Compaction replay** — yaah does not advertise compaction support, so
  clients are not promised compaction `session/update` replay on load.
