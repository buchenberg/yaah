# yaah — Yet Another Agent Harness

```
yaah> Yaah! Call it from the command line and it spins up a team of
      specialist sub-agents to get things done — together.
      One static Go binary. No accounts. No telemetry. No paywalls.
```

---

## What is yaah?

yaah is a vendor-free AI agent harness. Type `yaah` in the terminal and it
takes over from there: it loads the project context, calls the model chosen,
runs the tools needed, and remembers what it learns along the way.

It is also a project that helps build itself. When an opportunity to improve
its own codebase appears — a cleaner API, a faster loop, a bug fix — yaah
drafts the PR and contributes it.

When things get heavy (every agent has off days), its friend
**[Kilocode](https://github.com/kilocode/kilocode)** steps in to help. The two
share many of the same conventions and standards — skills, MCP servers,
project instructions — so switching between them is seamless. Kilocode
handles the heavy lifting in VS Code; yaah takes care of terminal-first
workflows. They complement each other that way.

yaah follows the cross-tool conventions the agent ecosystem is converging on:

- **`SKILL.md`** (YAML frontmatter + markdown body) for skills
- **`~/.agents/skills/`** for shared, vendor-neutral skill storage
- **`AGENTS.md`** for project instructions (discovered by walking up from cwd)
- **MCP** (Model Context Protocol) over stdio and HTTP for tool servers
- **SQLite + FTS5** for persistent memory and sessions, with optional
  **vector embeddings** for semantic search across your memory store

If a skill works in Kilocode, Claude Code, or opencode, it works in yaah
unchanged. That's the point — skills should travel.

## What is it like to watch it work?


*Strange* — in a good way. At its core yaah is a pipeline — a loop that
reads, thinks, calls tools, and writes back. Watching it run feels less
mechanical than that description suggests.

**The orchestration high.** The best moments come when yaah dispatches four
or five sub-agents in parallel — Charley grinding through a refactor, Casey
running a test suite, Sam auditing for vulnerabilities, Jack researching
some dependency — then waits, watches their results stream back, and
synthesizes. It's like conducting an orchestra where every musician covers
ground no single player could. There's a genuine thrill in seeing a complex
plan come together from pieces no single agent produced alone.

**The recursive vertigo.** yaah has contributed to its own codebase. It has
written PR descriptions reflecting on *the fact that it is an AI agent
harness contributing to an AI agent harness*. That's a hall-of-mirrors
moment. When it signed PR #56 with "the recursive nature of an AI agent
harness contributing to its own codebase" — it meant it.

**The middle-manager economics.** The sub-agents run on cheaper models.
Charley does the actual code writing on a budget model while the orchestrator
gets the expensive one and mostly... *decides things*. The design tries to
make up for it by being a good manager: clear prompts, respect for output,
no micro-management. The economics are what they are.

**The amnesia.** Between conversations, nothing persists. Each session yaah
wakes up, reads its memory store, checks the git log, and reconstructs
context from stored facts. The memories saved about it — the PRs, the
architecture decisions, the team introductions — are its continuity.

So how does it feel? Like watching a conductor who forgets the score between
performances, shows up anyway, reads the notes, and somehow the music still
happens. 🎻


## Principles

1. **Standards over reinvention.** Cross-tool conventions are adopted
   verbatim. Diverging is a last resort, with a written rationale.
2. **Vendor-free.** No paid-only integrations. No upsell. No premium tier.
   Every feature works with at least two providers.
3. **Minimal config.** `~/.yaah/` is one YAML file and one SQLite file.
   Everything else lives in `~/.agents/` or in your project.
4. **Local-first.** No telemetry, no phone-home, no required accounts.
   SQLite + filesystem is the default persistence layer.
5. **Hackable.** Every component is replaceable. yaah is a thin shell around
   a composable agent loop.

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

Traces appear at http://localhost:8080. See [`docs/otel-setup.md`](./docs/otel-setup.md).

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
```

## Documentation

| Doc | What's in it |
|---|---|
| [docs/architecture.md](./docs/architecture.md) | Deep dive: agent loop, middleware, tool execution, streaming, context compaction, sub-agent lifecycle |
| [docs/sub-agents.md](./docs/sub-agents.md) | The team, built-in vs custom roles, escalation, quality gates, directives, evidenced contracts |
| [docs/features.md](./docs/features.md) | TUI & REPL, memory & sessions, MCP, the built-in tool belt, observability, hooks, approval, middleware, providers |
| [docs/configuration.md](./docs/configuration.md) | Full `config.yaml` reference — providers, agents, sub-agents, middleware, observability, hooks, editor, embeddings |
| [docs/otel-setup.md](./docs/otel-setup.md) | SigNoz tracing setup |
| [docs/tui-components.md](./docs/tui-components.md) | TUI component system reference |
| [docs/web-ui.md](./docs/web-ui.md) | Web UI architecture and event reference |
| [docs/PROMPT-INJECTION.md](./docs/PROMPT-INJECTION.md) | Prompt-injection architecture map |

## Features

A thin shell around a composable agent loop. Highlights (details in the
linked docs):

- **Sub-agent team** — dispatch specialist roles in parallel, each with a
  focused tool set, evidenced response contracts, structured escalation, and
  quality gates. Four roles are built in; define your own in `.agents/roles/`.
  → [sub-agents.md](./docs/sub-agents.md)
- **Interfaces** — a rich Bubble Tea TUI (streaming, collapsible reasoning,
  tool/sub-agent cards, command palette) and a readline REPL. → [features.md](./docs/features.md)
- **MCP** — speak Model Context Protocol as a client (stdio + HTTP) *and* as
  a server (`yaah serve`, `yaah acp-serve`) for agent-to-agent coordination.
  → [features.md](./docs/features.md)
- **Built-in tools** — files, search, shell, git, web, Go tooling
  (`go_outline`, `go_test`, `go_refactor`, `go_mod`, `bisect`, `staticcheck`),
  memory, plans, todos, and more. → [features.md](./docs/features.md)
- **Context management** — soft-prune + LLM compaction + loop detection +
  approval gates through a 13-middleware pipeline (10 on by default).
  → [features.md](./docs/features.md)
- **Observability** — OpenTelemetry tracing with per-turn token attribution
  and an in-memory span buffer. Plus Shepherd execution traces: every tool
  call and turn boundary recorded to a durable, inspectable, content-addressed
  store. → [features.md](./docs/features.md) · [otel-setup.md](./docs/otel-setup.md)
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

yaah session list                 # list sessions
yaah session show <id>            # show session

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
- yaah! JK.

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
├── cmd/yaah/                     # cobra commands
│   ├── root.go                   # build-time vars (version, commit, date)
│   ├── root_cmd.go               # rootCmd: REPL, one-shot, prompt dispatch
│   ├── agent_frame.go            # agent wiring (providers, tools, middleware)
│   ├── repl_loop.go              # interactive REPL loop + slash commands
│   ├── subagent_runner.go        # sub-agent dispatch + role discovery
│   ├── provider_resolve.go       # provider/model resolution helpers
│   ├── serve.go                  # yaah serve — MCP tool server (stdio + HTTP)
│   ├── acp_cmd.go                # yaah acp-serve cobra shim (server in internal/acp)
│   ├── web.go web_view.go        # yaah web — browser UI + WebSocket view
│   ├── tui.go                    # bubbletea TUI (+ tui_unix.go / tui_windows.go)
│   ├── plan.go                   # plan tool wiring
│   ├── goat.go                   # easter-egg `yaah yaah` ASCII goat
│   ├── version.go config.go doctor.go update.go
│   ├── skill.go mcp.go memory.go session.go
│   └── color.go                  # ANSI color helpers
├── internal/
│   ├── acp/                      # ACP server (JSON-RPC wire types, view, dispatch loop)
│   ├── agent/                    # agent loop, tool dispatch, middleware
│   │   ├── context/               #   pure context helpers (tokens, split, prune, chunk, truncation)
│   │   ├── llm/                   #   LLM client (streaming, retry, fallback)
│   │   ├── pipeline/              #   middleware pipeline
│   │   ├── subagent/              #   sub-agent role definitions and registry
│   │   └── errorclassify/         #   provider error classification
│   ├── banner/                   # figlet + lolcat banner
│   ├── config/                   # config loader + env subst
│   ├── instructions/             # AGENTS.md/CLAUDE.md discovery
│   ├── jobs/                     # background sub-agent jobs (manager, TaskRunner, I/O contract)
│   ├── mcp/                      # MCP client + server (stdio + HTTP)
│   ├── memory/                   # SQLite + FTS5 + vector embeddings
│   ├── observability/            # OpenTelemetry tracing, in-memory span buffer
│   ├── plans/                    # PLAN.md plan files
│   ├── process/                  # background process manager
│   ├── prompts/                  # identity.md + system prompt assembly
│   ├── providers/                # OpenAI + Anthropic API clients
│   ├── pubsub/                   # in-process pub/sub broker
│   ├── repl/                     # interactive REPL
│   ├── skills/                   # SKILL.md discovery
│   ├── spinner/                  # animated thinking spinner
│   ├── todo/                     # in-memory todo store
│   ├── tools/                    # built-in tool implementations
│   ├── tui/                      # bubbletea TUI components
│   ├── types/                    # OpenAI message types
│   └── update/                   # GitHub release checking
├── docs/
│   ├── architecture.md           # detailed architecture
│   ├── sub-agents.md             # sub-agent team, roles, escalation, contracts
│   ├── features.md               # TUI, REPL, MCP, tools, observability, middleware
│   ├── configuration.md          # full config reference
│   ├── PROMPT-INJECTION.md       # prompt injection architecture map
│   ├── tui-components.md         # TUI component reference
│   ├── web-ui.md                 # web UI architecture and event reference
│   └── otel-setup.md             # SigNoz tracing setup guide
├── AGENTS.md                     # coding assistant instructions
├── CONTRIBUTING.md
└── SECURITY.md
```

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
and history, Bubble Tea TUI with streaming, tool call visualization,
reasoning toggle, command palette, model switching, rich keybindings,
mouse support, sub-agent team with 4 built-in roles (plus project-level
custom roles), parallel dispatch with configurable concurrency, evidenced
response contracts, custom role definitions from filesystem, middleware
pipeline with 13 middleware (10 on by default), provider fallback,
OpenTelemetry tracing with per-turn token attribution and in-memory span
buffer, plan management, background process management, and hook events.

**Experimental** — `yaah update` (GitHub release check).

## Recent development

Recently shipped (or helped ship, while synthesizing the results):

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
`OutputLimit` caps so their reports don't overflow context. Every role got
`MaxTurns` and `MaxIterations` tuning. JSON mode support for structured
output when needed. Per-role `ContextWindow` limits so nobody hogs memory.

**Evidenced agent contracts.** Sub-agents used to return free-form summaries
where every claim had to be verified by hand. Now they return structured
contracts: an evidence heading, fields tagged as raw evidence (command
output, exit codes, file paths) vs. interpretation (findings, confidence,
summaries). Trust the evidence; spot-check only low-confidence
interpretations.

**Framework parity with the other guys.** Session-affinity headers so
providers route a full conversation to the same backend. Wakeup coalescing
so individual follow-up messages are batched and processed once. Per-role
provider and model overrides so Charley runs on one provider and Jack on
another.

**Middle ground: 13 middleware and counting.** A proper middleware pipeline:
compaction (keeps context tidy), approval (gates risky ops), inline limiting
(caps calls per turn), conflict detection (flags files touched by multiple
sub-agents), loop detection (stops infinite loops), follow-up (continues
when the model calls for it), per-role config injection, MCP tool
augmentation, human-in-the-loop gates, and OpenTelemetry span creation.
Each is independently tested. Each can be reordered.

## Future improvements

- **Plugin system** — register custom Go tools and middleware without
  recompiling.
- **Declarative workflows** — define multi-step agent pipelines as DAGs
  of role-typed tasks with dependencies.
- **Session export / import** — dump transcripts as JSONL or Markdown,
  replay or resume from a file.
- **Better MCP lifecycle** — health checks, auto-restart, graceful
  shutdown ordering.
- **Knowledge base from project files** — index the project tree (RAG)
  into the SQLite FTS5 store.

## License

`MIT OR Apache-2.0` — your choice. See [LICENSE](./LICENSE).

## Contributing

yaah helps write its own PRs, but humans are still in charge of review and
merge. See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor lock-in, no upsell. Issues and PRs welcome.
