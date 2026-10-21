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

One agent conversation is served per process. The underlying `agentSession`
is built lazily on the first `session/new` or `session/load` so the client's
cwd takes effect before construction.

## Protocol surface

| Method | Status | Notes |
|---|---|---|
| `initialize` | supported | advertises `agentCapabilities.loadSession: true`; `authMethods` is empty so `authenticate` is rejected |
| `session/new` | supported | honors the client's cwd; repeat calls return the same id |
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

`session/new` validates the client's cwd, builds the agent session, and
returns a session id. The **ACP session id is the underlying persisted
conversation id** (the memory-DB session id recorded in
`agentSession.AgentSessionID()`), so anything that can name a yaah session
can also re-attach to it in a fresh process.

Only the first cwd wins: once a session is established, a later cwd is
logged and ignored (`cwd %q ignored: session already established`) because
chdir mid-session would silently move an established workspace. A repeat
`session/new` is idempotent — issuing a second id would invalidate the
client's existing handle while the same conversation still answers it.

### session/load and where the state lives

`yaah acp-serve` is disposable: the client can kill and restart it at any
time, then call `session/load` to re-attach. Persisted state lives in the
existing SQLite session store — `~/.yaah/state.db` via `internal/memory` —
which every yaah process already writes through (`SessionPersister` records
each user/assistant/tool message). **No ACP-specific state file exists**:
the ACP session id *is* the persisted memory-DB session id, so a load needs
nothing beyond the id the client already holds.

Load flow (`internal/acp/server.go` `handleSessionLoad`):

1. The client sends `{sessionId, cwd?, mcpServers?}`. The cwd is applied
   exactly as in `session/new` (first cwd wins).
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
   `agent_message_chunk`. Conversation internals are skipped: system rows
   (compaction summaries and post-interrupt continuations), tool results,
   the assistant tool-call-only rows whose text is the synthetic
   `[tool:…]` summary, and user-role rows that carry agent-internal
   guidance rather than user input (`[STEER] ` steering/loop-detection
   nudges, `SUPERVISOR: ` checkpoint-recovery guidance, `CONFLICT: `
   sub-agent conflict reports, `[BACKGROUND <role>]` background-job
   result deliveries). Tool calls are not replayed as
   `tool_call` updates — replay is text-level.
4. The request then answers with `{}` (empty object), per spec.

Idempotency and the per-process conversation limit:

- Reloading the **already-established id** responds immediately with `{}`
  (no replay — the conversation is live in this process).
- Loading a **different id** after one conversation is established is
  rejected with invalid params (`session %q already established; one
  conversation per process`).

Clients that hold old ids must tolerate both outcomes: ids the store
never issued (e.g. after `~/.yaah/state.db` was deleted) are rejected
with an invalid params error naming the id, while a state store that
failed to open surfaces as its own invalid params error (the message
describes the store problem; it does not name the id).

## Approvals and questions

- **Tool approvals** bridge to the client via `session/request_permission`
  (`internal/acp/approval.go`). Auto-continue is on by default: when the
  client does not answer in time the run proceeds with the safe default.
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
- **`mcpServers`** — recorded from `session/new`/`session/load` params but
  not used: yaah wires its own MCP config; client-declared MCP servers are
  not connected.
- **Context/content blocks** — only text content is produced and replayed;
  image and audio blocks are not modeled.
- **`fs/` and `terminal/` client capabilities** — a read-only terminal and
  file system access that ACP allows agents to request from clients are
  not used; yaah's tools act in the session cwd only.
- **Mode switching** — `session/set_mode` is acknowledged but does nothing;
  modes are not part of the session result.
- **Compaction replay** — yaah does not advertise compaction support, so
  clients are not promised compaction `session/update` replay on load.
