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

The `task` tool delegates isolated subtasks to a sub-agent running under a **role** that selects its tool set, iteration budget, and timeout:

- **`worker`** — code changes, file edits, test runs (filesystem + shell tools).
- **`reviewer`** — read-only analysis and code review (`read`, `grep`, `glob`, `ls`).
- **`planner`** — decomposition and coordination; inherits the worker set and can spawn further workers via `task`.

Multiple `task` calls in one turn run in parallel up to a configurable concurrency cap. Sub-agents honour a per-call or role-default timeout and return a structured result (`{"error":"timed out","partial":"..."}`) on timeout or cancellation so the parent can recover gracefully. Configurable under `agent.subagent` in `config.yaml`.

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

Edit `~/.yaah/config.yaml`:

```yaml
providers:
  openai:
    name: OpenAI                          # display name (shown in :model)
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    # models:                              # optional: override API model list
    #   - gpt-4o
    #   - gpt-4o-mini
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  provider: openai                        # which provider to use by default
  model: gpt-4o-mini                      # model name (no provider prefix needed)
  small_model: gpt-4o-mini                  # used for context compaction (optional; falls back to model)
  max_iterations: 50
  approval: ask                           # ask | allow | deny

# agent:
#   middleware:
#     enabled:                             # explicit set of middleware to run (in order)
#       - steer
#       - followup
#       - compaction
#       - approval
#       - loop_detection
#     # disabled:                          # exclude specific middleware
#     #   - approval

# hooks:
#   dir: ~/.yaah/hooks                    # optional: JSONL event log for external integrations

# editor: code --wait                    # editor for 'yaah config edit' (falls back to $EDITOR, $VISUAL, vi)

log_level: INFO
```

Environment variables referenced as `${VAR_NAME}` are substituted at load time.

The editor for `yaah config edit` is resolved in this order:
1. `editor` field in config.yaml
2. `$EDITOR` environment variable
3. `$VISUAL` environment variable
4. `vi` (built-in fallback)

Run `yaah doctor` to see which editor is active and how it was resolved.

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

**Active development.** Core features stable: middleware pipeline, streaming,
tool execution, SQLite memory and session persistence, MCP integration, TUI,
session resume, and hook events for external agents. See
[docs/architecture.md](./docs/architecture.md) for details.

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
