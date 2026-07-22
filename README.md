# yaah — Yet Another Agent Harness

> A vendor-free, open-source, fully customizable AI agent harness.
> One static Go binary. Minimal config at `~/.yaah/`. Skills at `~/.agents/`.

---

## What is this?

yaah is a CLI that lets you run an AI agent on your machine. It loads
your project context, calls the model you choose, executes the tools it
asks for, and remembers what it learned — all from a single static binary
with no required account, no telemetry, and no subscription paywall.

It follows the emerging cross-tool conventions that the agent ecosystem is
converging on:

- **`SKILL.md`** (YAML frontmatter + markdown body) for skills
- **`~/.agents/skills/`** for shared, vendor-neutral skill storage
- **`AGENTS.md`** for project instructions (walked up from cwd)
- **MCP** (Model Context Protocol) over stdio and HTTP for tool servers
- **SQLite + FTS5** for persistent memory

If a skill works in [opencode](https://github.com/anomalyco/opencode),
Claude Code, or [Hermes](https://github.com/NousResearch/hermes-agent),
it works in yaah unchanged.

## Why?

Every modern agent harness invents its own conventions for skills, config,
memory, and MCP wiring. Users pay the cost of context-switching, and skills
don't travel between tools. yaah's bet is that the standards everyone is
already converging on are good enough — and a thin, opinionated, vendor-free
CLI that consumes them is more useful than another walled garden.

## Principles

1. **Standards over reinvention.** We adopt the cross-tool conventions
   verbatim. Diverging is a last resort, with a written rationale.
2. **Vendor-free.** No paid-only integrations. No upsell. No premium
   tier. Every feature works with at least 2 providers.
3. **Minimal config.** `~/.yaah/` is one YAML file and one SQLite file.
   Everything else lives in the cross-tool `~/.agents/` or in the project.
4. **Local-first.** No telemetry, no phone-home, no required accounts.
   SQLite + filesystem is the default persistence layer.
5. **Hackable.** Every component is replaceable. The CLI is a thin shell.
6. **No subscription offers.** The project makes money (if ever) through
   support, hosted add-ons, or donations. Never gate features behind a plan.

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
with Jaeger tracing. The `yaah` service is scoped behind the `cli` profile —
add `--profile cli` to `docker compose up` and `run` commands.

```bash
export DEEPSEEK_API_KEY=sk-...
docker compose --profile cli build
docker compose --profile cli up -d    # starts yaah + jaeger
docker compose --profile cli run --rm yaah "explain this codebase"
```

Traces appear at http://localhost:16686. See [`docs/otel-setup.md`](./docs/otel-setup.md).

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
yaah --approval allow "run the tests"       # auto-approve dangerous tools
YAAH_APPROVAL=allow yaah "deploy"           # env-var equivalent
yaah --resume <session-id> "continue"       # resume a saved session
```

## Features

### Sub-agent team

yaah uses a team-based architecture. The main agent coordinates via the
`spawn_subagent` tool — it dispatches specialist sub-agents, each with a
curated tool set and role-specific guidance. Sub-agents run tools directly
on the filesystem.

| Role | Tools | Iterations | Timeout |
|---|---|---|---|
| `analyst` | webfetch, http, read, grep, glob, ls, powershell, bash, json_query, calculate, file_info, go_outline, git | 20 | 120s |
| `developer` | read, write, edit, delete, replace, grep, glob, ls, powershell, bash, json_query, git, go_outline, calculate, file_info, webfetch, http | 25 | 180s |
| `tester` | read, powershell, bash, grep, glob, ls, go_outline, calculate, file_info, json_query, webfetch, http, git | 20 | 180s |
| `reviewer` | read, grep, glob, ls, powershell, bash, calculate, file_info, go_outline, json_query, webfetch, http, git | 15 | 120s |

Custom roles from `.agents/roles/` or `~/.agents/roles/` appear at startup.

Multiple `spawn_subagent` calls in one turn fan out in parallel (up to
`agent.subagent.max_concurrency`, default 3). Sub-agents can use a different
provider and model than the main agent:

```yaml
agents:
  subagent:
    provider: deepseek
    model: deepseek-v4-flash
```

### Custom roles

Define your own sub-agent roles as markdown files in `.agents/roles/`
(project-level) or `~/.agents/roles/` (user-level):

```markdown
---
tools: [read, grep, glob, ls, bash]
max_iterations: 30
timeout: 180
---

You are a SECURITY AUDITOR. Find vulnerabilities, hardcoded secrets, and
unsafe patterns. Report findings with file paths, line numbers, and severity.
```

The file name (without `.md`) becomes the role name. Built-in roles
(`analyst`, `developer`, `tester`, `reviewer`) take precedence and cannot be
overridden.

### Todo lists

The agent can create and manage task lists during conversations using the
`todowrite` tool. Use `/compact` in the REPL or `:compact` in the TUI to
summarize old messages and free up context.

### Persistent memory

SQLite + FTS5 memory persists across sessions:

```bash
yaah memory add "user prefers dark mode" --tags '["ui"]'
yaah memory search "dark mode"
```

The agent uses `memory_search` and `memory_add` tools during conversations.

### Session persistence

Every message is persisted to SQLite in real time. Sessions survive crashes
and process restarts:

```bash
yaah session list
yaah session show <id>
yaah --resume <id> "pick up where we left off"
```

### MCP servers

List, add (stdio or HTTP), and remove Model Context Protocol servers:

```bash
yaah mcp list
yaah mcp add <name> <command> [args...]
yaah mcp add <name> --url http://localhost:3000
yaah mcp remove <name>
```

### REPL slash commands

In the interactive REPL, prefix with `/` for built-in commands:

| Command | Action |
|---|---|
| `/exit`, `/quit` | Quit yaah |
| `/clear` | Clear the terminal screen |
| `/compact` | Force LLM context summarization |
| `/help`, `/?` | Show available commands |

Arrows navigate REPL history. History is persisted at `~/.yaah/history`.

### TUI colon commands

In the TUI, type `:` to open the command palette:

| Command | Action |
|---|---|
| `:help` | Show available commands |
| `:clear` | Clear chat history |
| `:compact` | Summarize old messages |
| `:banner` | Toggle the ASCII art banner |
| `:model` | Search and switch model/provider |
| `:quit` | Exit the TUI |

### Hook events

Set `hooks.dir` in config to emit structured JSONL events on session
boundaries, turn boundaries, and tool calls:

```yaml
hooks:
  dir: ~/.yaah/hooks
```

### OpenTelemetry observability

Enable in `config.yaml` to emit traces to any OTLP-compatible backend
(Jaeger, Grafana Tempo, OTel Collector):

```yaml
observability:
  otel:
    enabled: true
```

Every LLM call, tool execution, inner loop, and sub-agent dispatch
produces a span. Token attribution is tracked per-turn.
Start Jaeger with `docker compose up -d jaeger`, then visit
http://localhost:16686. Full guide at [`docs/otel-setup.md`](./docs/otel-setup.md).

### Approval override

```bash
yaah --approval allow "run the tests"    # headless / CI
YAAH_APPROVAL=allow yaah "deploy"        # env-var equivalent
```

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

yaah mcp list                     # list MCP servers
yaah mcp add <name> <cmd> [args]  # add stdio MCP server
yaah mcp add <name> --url <url>   # add HTTP MCP server
yaah mcp remove <name>            # remove MCP server

yaah memory add <text>            # store a fact
yaah memory search <query>        # search memory

yaah session list                 # list sessions
yaah session show <id>            # show session

yaah tui                          # launch the bubbletea TUI

yaah update                       # update yaah
yaah update check                 # check for new version
yaah version                      # print version
```

## Config

Edit `~/.yaah/config.yaml`. Environment variables referenced as `${VAR_NAME}`
are substituted at load time. Missing sections fall back to defaults.

### Full example

```yaml
providers:
  deepseek:
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama

agents:
  default:
    provider: deepseek
    model: deepseek-v4-pro
    small_model: deepseek-v4-flash
    max_iterations: 50
    context_window: 128000
    approval: ask                    # ask | allow | deny

  subagent:
    provider: deepseek               # optional — defaults to main provider
    model: deepseek-v4-flash         # optional — defaults to main model
    max_depth: 3                     # max spawn_subagent nesting depth
    max_concurrency: 3               # simultaneous spawn_subagent calls per turn
    default_timeout: 120             # seconds
    roles:
      analyst:
        timeout: 120
        max_iterations: 20
        max_depth: 0
      developer:
        timeout: 180
        max_iterations: 25
        max_depth: 0
      reviewer:
        timeout: 120
        max_iterations: 15
      tester:
        timeout: 180
        max_iterations: 20

  middleware:
    enabled:                         # explicit set overrides default pipeline
      - steer
      - followup
      - compaction
      - approval
      - loop_detection
    # disabled:                      # remove specific middleware
    #   - approval

  fallback:                          # optional — try on primary provider failure
    provider: ollama
    model: llama3.2

observability:
  otel:
    enabled: false
    endpoint: localhost:4317
    service_name: yaah
    traces: true                     # enable trace spans (default: true)
    metrics: false                   # enable OTLP metrics (default: false)
    verbose: false                   # record full conversations in spans

hooks:
  dir: ~/.yaah/hooks                 # JSONL event log (off by default)

editor: code --wait                  # config editor override
```

### Providers

At least one provider is required. Each has a `base_url` (OpenAI-compatible
endpoint) and an `api_key`:

```yaml
providers:
  deepseek:
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
    name: Deepseek                   # display name (defaults to map key)

  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama
    name: Ollama

  llama-cpp:
    base_url: http://localhost:8080/v1
    api_key:                         # not required for local models
    name: Llama.cpp
    timeout: 0                       # 0 = no timeout (slow local models)
    models:                          # limit available models for this provider
      - prism-ml/Bonsai-27B-gguf:Q1_0
```

Provider fields:

| Field | Default | Description |
|---|---|---|
| `base_url` | (required) | OpenAI-compatible API endpoint |
| `api_key` | — | API key (supports `${ENV_VAR}` substitution) |
| `name` | map key | Display name shown in CLI/TUI |
| `models` | — | Limit available models (empty = all from `/models` endpoint) |
| `timeout` | 120 | HTTP request timeout in seconds (0 = no timeout) |

### Agents

The `agents` block controls all agent behaviour including the main agent,
middleware pipeline, and sub-agents.

**`agents.default`** — the main agent loop:

| Field | Default | Description |
|---|---|---|
| `provider` | (first alphabetically) | Provider name from `providers` |
| `model` | — | Model name for the main agent |
| `small_model` | — | Cheaper model used for context compaction |
| `max_iterations` | 50 | Safety cap on loop turns |
| `context_window` | — | Token budget for compaction (0 = disabled) |
| `approval` | `ask` | `allow`, `ask`, or `deny` for dangerous tools |
| `max_inline_tools_per_turn` | `0` (unlimited) | Caps inline tool calls per turn; warns when exceeded |

**`agents.subagent`** — configures sub-agents spawned via `spawn_subagent`:

| Field | Default | Description |
|---|---|---|
| `provider` | `default.provider` | Provider for sub-agents (can differ from main agent) |
| `model` | `default.model` | Model for sub-agents (tip: use a cheaper one) |
| `max_depth` | — | Max nesting depth for `spawn_subagent` chains (0 = unlimited) |
| `max_concurrency` | 3 | Simultaneous `spawn_subagent` calls per turn |
| `default_timeout` | — | Default seconds per sub-agent (0 = no timeout) |
| `roles.<name>.timeout` | — | Per-role timeout override |
| `roles.<name>.max_iterations` | — | Per-role iteration cap override |
| `roles.<name>.max_depth` | — | Per-role depth cap override |

**`agents.fallback`** — optional provider/model to use if the primary
provider returns a transient error (429, 503):

| Field | Default | Description |
|---|---|---|
| `provider` | — | Fallback provider name |
| `model` | — | Fallback model name |

### Middleware reference

Nine middleware are available. The default pipeline runs
`steer` → `followup` → `compaction` → `approval` → `loop_detection`.

| Name | Default | Purpose |
|---|---|---|
| `steer` | on | High-priority mid-turn input before the next LLM call |
| `followup` | on | Queued between-turn messages |
| `compaction` | on | LLM-powered context summarization |
| `approval` | on | Gate on dangerous tools per `approval` mode |
| `loop_detection` | on | Halt stuck loops via tool-call-chain hashing |
| `permission` | off | Path-pattern rules to allow/deny tools by file path |
| `tool_concurrency` | off | Cap concurrent tool goroutines |
| `sub_agent` | off | Enforce per-role sub-agent depth caps |
| `prompt_caching` | off | Anthropic cache-control breakpoints |

Set `enabled` to specify an explicit order. Set `disabled` to remove
specific middleware from the default pipeline.

### Hooks

JSONL event log for external integrations. Off by default:

```yaml
hooks:
  dir: ~/.yaah/hooks
```

Events include `session.start`, `session.end`, `turn.start`, `tool.start`,
`tool.end`, and `conflict.detect` with timestamps, model, tool results,
and durations.

### Observability

```yaml
observability:
  otel:
    enabled: false
    endpoint: localhost:4317     # OTLP gRPC endpoint
    service_name: yaah
    traces: true                 # emit trace spans (default: true)
    metrics: false               # emit OTLP metrics (default: false)
    verbose: false               # record full conversation + summaries
```

When enabled, every LLM call, tool execution, inner loop, and sub-agent
dispatch produces a span with attributes. Token usage
is tracked per-turn via `turn.prompt_tokens` and `subagent.prompt_tokens`.

### Editor

```yaml
editor: code --wait              # overrides $EDITOR and $VISUAL
```

Resolution order: `editor` field → `$EDITOR` → `$VISUAL` → `vi`.

## Development

### Prerequisites

- Go 1.25+
- `gofmt` (ships with Go)
- `staticcheck` for linting (optional but recommended)

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

### Repo layout

```
yaah/
├── main.go                       # calls cmd/yaah.Execute()
├── cmd/yaah/                     # cobra commands
│   ├── root.go                   # build-time vars (version, commit, date)
│   ├── root_cmd.go               # rootCmd, REPL, one-shot, agent wiring
│   ├── version.go config.go      # CLI subcommands
│   ├── doctor.go update.go
│   ├── skill.go mcp.go memory.go session.go
│   ├── tui.go                    # bubbletea TUI
│   └── color.go                  # ANSI color helpers
├── internal/
│   ├── agent/                    # agent loop, tool dispatch, middleware
│   │   ├── llm/                   #   LLM client (streaming, retry, fallback)
│   │   ├── pipeline/              #   middleware pipeline
│   │   ├── subagent/              #   sub-agent role definitions and registry
│   │   └── errorclassify/         #   provider error classification
│   ├── banner/                   # figlet + lolcat banner
│   ├── config/                   # config loader + env subst
│   ├── instructions/             # AGENTS.md/CLAUDE.md discovery
│   ├── mcp/                      # MCP client (stdio + HTTP)
│   ├── memory/                   # SQLite + FTS5
│   ├── observability/            # OpenTelemetry tracing
│   ├── plans/                    # PLAN.md plan files
│   ├── process/                  # background process manager
│   ├── prompts/                  # identity.md + system prompt assembly
│   ├── providers/                # OpenAI Chat Completions client
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
│   ├── BENCHMARK-HISTORY.md      # benchmark history
│   ├── PROMPT-INJECTION.md       # prompt injection architecture map
│   ├── tui-components.md         # TUI component reference
│   └── otel-setup.md             # Jaeger setup guide
├── BENCHMARKS.md                  # current benchmark suite
├── AGENTS.md                     # coding assistant instructions
├── CONTRIBUTING.md
└── SECURITY.md
```

### Architecture

See [`docs/architecture.md`](./docs/architecture.md) for a detailed
walkthrough of the agent loop, middleware pipeline,
tool execution, streaming, context compaction, and sub-agent lifecycle.

Benchmarks and perf history are in [`docs/BENCHMARK-HISTORY.md`](./docs/BENCHMARK-HISTORY.md).
Current benchmark results are in [`BENCHMARKS.md`](./BENCHMARKS.md).

## Status

yaah is in active development and is feature-complete for daily use.

**Stable** — two-layer agent→sub-agent architecture with FullTools mode,
sub-agent batching and contract auto-injection, token attribution,
middleware pipeline, streaming LLM responses, context compaction,
approval gates, loop detection, SQLite session and memory
persistence, session resume, MCP integration (stdio + HTTP), bubbletea TUI,
REPL with slash commands, hook events for external agents, sub-agent dispatch
with roles/concurrency/timeouts, agent conflict reconciliation, context-aware
sub-agent interrupt propagation.

**Experimental** — `yaah update` (GitHub release check), `yaah tui`'s
`:model` and `:provider` commands.

## Future improvements

- **Named sub-agent roster** — configure multiple sub-agent roles with
  different models and tool sets, selectable per dispatch.
- **Plugin system** — register custom Go tools and middleware without
  recompiling.
- **Declarative workflows** — define multi-step agent pipelines as DAGs
  of role-typed tasks with dependencies.
- **Web UI** — a browser-based interface with session browsing and
  real-time streaming.
- **Session export / import** — dump transcripts as JSONL or Markdown,
  replay or resume from a file.
- **Better MCP lifecycle** — health checks, auto-restart, graceful
  shutdown ordering.
- **Knowledge base from project files** — index the project tree (RAG)
  into the SQLite FTS5 store.
- **Structured `spawn_subagent` results** — programmatic sub-agent state
  detection in tool results.

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
