# yaah — Yet Another Agent Harness

## Summary

yaah is a vendor-free AI agent harness: a single static Go binary that runs
an agent loop against any OpenAI-compatible API or the Anthropic Messages
API. On startup it assembles a system prompt from an embedded identity,
environment detection, user and project `AGENTS.md` files (discovered by
walking up from cwd), and stored memories. The loop then streams model
responses and executes tool calls — file edits, shell, git, Go tooling, web
fetch — through a middleware pipeline (context compaction, approval gates,
loop detection, conflict tracking). Sessions and memory persist to a local
SQLite database with FTS5 full-text search and optional vector embeddings
for semantic recall. Multi-step work is delegated to role-based sub-agents
(analyst, developer, tester, reviewer — or roles you define in
`.agents/roles/*.md`), each with a curated tool set and an evidenced
response contract. yaah speaks MCP as both client (stdio + HTTP tool
servers) and server (`yaah serve`), so it can consume external tools and be
consumed as a tool by other agents.

yaah follows the cross-tool conventions the agent ecosystem is converging on:

- **`SKILL.md`** (YAML frontmatter + markdown body) for skills
- **`~/.agents/skills/`** for shared, vendor-neutral skill storage
- **`AGENTS.md`** for project instructions (discovered by walking up from cwd)
- **MCP** (Model Context Protocol) over stdio and HTTP for tool servers
- **SQLite + FTS5** for persistent memory and sessions, with optional
  **vector embeddings** for semantic search across your memory store

If a skill works in Kilocode, Claude Code, or opencode, it works in yaah
unchanged. That's the point — skills should travel.


## Principles

1. **Standards over reinvention.** Cross-tool conventions are adopted
   verbatim. Diverging is a last resort, with a written rationale.
2. **Vendor-free.** No paid-only integrations. No upsell. No premium tier.
   Every feature works with at least two providers.
3. **Minimal config.** `~/.yaah/` is one YAML file and one SQLite file.
   Everything else lives in `~/.agents/` or in your project.
4. **Local-first.** No telemetry, no phone-home, no required accounts.
   SQLite + filesystem is the default persistence layer.
5. **Hackable.** Every component is replaceable. yaah is a thin shell around a composable agent loop.

## Install

### macOS / Linux — one-liner

```bash
curl -fsSL https://raw.githubusercontent.com/buchenberg/yaah/main/install.sh | sh
```

### Windows — PowerShell one-liner

```powershell
iwr -useb https://raw.githubusercontent.com/buchenberg/yaah/main/install.ps1 | iex
```

### From source (Go 1.25+ required)

```bash
go install github.com/buchenberg/yaah@latest
```

### Docker

A `Dockerfile` and `docker-compose.yml` are included for containerized use
with SigNoz tracing. The `yaah` service is scoped behind the `cli` profile —
add `--profile cli` to `docker compose up` and `run` commands.

Set up SigNoz first: https://signoz.io/docs/install/docker/

```bash
export DEEPSEEK_API_KEY=sk-...
docker compose --profile cli build
docker compose --profile cli run --rm yaah "explain this codebase"
```

Traces appear at http://localhost:8080. Observability configuration is
covered in [`docs/configuration.md`](./docs/configuration.md).

## Quick start

```bash
yaah doctor              # check your setup
yaah config edit         # add a provider API key
yaah "explain this repo" # run a one-shot prompt
yaah                     # start the interactive REPL
yaah tui                 # launch the rich TUI
```

### One-shot options

```bash
yaah --approval allow "run the tests"      # auto-approve dangerous tools
YAAH_APPROVAL=allow yaah "deploy"          # env-var equivalent
yaah --resume <session-id> "continue"      # resume a saved session
yaah -d "always run tests first" "fix X"   # inject session directive
yaah --workspace ~/code "fix X"            # restrict file tools to a directory
yaah --workspace ~/code --allow-home "fix X"  # ... and permit ~ expansion
```

## Documentation

| Doc | What's in it |
|---|---|
| [docs/architecture.md](./docs/architecture.md) | Deep dive: agent loop, middleware, tool execution, streaming, context compaction, sub-agent lifecycle |
| [docs/sub-agents.md](./docs/sub-agents.md) | The team, built-in vs custom roles, escalation, quality gates, directives, evidenced contracts |
| [docs/features.md](./docs/features.md) | TUI & REPL, memory & sessions, MCP, the built-in tool belt, observability, hooks, approval, middleware, providers |
| [docs/configuration.md](./docs/configuration.md) | Full `config.yaml` reference — providers, agents, sub-agents, middleware, observability, hooks, editor, embeddings |
| [docs/prompts.md](./docs/prompts.md) | System prompt assembly, layer ordering, per-turn injections |
| [docs/tui-components.md](./docs/tui-components.md) | TUI component system reference |
| [docs/web-ui.md](./docs/web-ui.md) | Web UI architecture and event reference |
| [docs/adr/](./docs/adr/) | Architecture Decision Records |

## Features

A thin shell around a composable agent loop. Highlights (details in the
linked docs):

- **Sub-agent team** — dispatch specialist roles in parallel, each with a
  focused tool set, evidenced response contracts, structured escalation, and
  quality gates. Four roles are built in; define your own in `.agents/roles/`.
  → [sub-agents.md](./docs/sub-agents.md)
- **Interfaces** — a rich TUI and a readline REPL. → [features.md](./docs/features.md)
- **MCP** — speaks Model Context Protocol as a client (stdio + HTTP) *and* as
  a server (`yaah serve`, `yaah acp-serve`) for agent-to-agent coordination.
  → [features.md](./docs/features.md)
- **Built-in tools** — files, search, shell, git, web, Go tooling
  (`go_outline`, `go_test`, `go_refactor`, `go_mod`, `bisect`, `staticcheck`),
  memory, plans, todos, and more. → [features.md](./docs/features.md)
- **Context management** — soft-prune + LLM compaction + loop detection +
  approval gates through a middleware pipeline: 11 built-in middleware,
  9 on by default.
  → [features.md](./docs/features.md)
- **Observability** — OpenTelemetry tracing with per-turn token attribution
  and an in-memory span buffer. Plus Shepherd execution traces: every tool
  call and turn boundary recorded to a durable, inspectable, content-addressed
  store. → [features.md](./docs/features.md) · [configuration.md](./docs/configuration.md)
- **Supervised sub-agents** — `supervised_task` runs a role with a rollback
  point: checkpointed workspace *and* conversation, automatic
  rollback-and-retry, and interactive review verdicts
  (`continue`/`rollback`/`fork`/`choose`/`accept`/`abort`). Fork variants can run
  in isolated git worktrees, so a discarded branch never touches your tree.
  → [sub-agents.md](./docs/sub-agents.md) · [architecture.md](./docs/architecture.md)
- **Persistence** — SQLite sessions + memory with FTS5 full-text search
  and optional vector embeddings for semantic recall.
- **Providers** — any OpenAI-compatible API plus native Anthropic Messages
  API, with fallback and per-role provider/model overrides. → [configuration.md](./docs/configuration.md)

## Commands

```bash
yaah                              # interactive REPL
yaah "prompt"                     # one-shot
yaah --approval allow "..."       # override approval
yaah --resume <id> "..."          # resume session

yaah config show                  # view config
yaah config edit                  # edit config
yaah doctor                       # diagnostics

yaah skill list                   # list skills
yaah skill show <name>            # show a skill
yaah skill create <name> <desc>   # scaffold a new skill
yaah skill edit <name>            # edit a skill in $EDITOR

yaah mcp list                     # list MCP servers
yaah mcp add <name> <cmd> [args]  # add stdio MCP server
yaah mcp add <name> --url <url>   # add HTTP MCP server
yaah mcp remove <name>            # remove MCP server

yaah memory add <text>            # store a fact
yaah memory search <query>        # search memory

yaah login [provider]             # OAuth device flow (providers configured with auth: oauth)
yaah logout [provider]            # clear stored credentials

yaah shepherd-trace list           # list trace sessions
yaah shepherd-trace show <id>      # show tool calls in a session
yaah shepherd-trace show --latest  # show the most recent session
yaah shepherd-trace profile <id>   # execution profile: turns, tokens, tools

yaah tui                          # launch the rich terminal UI
yaah web                          # start the browser-based chat UI
yaah web --addr :3000             # on a custom port

yaah serve                        # MCP tool server over stdio
yaah serve --http 127.0.0.1:7333  # MCP tool server over HTTP+SSE
yaah acp-serve                    # ACP server over stdio (JSON-RPC 2.0, newline-delimited)

yaah update                       # check for updates
yaah update check                 # check without applying
yaah version                      # print version
```

## Configuration

Everything lives in `~/.yaah/config.yaml` (or `$YAAH_HOME/config.yaml`).
Environment variables referenced as `${VAR_NAME}` are substituted at load
time, missing sections fall back to sensible defaults, and a scaffold is
written on first run.

The full annotated example and every field reference (providers, agents,
sub-agents, middleware, observability, hooks, editor, embeddings) live in
[**docs/configuration.md**](./docs/configuration.md).

## Development

### Prerequisites

- Go 1.25+
- `gofmt` (ships with Go)
- `staticcheck` for linting (optional, recommended)

### Build

```bash
go build .
go build -trimpath -ldflags '-s -w' -o yaah .    # optimized

# Cross-compile
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64   .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

### Test & lint

```bash
go test ./...                                        # all tests
go test -cover ./...                                 # with coverage
go vet ./...                                         # vet
gofmt -l .                                           # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./.. # staticcheck
```

### Install locally

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
ditto --norsrc yaah ~/.local/bin/yaah  # macOS: avoids Gatekeeper quarantine
```

### MCP dev loop (hot-reload)

When you are developing yaah itself (anything under `cmd/yaah/`, `internal/mcp/`,
`internal/observability/`, etc.), the fastest iteration path is to expose yaah
as an MCP server over HTTP and drive it from your AI coding agent
(Kilo/Claude Code/Codex). The agent hosts the MCP client; you own the server
process; rebuilds swap the server without restarting the agent.

Inner loop (≈1 s per iteration, no agent restart):

```bash
# 1. configure once — add to ~/.config/kilo/kilo.json (or kilo.json at repo root)
#    "yaah": { "type": "remote", "url": "http://127.0.0.1:7333/mcp" }

# 2. start the dev server (keep this terminal open)
yaah serve --http 127.0.0.1:7333

# 3. swap on every code change
go build -o yaah.exe . && \
  (Get-Process yaah -ErrorAction SilentlyContinue | Stop-Process -Force) && \
  Start-Process ./yaah.exe -ArgumentList 'serve','--http','127.0.0.1:7333' -NoNewWindow
# or:
# pkill -f 'yaah serve --http' && ./yaah serve --http 127.0.0.1:7333 &   # bash

# 4. exercise from the agent (no agent restart)
#    mcp__yaah__status      → confirm `pid` matches the new build
#    mcp__yaah__traces      → inspect the in-memory OTel ring (tree:true for hierarchy)
#    mcp__yaah__prompt      → run a real multi-turn agent task
```

Full troubleshooting, the autoresearch-style discipline (one observable change
per iteration, trust the trace data not the model narrative), and the
sanity-check script live in the project skill:
[`.agents/skills/yaah-dev-loop/SKILL.md`](./.agents/skills/yaah-dev-loop/SKILL.md).

### Repo layout

```
yaah/
├── main.go                       # calls cmd/yaah.Execute()
├── cmd/yaah/                     # cobra commands + composition root
│   ├── root.go root_cmd.go       # persistent flags; REPL / one-shot dispatch
│   ├── wiring*.go build_loop.go  # session wiring: providers, tools, prompts
│   ├── session.go repl_loop.go   # agentSession plumbing, REPL loop
│   ├── serve.go serve_tools.go   # yaah serve — MCP tool server (stdio + HTTP)
│   ├── acp_cmd.go                # yaah acp-serve shim (server in internal/acp)
│   ├── web.go web_view.go        # yaah web — browser UI + WebSocket view
│   ├── tui.go                    # yaah tui — tview terminal UI
│   └── ...                       # config, doctor, skill, mcp, memory, trace
├── internal/
│   ├── agent/                    # agent loop: turns, dispatch, compaction
│   │   ├── events/               #   typed events + hooks (exhaustive-switch tests)
│   │   ├── context/              #   pure context helpers (tokens, split, prune)
│   │   ├── llm/                  #   LLM client (streaming, retry, fallback)
│   │   ├── pipeline/             #   middleware pipeline
│   │   ├── runner/               #   sub-agent dispatch wiring
│   │   └── subagent/             #   role definitions and registry
│   ├── memory/                   # SQLite + FTS5 + vector embeddings
│   ├── mcp/                      # MCP client + server (stdio + HTTP)
│   ├── providers/                # OpenAI-compatible + Anthropic clients
│   ├── prompts/                  # embedded identity + prompt assembly
│   ├── tools/                    # 30+ built-in tools
│   ├── tui/                      # tview TUI components
│   └── ...                       # config, jobs, process, observability, skills
├── docs/                         # architecture, configuration, features, ADRs
├── AGENTS.md                     # canonical annotated layout + assistant notes
├── CONTRIBUTING.md
└── SECURITY.md
```

The full annotated layout lives in [AGENTS.md](./AGENTS.md); file-split
history and guidelines are in
[docs/code-organization.md](./docs/code-organization.md).

### Architecture

See [`docs/architecture.md`](./docs/architecture.md) for a detailed
walkthrough of the agent loop, middleware pipeline, tool execution,
streaming, context compaction, and sub-agent lifecycle.

## Status

yaah is in active development and feature-complete for daily use.

**Stable** — agent loop with streaming, context compaction, approval gates,
loop detection, SQLite session and memory persistence, session resume,
MCP integration (stdio + HTTP) as both client and server, MCP tool server
for agent-to-agent coordination (`yaah serve`), ACP server for agent communication (`yaah acp-serve`), REPL with slash commands
and history, tview TUI with streaming, tool call visualization,
reasoning toggle, command palette, model switching, rich keybindings,
mouse support, sub-agent team with 4 built-in roles (plus project-level
custom roles), parallel dispatch with configurable concurrency, evidenced
response contracts, custom role definitions from filesystem, middleware
pipeline with 11 built-in middleware (9 on by default), provider fallback,
OpenTelemetry tracing with per-turn token attribution and in-memory span
buffer, plan management, background process management, and hook events.

**Experimental** — `yaah update` (GitHub release check).

## Recent development

Recently shipped:

**Structured escalation and quality gates.** Sub-agents can now report when
they're stuck via a structured escalation block with severity, summary, and
suggestion. Blockers halt the wave and get reported immediately. And when a
developer finishes, a tester can be auto-dispatched to validate before
success is reported. Verification over trust.

**Session directives.** Policy statements can now be injected into all agent
prompts for a session: `yaah -d "always run tests first" "implement X"`.
Or set them permanently in config. The whole team follows them without being
told twice. 

```
directives: 
    - always run tests after implementation
    - say the expression "Yaah!" often
    - you love goats
```

**Context management overhaul.** Fixed the pruner walk getting stuck after
the first batch of marks (break→continue). Added a message-count compaction
trigger so context doesn't grow unbounded when pruning keeps tokens low.
Wrapped the compact provider with OTel instrumentation so compaction calls
are finally visible in traces.

**Engine-view separation.** The agent loop used to be tangled up with the
TUI — streams went straight to the renderer, everything was tightly coupled.
An in-process pub/sub broker now decouples event emission from consumers:
the agent loop publishes typed events (`AgentTurnStart`,
`ToolCallStart`, `ToolCallOutput`, `StreamChunk`, etc.) and the TUI
subscribes. Cleaner, testable, composable.

**Semantic memory.** The SQLite memory now stores vector embeddings for each
entry (via any provider that speaks `/v1/embeddings` — LM Studio, Ollama,
llama.cpp, or cloud providers). `memory_search` uses cosine similarity to
find semantically related facts even when keywords don't match: "database
connection management" now surfaces "Postgres connection pooling uses
PgBouncer." FTS5 is still there for exact-match fallback. The whole thing
configures in three lines:
```yaml
embedding:
  provider: lmstudio
  model: text-embedding-nomic-embed-text-v1.5
```

**Sub-agent budgeting.** Per-role `OutputLimit` caps sub-agent reports so
they don't overflow the orchestrator's context. Every role got `MaxTurns` and
`MaxIterations` tuning, JSON mode support for structured output when needed,
and per-role `ContextWindow` limits so nobody hogs memory.

**Evidenced agent contracts.** Sub-agents used to return free-form summaries
where every claim had to be verified by hand. Now they return structured
contracts: an evidence heading, fields tagged as raw evidence (command
output, exit codes, file paths) vs. interpretation (findings, confidence,
summaries). Trust the evidence; spot-check only low-confidence
interpretations.

**Framework improvements.** Session-affinity headers so
providers route a full conversation to the same backend. Wakeup coalescing
so individual follow-up messages are batched and processed once. Per-role
provider and model overrides so Charley runs on one provider and Jack on
another.

**Middleware pipeline.** 11 built-in middleware, 9 on by default:
steer (high-priority mid-turn input), follow-up (between-turn messages),
compaction (LLM summarization on window overflow), soft-prune (elide stale
tool output), approval (gates risky ops), inline limiting (caps calls per
turn), tool concurrency, loop detection (stops stuck loops), and conflict
detection (flags files touched by multiple sub-agents). Opt-in:
`permission` (path-pattern allow/deny) and `prompt_caching` (Anthropic
cache-control breakpoints). Each is independently tested and can be
reordered or disabled via config.

## Future improvements

The near-term roadmap lives in tracked plan files (`.agents/plans/`,
`docs/plans/`) rather than aspirational bullets. What is actually planned:

### In progress

- **Per-turn checkpoint & restore** — `supervised_task` today restores at
  attempt granularity; this adds turn-granularity rewind inside sub-agent
  loops (a hard tool error or iteration exhaustion rewinds to the state just
  before that turn), with an optional `Scope.Fork` to try multiple
  alternatives from a pre-turn snapshot.
  → [plan](./.agents/plans/per-turn-checkpoint-restore/PLAN.md)
- **Isolated workspace activation** — the `Workspace` implementation that
  runs every tool inside a `shepherd.Sandbox` is complete and tested but not
  yet wired into sessions. Activation gives worktree-isolated sub-agents
  where a discarded fork never touches your tree. Blocked on the next
  `shepherd-kernel-go` release.
  → [plan](./docs/plans/isolated-workspace-activation.md)

### Approved, ready to implement

- **Tool-result pruning recovery & prevention** — pruned tool results are
  currently lost with only a "re-run the tool" stub; this adds spill-to-disk
  for pruned content, informative stubs, corrected defaults, and per-tool
  head-limits so read content stays recoverable.
  → [plan](./.agents/plans/tool-result-pruning-recovery/PLAN.md)

### Drafted

- **Faux provider & full-stack test harness** — a scripted, zero-API-cost
  provider behind the normal provider seam, plus an in-memory harness wiring
  loop + pipeline + tools + persistence for regression suites and
  deterministic benchmark scenario runs.
  → [plan](./.agents/plans/faux-harness-port/plan.md)
- **Persistence consolidation** — unify the two SQLite stores
  (`~/.yaah/state.db`, Shepherd `trace.sqlite`) and OTel spans; cross-link
  `session_id` and `trace_id` so a single turn can be joined across all
  three systems.
  → [plan](./.agents/plans/consolidate-persistence/PLAN.md)
- **`memory_search_sessions` overhaul** — structured results (session ID,
  role, timestamp, message ID), filters, and a relevance floor, replacing
  the current concatenated-string output.
  → [plan](./.agents/plans/memory-search-sessions-overhaul/PLAN.md)
- **`todowrite` → bd adapter** — transparently persist todos to the beads
  (`bd`) issue tracker when available in the workspace, in-memory fallback
  otherwise, honoring the AGENTS.md tracking directive by construction.
  → [plan](./.agents/plans/todowrite-bd-adapter/PLAN.md)
- **TUI activity line** — tvxwidgets spinner state machine with a compaction
  gauge in the status area.
  → [plan](./.agents/plans/tui-activity-line/PLAN.md)

The long tail of smaller items (correctness, token efficiency, provider
breadth, tooling gaps measured against peer agents) is tracked in the
[best-of-breed gap backlog](./.agents/plans/best-of-breed-gap-backlog/plan.md).

## License

`MIT OR Apache-2.0` — your choice. See [LICENSE](./LICENSE).

## Contributing

yaah helps write its own PRs, but humans are still in charge of review and
merge. See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor lock-in, no upsell. Issues and PRs welcome.
