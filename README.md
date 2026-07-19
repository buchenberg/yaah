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

## Quick start

Check your setup, then add a provider API key:

```bash
yaah doctor
```

```bash
yaah config edit
```

Start chatting — interactive REPL, one-shot prompt, or the rich TUI:

```bash
yaah
```

```bash
yaah "explain this codebase"
```

```bash
yaah tui
```

Headless / CI (override approval), check for updates, or resume a session:

```bash
yaah --approval allow "write a script"
```

```bash
YAAH_APPROVAL=allow yaah "run the tests"
```

```bash
yaah update
```

```bash
yaah --resume <session-id> "continue where we left off"
```

```bash
yaah session list
```

### MCP servers
List, add (stdio or HTTP), and remove MCP servers:

```bash
yaah mcp list
```

```bash
yaah mcp add <name> <command> [args...]
```

```bash
yaah mcp add <name> --url http://localhost:3000
```

```bash
yaah mcp remove <name>
```

### Persistent memory
SQLite + FTS5 memory that persists across sessions:

```bash
yaah memory add "user prefers dark mode" --tags '["ui"]'
```

```bash
yaah memory search "dark mode"
```

The agent can also use `memory_search` and `memory_add` tools during conversations.

### Project instructions
Project `AGENTS.md` / `CLAUDE.md` files are automatically loaded and injected into the system prompt. yaah walks up from the current directory to find them.

### Todo lists
The agent can create and manage task lists during conversations using the `todowrite` tool.

### Sub-agents

The `task` tool delegates isolated subtasks to a sub-agent running under a
**role** that selects its tool set, iteration budget, and timeout:

- **`worker`** — code changes, file edits, test runs. Has `read`, `write`,
  `edit`, `delete`, `grep`, `glob`, `ls`, `bash`, `powershell`, `webfetch`.
  Default: 25 iterations, 120s timeout.
- **`reviewer`** — read-only analysis and code review. Has only `read`,
  `grep`, `glob`, `ls`. Default: 10 iterations, no timeout.
- **`planner`** — decomposition and coordination. Inherits the worker tool
  set and can spawn further sub-agents via `task`. Default: 50 iterations,
  300s timeout.

Multiple `task` calls in one turn fan out in parallel up to the configured
concurrency cap (`agent.subagent.max_concurrency`, default 3). Nesting depth
is bounded structurally — each level decrements a counter seeded from
`max_depth` — so planner → worker → [stop] chains terminate naturally.

Sub-agents honour per-call overrides (`timeout_seconds`, `max_iterations`) or
their role defaults. On timeout or cancellation the tool returns structured
JSON (`{"error":"timed out","partial":"..."}`) so the parent can inspect
partial output and decide whether to retry or continue.

All `agent.subagent.*` settings in `config.yaml` are detailed in the
[Config](#agent) section above.

### Custom roles

Define your own sub-agent roles by adding markdown files to `.agents/roles/`
(project-level, walked up from cwd) or `~/.agents/roles/` (user-level). Each
file is a YAML frontmatter block followed by the role's system guidance:

```markdown
---
tools:
  - read
  - grep
  - glob
  - ls
  - bash
max_iterations: 30
timeout: 180
max_depth: 0
---

You are a SECURITY AUDITOR. Find vulnerabilities, hardcoded secrets, and
unsafe patterns. Report findings with file paths, line numbers, and severity.
```

The file name (without `.md`) becomes the role name. Built-in roles
(`worker`, `reviewer`, `planner`) take precedence and cannot be overridden.
New roles appear in the `task` tool's `role` enum automatically at startup.
Per-role defaults can be tuned in `config.yaml` under
`agent.subagent.roles.<name>`.

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
| `:compact` | Summarize old messages to free context |
| `:banner` | Toggle the ASCII art banner on/off |
| `:model` | Search and switch the active model/provider |
| `:quit` | Exit the TUI |

Type to filter the list after `:`. `:model` queries providers' model lists
live. Press `Esc` to dismiss the palette.

### Session persistence

Every message (user prompts, assistant responses, tool calls, and tool results)
is persisted to SQLite in real time as the agent loop runs. If the process
crashes mid-conversation, all messages up to that point are recoverable.

```bash
yaah session list
```

```bash
yaah session show <id>
```

```bash
yaah --resume <id> "pick up where we left off"
```

Sessions survive process restarts. Use `--resume` to continue a conversation
exactly where it stopped — the agent has the full message history including
tool call context.

### Hook events (external integrations)

When `hooks.dir` is set in `~/.yaah/config.yaml`, yaah appends structured JSONL
events to `<hooks.dir>/<session-id>.jsonl` on session boundaries, turn
boundaries, and tool calls. This enables external tools (e.g.
[Entire.io](https://entire.io)) to track sessions for checkpoint/transcript
integration.

```yaml
hooks:
  dir: ~/.yaah/hooks
```

Events are off by default — set `hooks.dir` to enable them.

### Approval override

The global approval mode can be overridden at runtime for headless or CI
environments:

```bash
yaah --approval allow "run the tests"
```

```bash
YAAH_APPROVAL=allow yaah "deploy"
```

Invalid values fall back to `ask` with a warning.

## Commands

Start yaah — interactive REPL or one-shot:

```bash
yaah
```

```bash
yaah "prompt"
```

With approval override or session resume:

```bash
yaah --approval allow "..."
```

```bash
yaah --resume <id> "..."
```

Config and diagnostics:

```bash
yaah config show
```

```bash
yaah config edit
```

```bash
yaah doctor
```

Skills:

```bash
yaah skill list
```

```bash
yaah skill show <name>
```

MCP servers:

```bash
yaah mcp list
```

```bash
yaah mcp add <name> <cmd> [args...]
```

```bash
yaah mcp add <name> --url <url>
```

```bash
yaah mcp remove <name>
```

Memory:

```bash
yaah memory add <text>
```

```bash
yaah memory search <query>
```

Sessions:

```bash
yaah session list
```

```bash
yaah session show <id>
```

TUI, updates, version:

```bash
yaah tui
```

```bash
yaah update
```

```bash
yaah update check
```

```bash
yaah version
```

## Where things live

```
~/.yaah/                          # yaah-specific (minimal!)
├── config.yaml                   #   providers, defaults
├── AGENTS.md                     #   optional global instructions
├── history                       #   plain-text REPL history
├── state.db                      #   SQLite: sessions, memory
└── mcp/<name>.json               #   MCP server manifests

~/.agents/                        # cross-tool (shared with opencode, claude, hermes)
├── AGENTS.md
├── skills/<name>/SKILL.md
├── agents/<name>.md
├── commands/<name>.md
└── mcp/<name>.json

./.agents/                        # project-level (walked up from cwd)
├── AGENTS.md
├── skills/<name>/SKILL.md
├── agents/<name>.md
└── commands/<name>.md
```

## Config

Edit `~/.yaah/config.yaml`. Environment variables referenced as `${VAR_NAME}`
are substituted at load time. Missing sections fall back to built-in defaults
so you only need to write what you're overriding.

### Providers

At least one provider is required. Each provider has a `name` (shown in the
UI), a `base_url` (OpenAI-compatible endpoint), and an `api_key`.
Optionally, list `models` to restrict the model picker:

```yaml
providers:
  openai:
    name: OpenAI
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    # models:                              # optional: restrict the model list
    #   - gpt-4o
    #   - gpt-4o-mini

  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama

  anthropic:
    name: Anthropic
    base_url: https://api.anthropic.com/v1
    api_key: ${ANTHROPIC_API_KEY}
```

### Default

Controls which provider and model are used, iteration and context budgets,
and the approval mode:

```yaml
default:
  provider: openai       # provider key from the providers map above
  model: gpt-4o-mini     # model name (without provider prefix)
  small_model: gpt-4o-mini  # used for context compaction; falls back to model
  max_iterations: 50     # safety cap on agent loop turns per prompt
  context_window: 128000 # token budget for LLM compaction; 0 disables
  approval: ask          # ask | allow | deny
```

The `provider` field is optional. When omitted, yaah picks the provider whose
name prefix matches `model`, or the first provider alphabetically.

### Agent

Controls the agent loop's middleware pipeline and sub-agent lifecycle:

```yaml
agent:
  middleware:
    enabled:             # explicit set (in order); overrides the default pipeline
      - steer
      - followup
      - compaction
      - approval
      - loop_detection
    # disabled:          # remove specific middleware from the pipeline
    #   - approval

  subagent:
    max_depth: 3         # max task calls per loop; 0 = unlimited
    max_concurrency: 3   # simultaneous task calls per iteration; 0 = unlimited
    default_timeout: 120 # seconds; used when no role default applies
    roles:               # per-role overrides (all optional)
      worker:
        timeout: 120
        max_iterations: 25
        max_depth: 1
      reviewer:
        timeout: 60
        max_iterations: 10
      planner:
        timeout: 300
        max_iterations: 50
        max_depth: 3
```

#### Middleware reference

Nine middleware are available. The default pipeline (when `enabled` is unset)
runs `steer` → `followup` → `compaction` → `approval` → `loop_detection`.

| Name | Default | Purpose |
|---|---|---|
| `steer` | on | High-priority mid-turn input injected before the next LLM call |
| `followup` | on | Queued between-turn messages (e.g. from parallel agents) |
| `compaction` | on | LLM-powered context summarization when `context_window` is exceeded |
| `approval` | on | Gate on destructive tools (bash, write, edit, delete) per `approval` mode |
| `loop_detection` | on | Detect and halt stuck loops by hashing identical tool call chains |
| `permission` | off | Path-pattern rules to allow/deny tools by file path glob |
| `tool_concurrency` | off | Cap concurrent tool goroutines via `MaxToolConcurrency` |
| `sub_agent` | off | Enforce per-role sub-agent depth caps |
| `prompt_caching` | off | Inject Anthropic cache-control breakpoints on system/tool messages |

To enable an off-by-default middleware, list it in `enabled`. To disable a
default middleware, list it in `disabled`. When `enabled` is set, only those
middleware run (in the given order), minus any in `disabled`.

The `permission` middleware accepts rules in the form `{tool, path, mode}`
where `tool` and `path` are optional (empty = match all), and `mode` is
`allow`, `ask`, or `deny`. Paths use `filepath.Match` globs. Rules are
injected programmatically via `Loop.PermissionRules` — there is no YAML
syntax for them currently.

### Hooks

JSONL event log for external integrations (e.g. session tracking,
checkpointing). Off by default:

```yaml
hooks:
  dir: ~/.yaah/hooks
```

Events are appended to `<dir>/<session-id>.jsonl` and include
`session.start`, `session.end`, `turn.start`, `tool.start`, and `tool.end`
with timestamps, model, tool results, and durations.

### Editor

Editor for `yaah config edit`. Resolution order: `editor` field → `$EDITOR`
env var → `$VISUAL` env var → `vi`. Run `yaah doctor` to see which editor is
active.

```yaml
editor: code --wait
```

### Log level

```yaml
log_level: INFO           # DEBUG | INFO | WARN | ERROR
```

Controls internal diagnostic output written to stderr. Agent responses and
tool output are unaffected.

## Development

### Prerequisites

- Go 1.25+
- `gofmt` (ships with Go)
- `staticcheck` for linting (optional but recommended)

### Build

```bash
go build .
```

Optimized binary (stripped, no debug info):

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
```

### Test

```bash
go test ./...
```

```bash
go vet ./...
```

```bash
gofmt -l .
```

### Lint

```bash
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

### Cross-compile

```bash
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64   .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

### Install locally

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
```

On macOS use `ditto` instead of `cp` to avoid Gatekeeper quarantine:

```bash
ditto --norsrc yaah ~/.local/bin/yaah
```

### Repo layout

```
yaah/
├── main.go                      # calls cmd/yaah.Execute()
├── cmd/yaah/                    # cobra commands
│   ├── root.go                  # build-time vars (version, commit, date)
│   ├── root_cmd.go              # rootCmd, REPL, one-shot, agent wiring
│   ├── version.go config.go     # CLI subcommands
│   ├── doctor.go update.go
│   ├── skill.go mcp.go memory.go session.go
│   ├── tui.go                   # bubbletea TUI
│   └── color.go                 # ANSI color helpers
├── internal/
│   ├── agent/                   # agent loop, middleware pipeline, streaming, compaction
│   ├── banner/                  # figlet + lolcat banner for TUI/REPL
│   ├── config/                  # ~/.yaah/config.yaml loader
│   ├── instructions/            # AGENTS.md/CLAUDE.md discovery
│   ├── mcp/                     # MCP client (stdio + HTTP)
│   ├── memory/                  # SQLite + FTS5 (sessions, messages, memory)
│   ├── process/                 # background process manager
│   ├── providers/               # OpenAI Chat Completions client
│   ├── prompts/                 # system prompt assembly
│   ├── repl/                    # REPL, history, slash commands
│   ├── skills/                  # SKILL.md discovery
│   ├── spinner/                 # animated thinking spinner
│   ├── todo/                    # in-memory todo store
│   ├── tools/                   # built-in tools (read, write, edit, grep, bash, task, etc.)
│   ├── tui/                     # bubbletea TUI components
│   ├── types/                   # OpenAI message types
│   └── update/                  # GitHub release checking
├── docs/
│   ├── architecture.md          # detailed architecture documentation
│   ├── tui-component-design.md  # TUI component system design proposal
│   ├── tui-refactoring-example.md # before/after refactoring examples
│   └── tui-summary.md           # TUI component system design summary
├── AGENTS.md                    # coding assistant instructions
├── CONTRIBUTING.md
└── SECURITY.md
```

### Architecture

See [docs/architecture.md](./docs/architecture.md) for a detailed walkthrough of the
agent loop, middleware pipeline, tool execution, streaming, and context compaction.

### Tests

```bash
go test ./...
```

```bash
go test -race ./...
```

```bash
go test -v ./internal/agent/
```

```bash
go test -cover ./...
```

Tests live next to the code they test (`foo.go` ↔ `foo_test.go`) and use
`t.Run("name", func(t *testing.T) { ... })` for subtests.

## Status

yaah is in active development and is feature-complete for daily use.

**Stable** — middleware pipeline, streaming LLM responses, tool execution,
context compaction, approval gates, loop detection, SQLite session and memory
persistence, session resume, MCP integration (stdio + HTTP), bubbletea TUI,
REPL with slash commands, hook events for external agents, sub-agent dispatch
with roles/concurrency/timeouts.

**Experimental** — `yaah update` (GitHub release check), `yaah tui`'s
`:model` and `:provider` commands.

## Future improvements

- **Plugin system** — register custom Go tools and middleware without
  recompiling, via a well-defined interface and a `plugins/` directory
  convention.
- **OpenTelemetry tracing** — wire the agent loop, middleware hooks, and
  tool calls into OTel spans for observability in production agent
  pipelines.
- **Agent conflict reconciliation** — detect and merge conflicting edits
  when multiple parallel workers touch the same files, and present
  resolution options to the parent agent.
- **Declarative workflows** — define multi-step agent pipelines as DAGs
  of role-typed tasks with dependencies and failure handlers, replacing
  ad-hoc planner prompts with reproducible recipes.
- **Inter-agent messaging** — direct messages between concurrently
  running sub-agents so they can coordinate without routing through
  the parent's context.
- **Web UI** — a browser-based interface as an alternative to the terminal,
  with session browsing, config editing, and real-time streaming.
- **Prompt template library** — curated system prompts and skill packs
  for common use cases (code review, PR triage, security audit,
  documentation generation).
- **Remote MCP gateway** — a thin sidecar that bridges local `yaah`
  instances with remote MCP servers over HTTPS, enabling secure sharing
  of tool servers across machines.
- **Better MCP lifecycle** — health checks, auto-restart on crash,
  graceful shutdown ordering, and `yaah mcp status` per-server.
- **Session export / import** — dump a session transcript as JSONL or
  Markdown, and replay or resume from a file.
- **Chat completions streaming with tool use in one pass** — some
  providers (Anthropic, Groq) support streaming tool calls alongside
  content in a single response, reducing round-trips.
- **Knowledge base from project files** — index the project tree (RAG)
  into the SQLite FTS5 store so the model can ask about the codebase
  without reading every file.

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
