# Features

yaah's runtime capabilities: the terminal interfaces, persistence, MCP,
built-in tools, observability, approval, the middleware pipeline, and
provider support. For the sub-agent team see [sub-agents.md](./sub-agents.md);
for configuration see [configuration.md](./configuration.md).

## Rich terminal interface

`yaah tui` launches a full-screen tview-based terminal UI with:

- Streaming token-by-token responses as I think
- Collapsible reasoning/thinking blocks — toggle with `ctrl+r`
- Collapsible tool call cards showing what I'm running, how long it took, and
  the result (`ctrl+t` toggles all tool blocks)
- Sub-agent dispatch cards with role, duration, and error status
- A command palette (`ctrl+p` or `:`) with `:help`, `:clear`, `:compact`,
  `:banner`, `:model`, `:search`, `:steer`, `:verbose`, `:quit`, and `:stop`
- Search (`:search <query>`) through response history
- Paned layout: conversation, info pane (session/context/MCP/config), todo
  sidebar, and background sub-agent jobs
- Mouse wheel and keyboard navigation throughout
- Input history and expandable multi-line input

See [architecture.md](./architecture.md) for how the UI consumes typed agent
events, and `internal/tui/components/` for the component inventory.

## Interactive REPL

When you just want a quick conversation without the full TUI, `yaah`
(no args) starts a readline REPL with slash commands:

| Command | Action |
|---|---|
| `/exit`, `/quit` | Say goodbye |
| `/clear` | Clear the screen |
| `/compact` | Summarize old context to free up space |
| `/help`, `/?` | Show available commands |

Arrow keys navigate history. History persists at `~/.yaah/history`.

## Persistent memory

I remember things across sessions using SQLite with full-text search (FTS5)
and optional **semantic vector search** via cosine similarity over embeddings:

```bash
yaah memory add "user prefers dark mode" --tags '["ui"]'
yaah memory search "dark mode"
```

During conversations, I use `memory_search`, `memory_add`, `memory_update`,
and `memory_delete` tools. I save session summaries so you can pick up where
you left off.

### Semantic search

When an embedding provider is configured (see
[configuration.md](./configuration.md#embeddings-reference)), each memory
entry is embedded via a local or cloud `/v1/embeddings` endpoint. Searches
use cosine similarity to find semantically related facts even when keywords
don't overlap — "database connection management" surfaces "Postgres
connection pooling uses PgBouncer." Results include similarity scores.
FTS5 is always available as a fallback.

## Session persistence and resume

Every message is written to SQLite in real time. Sessions survive crashes
and process restarts:

```bash
yaah --resume <id> "pick up where we left off"
```

Session transcripts are stored in SQLite at `~/.yaah/state.db` (or
`$YAAH_HOME/state.db`); inspect trace-level detail with
`yaah shepherd-trace list` / `yaah shepherd-trace show <id>`.

## MCP servers

I speak Model Context Protocol over stdio and HTTP. MCP servers give me
more tools — databases, APIs, custom workflows:

```bash
yaah mcp list
yaah mcp add <name> <command> [args...]
yaah mcp add <name> --url http://localhost:3000
yaah mcp remove <name>
```

## MCP tool server (agent-to-agent)

I can also *be* an MCP server. `yaah serve` exposes my engine as three MCP
tools — `prompt`, `traces`, and `status` — so other agents (Kilo, Claude
Code, benchmarking harnesses) can drive multi-turn conversations and query
in-process OpenTelemetry traces programmatically:

```bash
yaah serve                        # stdio (newline-delimited or Content-Length)
yaah serve --http 127.0.0.1:7333  # HTTP+SSE (hot-reload dev loop)
```

The server auto-detects framing, lazily initializes the agent session for
instant handshakes, and auto-registers stale sessions so server restarts
are transparent to the client. See the [MCP dev loop](../README.md#mcp-dev-loop-hot-reload)
section in the README.

## Skills

I load `.agents/skills/` (project) and `~/.agents/skills/` (user) at
startup. Skills are `SKILL.md` files — YAML frontmatter with a markdown
body. I use the `skill` tool to inject a skill's instructions into my
context when the task matches:

```bash
yaah skill list
yaah skill show <name>
```

## Plans

I can create and manage structured plans via the `plan` tool. Plans live as
`PLAN.md` files in `.agents/plans/` with a workflow: `draft → approved →
in_progress → completed`. Great for multi-step tasks that need sign-off.

## Todo lists

I track in-flight work with the `todowrite` tool. The TUI shows them in a
sidebar. Use `/compact` or `:compact` when the list gets long.

## Tool belt (my built-in tools)

Here's what I can reach for directly:

| Tool | What it does |
|---|---|
| `read`, `write`, `edit`, `delete`, `replace` | File operations |
| `grep`, `glob`, `ls` | Search and navigation |
| `bash`, `powershell` | Shell commands — I pick the right one for your OS |
| `git` | Version control |
| `http`, `webfetch` | Web requests and scraping |
| `go_outline` | Parse Go source structure — this one comes in handy |
| `patch`, `sed` | Apply unified diffs and stream-edit text |
| `go_refactor`, `go_test`, `go_mod`, `bisect` | Go refactor, test, module, and git-bisect helpers |
| `diff`, `staticcheck` | Diff output and static analysis |
| `json_query` | Read/write/delete JSON values by path |
| `calculate` | Math expressions — I'm a language model, give me a break |
| `file_info` | File metadata without reading |
| `question` | Ask you for clarification |
| `background_process` | Manage long-running processes |
| `skill` | Load skill instructions on demand |
| `plan` | Create and manage PLAN.md files |
| `todowrite` | Track in-flight tasks |
| `memory_search`, `memory_add`, `memory_update`, `memory_delete`, `memory_search_sessions` | Long-term memory |
| `delegate`, `list_subagents` | Team management |
| `supervisor` | Supervision of running sub-agents: scope inspection (`list_scopes`, `status`), guidance injection, and force-stop (`halt`) |

## OpenTelemetry observability

Enable tracing to see every LLM call, tool execution, inner loop, and
sub-agent dispatch as spans:

```yaml
observability:
  otel:
    enabled: true
```

Token attribution is tracked per-turn. Fire up SigNoz (https://signoz.io/docs/install/docker/),
visit http://localhost:8080, and watch me work. Full observability
configuration is in
[`docs/configuration.md`](./configuration.md).

## Hook events

Set `hooks.dir` to emit structured JSONL events — `session.start`,
`session.end`, `turn.start`, `tool.start`, `tool.end`, `conflict.detect` —
with timestamps, model info, tool results, and durations. Basically I narrate my own life. Useful for external integrations, audit
trails, and keeping the collective honest.

## Approval gates

Three modes for dangerous tools (write, delete, execute, git push):

```bash
yaah --approval ask "deploy"    # prompt for each dangerous call (default)
yaah --approval allow "deploy"  # auto-approve (headless / CI)
yaah --approval deny "deploy"   # block all dangerous calls
```

The policy is enforced by the `approval` middleware in the pipeline: dangerous calls are stripped before dispatch and answered with a synthesized error tool result, so the model always sees a response for every call it made. Sub-agents run in `allow` mode; parent path rules (the `permission` middleware) are evaluated first.

## Workspace containment

File-accessing tools (read, write, edit, delete, grep, glob, ls, sed,
replace, patch, json_query, go_outline, go_refactor, file_info) can be
confined to a directory:

```bash
yaah --workspace . "refactor this repo"          # confine to cwd
yaah --workspace . --allow-home "..."            # also allow ~ paths
yaah --workspace . --workspace-ask "..."         # prompt before denying out-of-bounds access
```

Without `--workspace`, access is unrestricted. With it, out-of-bounds
paths are hard-rejected unless `--workspace-ask` (or
`agent.default.workspace_ask: true`) is set — then each offending path
prompts once through the approval UI (or stdin in the plain REPL), and
grants are remembered for the session. Symlinks are resolved before the
containment check, and approvals apply to the agent and its sub-agents
alike.

## Isolated workspace

Beyond confining paths, a session can run its tools inside a real Linux
container: `workspace.backend: containerd` (config file, see
`docs/configuration.md` "Workspace reference") starts each session inside
a containerd sandbox. The working tree is copied into the container at
startup and a git baseline is captured there; every file tool and shell
command runs in the container, and the host's working tree stays untouched
until the model calls `apply_out` — a dry run previews the diff since the
baseline, and the real apply is gated behind your approval.

Sub-agents dispatched with `delegate` inherit the same container, so
their file work is isolated with the parent's and reaches the host through
the same `apply_out`. Their turn-checkpoint/rollback substrate is host-git
based and stays off in isolated mode — the scope remains for supervisor
inject/halt.

Isolation costs a tool delta, named in the session prompt and in the
session record: `role`, `background_process`, and
`go_refactor` are host-only and omitted; MCP-served tools are omitted too
(they execute on the host), and `skill`/`plan` stay registered but
read-only. `doctor` reports the workspace check when the stanza is
present, and `docs/isolated-workspace-live-testing.md` covers the image
contract and the end-to-end live test.

## Middleware pipeline

Every agent turn runs through a configurable middleware pipeline. The
orchestrator registers 11 middleware by name — 9 on by default:

| Middleware | On by default | What it does |
|---|---|---|
| `steer` | ✓ | High-priority mid-turn input before the next LLM call |
| `followup` | ✓ | Queued between-turn messages, coalesced into one |
| `compaction` | ✓ | LLM-powered context summarization when the window fills up |
| `soft_prune` | ✓ | Elides stale tool-output content without an LLM call |
| `approval` | ✓ | Gates dangerous tools per your approval mode |
| `inline_limit` | ✓ | Caps tool calls per turn; dropped calls get synthesized results |
| `conflict_detect` | ✓ | Flags files touched by multiple sub-agents after each batch |
| `loop_detection` | ✓ | Halts stuck loops via tool-call-chain hashing |
| `tool_concurrency` | ✓ | Caps concurrent tool goroutines |
| `permission` | — | Path-pattern rules to allow/deny tools by file path |
| `prompt_caching` | — | Anthropic cache-control breakpoints |

Sub-agent loops run their own curated pipeline: `tool_concurrency` and
`shepherd_trace` by default, plus `permission` when the parent passes path
rules (denied calls are filtered before execution).

You can reorder, disable, or enable middleware in your config.

## Supervision lifecycle

The `supervisor` tool (see the tool belt above) watches
sub-agents under kernel-backed supervision: scope inspection, guidance
injection, and force-stop. The lifecycle around those runs is durable — the
trace is the registry —
and this section is the operational half: what happens at startup, and
what to do with the leftovers a crash can leave.

### Startup recovery

When a trace dir is configured (`agents.default.shepherd_trace_dir`),
startup rebuilds the supervision state from the durable trace instead of
forgetting it: the scope tree, checkpoints, and sealed outputs come back,
and isolated worktree children are re-adopted onto their recorded
addresses — a crash mid-run leaves the work recoverable, not stranded.
The recovery report prints four buckets:

| Bucket | Meaning | What to do |
|---|---|---|
| `adopted` | A worktree child re-adopted — its work is live again | Nothing; it is back |
| `orphaned` | A terminal scope's worktree still on disk (the crash window between the terminal record and the teardown) — it may hold the only copy of unmerged work | Inspect it; `yaah shepherd prune` when you are sure |
| `missing` | An active child whose worktree is gone | Nothing to recover; the crash lost its work |
| `unadopted` | A recorded path that is not a registered worktree of its repository (a squatter, or a moved repo) | Nothing — yaah never adopts or deletes what it cannot verify it owns |

Nothing is ever deleted automatically. The orphan buckets are warn-only
by design.

### The shepherd command

```bash
yaah shepherd          # list the recovery report's worktree leftovers
yaah shepherd prune <worktree-path>   # delete exactly one orphaned worktree
```

Normal lifecycle teardown is automatic — settling or discarding a
variant destroys the worktree its scope owns — so `prune` exists for the
one case nothing else covers: an *orphan* the crash left behind. It
deletes by exact path, user-invoked only, and it verifies ownership (the
git worktree registry and the directory's own repository link) before
deleting. An adopted, missing, or unadopted path refuses by name; an
unlisted path is not on the report at all.

### Doctor

`yaah doctor` runs the same recovery scan as a check: a trace dir that
fails to recover fails the check, and any non-empty leftover bucket
(orphaned, missing, unadopted) warns with the same bucket vocabulary.

## Execution traces

When a trace store is configured (sub-agent pipelines carry `shepherd_trace`
by default; the orchestrator initializes the session-wide store via
`scope_init.go`), every tool call and turn
boundary is recorded to a content-addressed append-only trace store at
`~/.yaah/traces/trace.sqlite`. Each record is cryptographically hashed and
causally chained, giving you a tamper-evident, replayable execution history.

### Inspecting traces

```bash
yaah shepherd-trace list              # show all sessions with fact counts
yaah shepherd-trace show <session>    # show tool-call sequence with args and status
yaah shepherd-trace show --latest     # show the most recent session
yaah shepherd-trace profile <session> # aggregate: turns, tokens, tool stats, success rates
```

Profile output:

```
Session:  sess-xxx
Turns:    3        Tools:  4
Tokens:   12.3k prompt  2.2k completion
Success:  3/4 (75%)

T1       continued
  ls    {"path":"."} 3ms  ok
T2       continued
  edit  {"filePath":"src/foo.go"...} 2ms  ok
T3       completed              10.5k + 1.8k tokens

TOOL   CALLS  SUCCESS  AVG DUR
edit   1      100%     2ms
ls     1      100%     3ms
```

### Sub-agent traces

Sub-agents get their own trace owners (identified as `sub-<role>-sess-*-<id>`)
in the same store. When a sub-agent fails, the orchestrator automatically
inspects its trace — so retry prompts include the exact tool-call sequence
that led to the failure.

### Configuration

```yaml
agents:
  default:
    shepherd_trace_dir: ~/.yaah/traces   # default, optional
```

No separate enable flag — tracing is active whenever a trace store is
initialized (`shepherd_trace_dir` set); sub-agent pipelines carry the
`shepherd_trace` middleware by default. Set
`middleware.disabled: [shepherd_trace]`
to turn it off.

## Provider flexibility

I work with any OpenAI-compatible API, plus native Anthropic Messages API
support (with prompt caching and extended thinking). Configure as many
providers as you want — DeepSeek, OpenAI, Anthropic, Ollama, llama.cpp,
OpenRouter. The really cool people run open-weight models locally, but I
won't stop you from doing whatever. Set a fallback provider for when the
primary one returns a transient error (429, 503). Sub-agents can use
different providers and models than the main loop.
