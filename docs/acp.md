# ACP (Agent Client Protocol)

How `yaah acp serve` exposes yaah as an Agent Client Protocol v1 agent over
stdio: the session lifecycle, where resumed sessions live, and what is
deliberately out of scope (Tier 3).

> The former name `yaah acp-serve` still works and prints a deprecation
> notice; `yaah acp serve` is the canonical spelling.

- Spec: <https://agentclientprotocol.com> (this server speaks v1)
- Implementation: `internal/acp` (protocol + server), `cmd/yaah/acp_cmd.go`
  (composition), `internal/acp/view.go` (event → update translation)

## Overview

`yaah acp serve` runs yaah as an ACP **agent** (server side) speaking
newline-delimited JSON-RPC 2.0 on stdin/stdout. All diagnostics go to stderr
so stdout stays protocol-clean.

```
ACP client (Zed, ...)                    yaah acp serve
─────────────────────                    ───────────────
 ── initialize ─────────────────────────▶ version + agentCapabilities
 ── session/new {cwd} ──────────────────▶ agentSession built; ACP session id
 ── session/load {sessionId} ──────────▶ conversation restored from state.db
 ── session/resume {sessionId} ────────▶ conversation re-attached, no replay
 ── session/list {cwd?, cursor?} ──────▶ persisted sessions, paged
 ── session/prompt ─────────────────────▶ session/update stream, then stopReason
 ── session/close / session/delete ────▶ cancel + free / remove from store
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
| `initialize` | supported | advertises `agentCapabilities.loadSession: true` plus `sessionCapabilities` for `resume`, `list`, `close`, `delete`, and a static catalog of agent-typed `authMethods` (see [Authentication and credentials](#authentication-and-credentials)) |
| `authenticate` | supported | validates the named auth method and answers `{}`; no credential check, no network call |
| `logout` | supported | removes all stored OAuth provider tokens; advertised only when wired (`agentCapabilities.auth.logout`) |
| `session/new` | supported | requires the client's absolute cwd; every call creates a distinct session; advertises `configOptions` (mode always, model when a catalog is wired) |
| `session/load` | supported | resume + replay; re-advertises `configOptions` like `session/new`; see below |
| `session/resume` | supported | re-attach to a persisted session without replaying history |
| `session/list` | supported | persisted sessions as `SessionInfo` pages (cwd filter, opaque cursor) |
| `session/close` | supported | cancels in-flight work, frees the session; the id stays loadable in a later process |
| `session/delete` | supported | removes the conversation from the store; unknown/deleted ids succeed silently |
| `session/prompt` | supported | streams updates, then answers with a `stopReason` |
| `session/cancel` | supported | notification; cancels the in-flight turn |
| `session/set_mode` | supported alias | switches between `build` (full tools) and `plan` (read-only tool set + planning directive); unknown ids are invalid params and the change is pushed as `config_option_update` |
| `session/set_config_option` | supported | the mode picker (`configId: "mode"` with `category: "mode"`) and model picker (`configId: "model"`); validates the value, applies it to subsequent turns, emits `config_option_update` for mode changes, and echoes the recomputed `configOptions` set |
| `session/update` | supported | variants below |
| `session/request_permission` | supported | tool approvals delegated to the client; all four option kinds (`allow_once`/`allow_always`/`reject_once`/`reject_always`) are offered, and an always-decision is remembered for the rest of the session |

`session/update` variants: `agent_message_chunk`, `agent_thought_chunk`,
`tool_call`, `tool_call_update`, `plan`, `usage_update`,
`available_commands_update`, and `user_message_chunk` (replay only, on
`session/load`; `session/resume` re-attaches without any history replay). `usage_update` streams the
context occupancy during the turn and once more at turn end with the
engine's cumulative token tally, finish reason, and response model in
its `_meta` (cumulative `cost` is omitted — no pricing source).

`stopReason` values: `end_turn` (the model finished), `max_turn_requests`
(a request budget ended the turn — the soft `max_turns` cap stripped tools
before the model finished, or `max_iterations` was exhausted), `cancelled`,
and `refusal` (the turn failed with an error). Clients should treat
`max_turn_requests` as continuable: sending another `session/prompt`
resumes with a fresh budget.

`session_info_update` is sent when a session's title transitions from
empty (the first user message that derives one). It carries `title` and
`updatedAt` (RFC3339 UTC — the same values `session/list` reads back
from the store); not capability-gated in v1 — clients that ignore it
lose nothing.

## Command palette

After `session/new`, `session/load`, and `session/resume`, the agent sends an
`available_commands_update` containing the skills discovered for the session
cwd (project `.agents/skills`, `~/.yaah/skills`, and `~/.agents/skills`).
Each command carries the skill name, its description, and an argument hint.
ACP sends a selected command back as ordinary prompt text (`/name args`); the
server translates a known skill command into an explicit `skill` tool load
instruction before the model turn starts. Unknown slash text passes through
unchanged.

## Authentication

`initialize` advertises the static agent-typed auth methods. When the client
advertises `clientCapabilities.auth.terminal`, it also advertises a terminal
method with args `["login"]`, so the client can launch `yaah login`
interactively and reconnect. Terminal methods are never accepted by
`authenticate`. When the server is wired with a logout callback, it advertises
`agentCapabilities.auth.logout` and `logout` removes all stored OAuth provider
tokens.

## Session lifecycle

### session/new

`session/new` requires an absolute, existing `cwd`, builds a new agent
session for it, and returns a unique session id. The **ACP session id is the
underlying persisted conversation id** (the memory-DB session id recorded in
`agentSession.AgentSessionID()`), so anything that can name a yaah session
can also re-attach to it in a fresh process.

Every call creates a separate conversation; two calls return two distinct
ids, and prompts on different sessions may run concurrently. A missing or
relative cwd is rejected with invalid params (`-32602`); a second prompt on
a session whose turn is still in flight is rejected busy (`-32002`),
as is a `session/load` for a live id while its prompt runs — both retry
after the turn ends. New sessions start in **build** mode and advertise the
mode selector as `configOptions` (`id: "mode"`, `category: "mode"`, values
`build`/`plan`). This server is greenfield and does not emit the legacy
`modes` field; clients must support config options. `session/set_mode` remains
an accepted alias and pushes `config_option_update`. Plan mode withholds the
mutating tool set and injects a planning directive; an in-flight turn keeps
the mode it started with. Mode is not persisted, so a fresh process defaults
to build on `load`/`resume`; a live load/re-resume keeps the attachment's
current mode.

### session/load and where the state lives

`yaah acp serve` is disposable: the client can kill and restart it at any
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
4. The request then answers with the session result: `configOptions` (mode
   always, model when a catalog is wired) and `_meta` when the session has a
   derived world posture.

Reloading the **already-established id** replays the live conversation
again and returns the same result shape (load always replays). Loading a
**different id** is allowed: sessions are independent and may coexist in
one process.

Clients that hold old ids must tolerate both outcomes: ids the store
never issued (e.g. after `~/.yaah/state.db` was deleted) are rejected
with an invalid params error naming the id, while a state store that
failed to open surfaces as its own invalid params error (the message
describes the store problem; it does not name the id).

### session/resume

`session/resume` (`sessionCapabilities.resume`) re-attaches to a persisted
conversation **without replaying history**: no `session/update`
notifications precede the response, which carries the same `configOptions`
set as session/new. It shares `session/load`'s attachment
machinery — same required absolute cwd, same live-id checks (idempotent
mode result when already attached, busy error while a prompt runs), same
store-backed rebuild for ids that are
not live in this process, so foreign ids are rejected with invalid
params. A client that wants to render history uses `session/load`; one
that only continues a conversation uses `session/resume`.

### session/list

`session/list` (`sessionCapabilities.list`) pages through the persisted
sessions so a client can offer a picker without loading anything:

- Params: `{cwd?, cursor?}`. The optional `cwd` filters sessions by
  exactly that working directory (must be absolute; a directory the
  agent has never seen is not an error — it simply matches nothing).
- Result: `{sessions: [SessionInfo…], nextCursor?}`. Each entry carries
  `sessionId`, `cwd`, a human-readable `title` (yaah derives it from the
  session's first user message; absent for sessions that have none), and
  an ISO-8601 `updatedAt` (the last persisted activity; the creation
  time for sessions with no messages).
- Pagination is agent-controlled (50 rows per page) with an opaque
  cursor: a missing `nextCursor` marks the end. An unparsable cursor is
  rejected with invalid params rather than silently restarting the list.
- Each entry also carries `_meta: {"messageCount": N}` — the number of
  persisted messages — so a client can show session size without loading
  anything. `_meta` is omitted for a session with no messages rather than
  reporting a zero.

`yaah sessions list` and `yaah sessions delete <id>` run the same
queries against the same store (`internal/memory.ListSessionsPage`,
`DeleteSession`), so CLI and ACP clients see one session universe. Both
sides reach it through the engine-level session registry
(`internal/sessions`), which owns cwd canonicalization (`internal/cwdpath`),
cursor chasing, and the invalid-cwd / invalid-cursor / not-found mapping; ACP
session/list and session/delete are thin adapters over `Registry.Page` and
`Registry.Delete`, and the web picker and `yaah --resume` picker call the same
methods. `internal/acp` therefore does not depend on `internal/sessions` or
`internal/memory`: the registry is injected as callbacks.

### session/close and session/delete

`session/close` (`sessionCapabilities.close`) frees a session: the
in-flight turn is cancelled exactly as if `session/cancel` had been sent
(the prompt resolves with `stopReason: "cancelled"`), the prompt
goroutine is joined, and the underlying agent session is closed. The id
is no longer usable in this process, but nothing is erased — a later
`session/load` in a fresh process re-attaches. Only live sessions are
closable; unknown or already-freed ids get invalid params.

`session/delete` (`sessionCapabilities.delete`) removes the conversation
from the store (messages included), so `session/list` stops reporting
it. A still-live session is torn down first (same contract as close).
Deleting an already-deleted or never-created id succeeds silently — the
store no longer holding the id is exactly the requested end state; real
store failures surface as internal errors.

## Authentication and credentials

`initialize` advertises a **static** catalog of agent-typed `authMethods`
(`internal/acp/auth.go`). The list is compiled in and is never derived from
config, the environment, or the provider list, so a client on a cold
machine — empty environment, fresh HOME, no API keys — still sees it.

Each method carries only `id`, `name`, and a `description`; none carries a
`type`. The schema's `terminal` variant applies only when the client
enabled `clientCapabilities.auth.terminal`. The static catalog below is
agent-typed (a missing `type` means exactly that); when the client advertises
terminal auth, yaah adds a `terminal-login` method (`type: "terminal"`,
args `["login"]`) so the client can launch `yaah login` interactively. The
`authenticate` method only accepts the agent-typed ids.

| `methodId` | What it names |
|---|---|
| `openai-api-key` | An OpenAI-compatible key referenced from `~/.yaah/config.yaml` as `api_key: ${OPENAI_API_KEY}`; the default scaffold wires `${DEEPSEEK_API_KEY}` the same way |
| `anthropic-api-key` | A native Anthropic key referenced as `api_key: ${ANTHROPIC_API_KEY}` |
| `providers-config` | Whatever is already in `~/.yaah/config.yaml` under `providers.<name>.api_key`, or an OAuth token stored by one of yaah's three login entry points: the `yaah login [provider]` CLI command, the REPL `/login` slash command, or the web UI's login action (`POST /api/action` with `{"action":"login"}`) |

### Credential resolution and precedence (yaah-side, not ACP-side)

For a given provider entry in `~/.yaah/config.yaml` (or
`$YAAH_HOME/config.yaml`):

1. `auth: oauth` wins: the stored OAuth token is used as the bearer
   credential and `api_key` is ignored. The token comes from any of yaah's
   three login entry points — the `yaah login [provider]` CLI command
   (`cmd/yaah/login.go`), the REPL `/login` slash command
   (`internal/repl/slash.go`), or the web UI's login action
   (`POST /api/action` with `{"action":"login"}`, `cmd/yaah/web.go`).
   All three run the same OAuth 2.0 device flow and write the same
   per-provider token file (`oauth-<provider>.json` under the config
   directory); `yaah logout [provider]` / `/logout` removes it. There is
   no fallback from a missing OAuth token to `api_key`: an `auth: oauth`
   provider with no stored token resolves to a stub whose calls fail with
   a "run `yaah login`" error (`makeOAuthProvider`,
   `cmd/yaah/provider_resolve.go`).
2. Otherwise `api_key` is used after `${VAR}` substitution. Only the
   placeholders the config file itself references are expanded —
   `${OPENAI_API_KEY}`, `${ANTHROPIC_API_KEY}`, `${DEEPSEEK_API_KEY}`, and
   so on — because `config.Resolve` substitutes `${VAR}` in `api_key`,
   `base_url`, and the OAuth fields. There is no implicit fallback to a
   bare `OPENAI_API_KEY` environment variable.
3. Native Anthropic providers add one negative rule: yaah disables the
   Anthropic SDK's environment credential chain
   (`option.WithoutEnvironmentDefaults()`), so a stray `ANTHROPIC_AUTH_TOKEN`
   is never sent alongside yaah's own key to whatever endpoint is
   configured.

### What `authenticate` does and does not validate

It validates the method **id** against the advertised catalog and nothing
else. It does not check that a credential is present, does not store one,
and makes no network call: a client may declare its method before
provisioning a key. A declared id answers with an empty result object
(`{}`, the schema's `AuthenticateResponse` has only an optional `_meta`);
an unknown id is rejected with invalid params (`-32602`).

### What ACP does not surface

ACP has no channel for yaah's security posture, and no client can change
it. The approval bridge (`session/request_permission` plus yaah's own
`approval` / `mcp_approval` policy), the tool-kind classification that
decides which calls need approval, and the sandbox/egress layers are
configured yaah-side (`~/.yaah/config.yaml`, CLI flags). A client can
approve or reject an individual call through
`session/request_permission`; it cannot loosen or tighten the policy that
produced the request.

## Approvals and questions

- **Tool approvals** bridge to the client via `session/request_permission`
  (`internal/acp/approval.go`). When the model supplies a provider call id,
  the pending call is announced as a `tool_call` first and the permission
  request carries the same `toolCallId`; after approval the execution
  updates that row instead of creating a second one. Models that omit the
  call id prompt under a synthetic id and produce no `tool_call` row. A
  timeout or a rejection denies the call and, when a row was announced,
  closes it as `failed`.
- **Question-tool prompts** route through `elicitation/create` (form
  mode) when the client advertised the `elicitation.form` capability
  (`internal/acp/elicitation.go`): each question becomes a form whose
  `answer` property is a string (string-array for `multiple`) enum of the
  option labels, and the chosen label(s) reach the agent. A decline,
  cancel, or timeout records an empty answer — the auto-answer default
  never overrides a user who was actually offered the question. Without
  the capability the legacy path stands: status prose, auto-answered
  with the first option when `AutoAnswerQuestions` (default on).

### Diff content and locations

File-modifying tool calls (`write`, `edit`) carry ACP `diff` content
blocks plus jump-to-file `locations` on both the pending `tool_call` and
the completing `tool_call_update` (`internal/acp/toolcontent.go`): a
`write` yields an `oldText`-less diff for its new file, and an `edit`
yields one diff per replacement (`edits[]` entries included). Rendering
clients (Zed) show a real file diff instead of raw JSON arguments.
`patch` and `replace` keep their plain result text — replace is
multi-file and patch is hunk-based, so neither maps to an honest whole-file
diff.

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
      "args": ["acp", "serve"],
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
{"jsonrpc":"2.0","id":4,"method":"session/resume","params":{"sessionId":"<id>","cwd":"/repo","mcpServers":[]}}
// or, to render the history:
{"jsonrpc":"2.0","id":5,"method":"session/load","params":{"sessionId":"<id>","cwd":"/repo","mcpServers":[]}}
// pick a previous conversation:
{"jsonrpc":"2.0","id":6,"method":"session/list","params":{}}
// and later, to close the editor's session window:
{"jsonrpc":"2.0","id":7,"method":"session/close","params":{"sessionId":"<id>"}}
```

## Tier 3: deliberately unsupported

- **HTTP/SSE MCP transports** — not advertised (`mcpCapabilities` omits
  them). Client-declared **stdio** MCP servers are started and merged with
  the config-declared set for that session; their tools join the session's
  tool registry.
- **Context/content blocks** — text, embedded resources, and resource links
  are accepted on prompts; image and audio blocks are rejected with
  invalid params because yaah's provider message model has no multimodal
  content parts. Unrecognized block types are preserved as inline
  `[unsupported content block: …]` text for forward compatibility. Outbound
  tool-call content is text plus `diff` blocks for `write`/`edit` tool calls.
- **`fs/` and `terminal/` client capabilities** — when the client advertises
  `clientCapabilities.fs.readTextFile` / `writeTextFile`, text reads and
  writes go through the client, so open unsaved editor buffers are visible.
  When it advertises `clientCapabilities.terminal`, the user-facing `bash`
  and `powershell` tools run in a client terminal; the tool call gets a
  `terminal` content block with the terminal id, and every terminal is
  released when the command ends or the turn is cancelled. Internal commands
  (`rg`, `git`, `go test`, and similar plumbing) stay local, as do sessions on
  an isolated/container workspace where the client cannot see the files.
  Without those capabilities, yaah's local tools act in the session cwd
  unchanged. Client writes are text-only: yaah creates parent directories and
  restores an existing mode when possible, but the editor owns atomicity and
  the buffer itself. Binary writes fall back to the local workspace.
- **`additionalDirectories`** — not advertised; sessions act in the cwd
  the lifecycle request named.
- **Compaction replay** — with the client advertising
  `clientCapabilities.session.compaction`, live compaction is sent as
  ID-addressed `compaction_update` /
  `compaction_summary_chunk` updates on `session/update`; without it,
  compaction degrades to plain text message chunks. However, loaded sessions
  still do not replay compaction cards — the persisted
  summary is a system prompt row that replay skips, so only live compaction
  is structured (a persisted compaction record would be needed to replay).

## ACP v2 tracking

This server targets ACP **v1**. The draft v2 spec removes or reshapes several
surfaces used here: `fs/*`, `terminal/*`, `session/set_mode`,
`session/load`, and `tool_call` (along with the v1 diff content blocks,
locations, elicitation, and usage-cost fields). Keep the v1 implementation
authoritative for current Zed/VS Code clients; a v2 adapter would need new
surfaces rather than carrying these methods forward unchanged.
