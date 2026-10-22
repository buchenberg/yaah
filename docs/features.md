# Features

yaah's runtime capabilities: interfaces, persistence, MCP, the tool belt,
observability, approval, workspace isolation, and provider support. For the
sub-agent team, see [sub-agents.md](./sub-agents.md); for configuration, see
[configuration.md](./configuration.md).

## Terminal UI

`yaah tui` launches a full-screen tview interface with:

- Streaming responses, collapsible reasoning blocks (`ctrl+r`), and
  collapsible tool cards (`ctrl+t`)
- Sub-agent dispatch cards with role, duration, and status
- A command palette (`ctrl+p`) with `:help`, `:clear`, `:compact`,
  `:banner`, `:model`, `:search`, `:steer`, `:verbose`, `:quit`, and `:stop`
- A paned layout: conversation, info pane (session, context, MCP, config),
  todo sidebar, and background sub-agent jobs
- Mouse and keyboard navigation, input history, and multi-line input

See [tui-components.md](./tui-components.md) for the component system.

## REPL

Running `yaah` with no arguments starts a readline REPL:

| Command | Action |
|---|---|
| `/exit`, `/quit` | Exit |
| `/clear` | Clear the screen |
| `/compact` | Summarize old context |
| `/help`, `/?` | Show commands |

History persists at `~/.yaah/history` (or `$YAAH_HOME/history` when `YAAH_HOME` is set).

## Web UI

`yaah web` serves a browser interface with SSE streaming, tool cards,
reasoning display, and approval/question modals. It is token-gated: startup
prints a URL containing the access token. See [web-ui.md](./web-ui.md) for
the wire protocol and current feature differences from the TUI.

## Memory

yaah stores durable facts in SQLite with FTS5 full-text search and optional
vector search:

```bash
yaah memory add "user prefers dark mode" --tags '["ui"]'
yaah memory search "dark mode"
```

The agent uses `memory_search`, `memory_add`, `memory_update`, and
`memory_delete`. When an embedding provider is configured, entries are
embedded via any `/v1/embeddings` endpoint and searched by cosine similarity;
FTS5 remains the exact-match fallback. See
[configuration.md](./configuration.md#embeddings-reference).

## Sessions and traces

Every message is written to SQLite in real time, so sessions survive crashes
and restarts:

```bash
yaah --resume <id> "pick up where we left off"

# or let yaah list recent sessions for this directory and pick one:
yaah --resume
```

The bare `--resume` form resolves through an interactive picker (REPL, TUI,
and one-shot runs): it lists the sessions recorded for the current
directory — newest first, with titles, ages, and message counts — and a
bare Enter starts a fresh session instead. The legacy space-separated
form `yaah --resume <id> "prompt"` keeps working. Non-interactive
surfaces (`web`, `serve`, ACP) reject the bare form with guidance and
need an explicit id instead.

Session data lives in `~/.yaah/state.db` (or `$YAAH_HOME/state.db`).
Shepherd execution traces, when enabled, record every tool call and turn
boundary in a content-addressed store:

```bash
yaah shepherd-trace list
yaah shepherd-trace show <session>
yaah shepherd-trace show --latest
yaah shepherd-trace profile <session>
```

## MCP client

yaah consumes Model Context Protocol servers over stdio and HTTP:

```bash
yaah mcp list
yaah mcp add <name> <command> [args...]
yaah mcp add <name> --url http://localhost:3000
yaah mcp remove <name>
```

MCP tools are registered alongside built-in tools. `agents.default.mcp_approval`
controls whether they are auto-approved, prompted, or denied. See
[configuration.md](./configuration.md#mcp-servers).

## MCP and ACP servers

yaah can also be driven by another agent:

- `yaah serve` exposes an MCP server with `prompt`, `traces`, `status`,
  `steer`, and `compact` tools over stdio or HTTP+SSE.
- `yaah acp-serve` exposes yaah as an Agent Client Protocol (ACP) v1
  agent over stdio (newline-delimited JSON-RPC), compatible with
  editor clients such as Zed and VS Code ACP extensions. See
  [acp.md](./acp.md) for the session lifecycle, resume support, and
  client setup, and [architecture.md](./architecture.md) for the event
  plumbing.

This is useful for benchmark harnesses, editor integrations, and
agent-to-agent coordination.

## Skills

Skills are `SKILL.md` files with YAML frontmatter and a markdown body. yaah
loads project skills from `.agents/skills/` and user skills from
`~/.agents/skills/`. The `skill` tool injects a skill's instructions when the
task matches:

```bash
yaah skill list
yaah skill show <name>
yaah skill create <name> <desc> < body.md
yaah skill edit <name>
```

The format is cross-tool: a skill written for Claude Code, opencode, or
Kilo works unchanged.

## Plans and todos

The `plan` tool manages `PLAN.md` files under `.agents/plans/` with a
`draft → approved → in_progress → completed → cancelled` workflow. The
`todowrite` tool tracks in-flight work and feeds the TUI sidebar.

## Tool belt

Built-in tools include:

| Group | Tools |
|---|---|
| Files | `read`, `write`, `edit`, `patch`, `delete`, `replace`, `json_query`, `file_info` |
| Search | `grep`, `glob`, `ls` |
| Shell | `bash`, `powershell` (platform-appropriate) |
| Version control | `git`, `diff`, `bisect` |
| Go | `go_outline`, `go_refactor`, `go_test`, `go_mod`, `staticcheck` |
| Web | `http`, `webfetch` |
| Utility | `calculate`, `question` |
| Memory | `memory_search`, `memory_add`, `memory_update`, `memory_delete`, `memory_search_sessions` |
| Orchestration | `delegate`, `list_subagents`, `subagent_jobs`, `role`, `proposal`, `supervisor` |
| Session | `plan`, `todowrite`, `skill`, `background_process` |
| Isolated sessions | `apply_out` (only when a container-backed workspace is active) |

Host-only tools (`role`, `background_process`, `go_refactor`) are omitted from
isolated sessions.

## OpenTelemetry

Enable OTLP tracing to see every LLM call, tool execution, and sub-agent
dispatch as spans:

```yaml
observability:
  otel:
    enabled: true
```

Token attribution is tracked per turn, and a bounded in-memory span buffer is
available through the MCP `traces` tool. The default exporter is OTLP/HTTP.
See [configuration.md](./configuration.md#observability-reference).

## Hook events

Set `hooks.dir` to emit structured JSONL events: `session.start`,
`session.end`, `turn.start`, `tool.start`, `tool.end`, `conflict.check`,
`conflict.detect`, and `context.prune`. Events carry timestamps, model and
tool data, durations, and prune counters. Unset or empty disables hooks.

## Approval gates

Dangerous tools (writes, deletes, shell execution, git pushes) follow the
configured approval policy:

```bash
yaah --approval ask "deploy"    # prompt per dangerous call (default)
yaah --approval allow "deploy"  # auto-approve
yaah --approval deny "deploy"   # block dangerous calls
```

Approval runs in the `approval` middleware before tool dispatch. Denied calls
are replaced with synthesized tool results so the model always sees a
response. Sub-agents run with approval `allow`; the parent's path rules still
apply.

## Workspace containment

File tools can be confined to a directory:

```bash
yaah --workspace . "refactor this repo"
yaah --workspace . --allow-home "..."
yaah --workspace . --workspace-ask "..."
```

Out-of-bounds paths are rejected unless `--workspace-ask` grants them for the
session. Symlinks are resolved before the check, and approvals apply to
sub-agents too. `agents.default.workspace_deny_patterns` adds secret-file
denials on top.

Session path containment is one axis. Retained sub-agent dispatches can also
carry OS-level confinement, where the host can provide one (Landlock's syscall
filtering on Linux, winacl's write-restricted access checks on Windows): a
kernel-enforced jail with a worktree-only writable root and read-only git for
the confined body, plus — on Linux only; winacl has no network axis and refuses
restrictive postures — governed egress (`deny_all` or a broker-pinned
allowlist). It is placement-gated
and host-dependent — `containment: off`/`advisory`, `mode: shared`, and any
host without a usable jail run the advisory or host path instead — with a
per-platform containment table in the
[README](../README.md#confinement-and-egress); full semantics are
in [configuration.md](./configuration.md#retained-worktrees-and-turn-checkpoints).

## Isolated workspace

For stronger isolation, a session can run its tools inside a Linux container:

```yaml
workspace:
  backend: containerd
```

The working tree is copied into the container and a git baseline is
captured. File tools and shell commands run inside the container; the host
tree stays untouched until `apply_out` is called (dry run first, then an
approval-gated apply). Sub-agents dispatched with `delegate` inherit the
same container. See
[configuration.md](./configuration.md#workspace-reference) for the image
contract and daemon requirements.

## Middleware

Every turn runs through a configurable middleware pipeline. The orchestrator
registers eleven middleware and enables nine by default; sub-agent loops use a
curated pipeline (`tool_concurrency`, `shepherd_trace`, plus `permission`,
`steer`, and `supervisor_guard` where applicable). The full table and
enable/disable rules live in
[configuration.md](./configuration.md#middleware).

## Supervision lifecycle

When a trace store is configured, startup rebuilds the supervision state from
the durable trace: the scope tree, checkpoints, and sealed outputs come back,
and isolated worktree children are re-adopted. The recovery report has four
buckets:

| Bucket | Meaning |
|---|---|
| `adopted` | A worktree child was re-adopted; its work is live again |
| `orphaned` | A terminal scope's worktree is still on disk and may hold unmerged work |
| `missing` | An active child's worktree is gone |
| `unadopted` | A recorded path is not a registered worktree of its repository |

Nothing is deleted automatically. Use `yaah shepherd` to list leftovers and
`yaah shepherd prune <path>` to delete one orphan after verifying ownership.
`yaah doctor` runs the same scan.

## Providers

yaah works with any OpenAI-compatible API and with the native Anthropic
Messages API. Providers can be mixed freely; the main agent, compact model,
fallback, and sub-agents can each use a different provider/model. The
`providers` map supports API-key and OAuth authentication, per-provider
`max_tokens`, timeouts, extra headers, and model lists. See
[configuration.md](./configuration.md#provider-reference).
