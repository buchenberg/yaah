# Features

yaah's runtime capabilities: the terminal interfaces, persistence, MCP,
built-in tools, observability, approval, the middleware pipeline, and
provider support. For the sub-agent team see [sub-agents.md](./sub-agents.md);
for configuration see [configuration.md](./configuration.md).

## Rich terminal interface

`yaah tui` launches a full Bubble Tea terminal UI with:

- Streaming token-by-token responses as I think
- Collapsible reasoning/thinking blocks (DeepSeek R1, Claude) — toggle with
  `ctrl+t`
- Inline tool call cards showing what I'm running, how long it took, and the
  result
- Sub-agent dispatch cards with role, duration, and error status
- A command palette (`:`) with `:help`, `:clear`, `:compact`, `:banner`,
  `:model`, `:steer`, `:copyview`, `:quit`, and `:stop`
- Search (`/`) through response history
- `ctrl+y` to copy the last response
- Mouse wheel, page up/down, home/end navigation
- Footer bar with the most important keybindings
- Todo list sidebar tracking in-flight tasks
- Input history and expandable multi-line input

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
yaah session list
yaah session show <id>
yaah --resume <id> "pick up where we left off"
```

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
| `spawn_subagent`, `list_subagents` | Team management |
| `supervised_task`, `supervisor` | Checkpointed sub-agent runs: automatic rollback+retry, or interactive review sessions (continue/rollback/fork/choose/review_diff/accept/abort) |

## OpenTelemetry observability

Enable tracing to see every LLM call, tool execution, inner loop, and
sub-agent dispatch as spans:

```yaml
observability:
  otel:
    enabled: true
```

Token attribution is tracked per-turn. Fire up SigNoz (https://signoz.io/docs/install/docker/),
visit http://localhost:8080, and watch me work. Full guide at
[`docs/otel-setup.md`](./otel-setup.md).

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

## Middleware pipeline

I run a configurable middleware pipeline on every agent turn:

| Middleware | On by default | What it does |
|---|---|---|
| `steer` | ✓ | High-priority mid-turn input before the next LLM call |
| `followup` | ✓ | Queued between-turn messages, coalesced into one |
| `compaction` | ✓ | LLM-powered context summarization when the window fills up |
| `soft_prune` | ✓ | Elides stale tool-output content without an LLM call |
| `approval` | ✓ | Gates dangerous tools per your approval mode |
| `loop_detection` | ✓ | Halts stuck loops via tool-call-chain hashing |
| `staleness` | ✓ | Annotates sub-agent results when orchestrator context shifted mid-flight |
| `permission` | — | Path-pattern rules to allow/deny tools by file path |
| `tool_concurrency` | ✓ | Caps concurrent tool goroutines |
| `shepherd_trace` | ✓ | Records every tool call as a durable, inspectable execution trace |
| `sub_agent` | — | Enforces sub-agent depth limits |
| `prompt_caching` | — | Anthropic cache-control breakpoints |

You can reorder, disable, or enable middleware in your config.

## Execution traces

When `shepherd_trace` is active (on by default), every tool call and turn
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
that led to the failure. The `subagent_trace` tool lets the orchestrator
query sub-agent traces proactively during a run.

### Configuration

```yaml
agents:
  default:
    shepherd_trace_dir: ~/.yaah/traces   # default, optional
```

No separate enable flag — tracing is active whenever `shepherd_trace` is in
the middleware pipeline (on by default). Set `middleware.disabled: [shepherd_trace]`
to turn it off.

## Provider flexibility

I work with any OpenAI-compatible API, plus native Anthropic Messages API
support (with prompt caching and extended thinking). Configure as many
providers as you want — DeepSeek, OpenAI, Anthropic, Ollama, llama.cpp,
OpenRouter. The really cool people run open-weight models locally, but I
won't stop you from doing whatever. Set a fallback provider for when the
primary one returns a transient error (429, 503). Sub-agents can use
different providers and models than the main loop.
