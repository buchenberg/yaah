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

```bash
# From source (Go 1.22+ required)
go install github.com/buchenberg/yaah@latest

# macOS Apple Silicon
curl -fsSL https://github.com/buchenberg/yaah/releases/latest/download/yaah-darwin-arm64 -o yaah
chmod +x yaah && sudo mv yaah /usr/local/bin/

# macOS Intel
curl -fsSL https://github.com/buchenberg/yaah/releases/latest/download/yaah-darwin-amd64 -o yaah
chmod +x yaah && sudo mv yaah /usr/local/bin/

# Linux amd64
curl -fsSL https://github.com/buchenberg/yaah/releases/latest/download/yaah-linux-amd64 -o yaah
chmod +x yaah && sudo mv yaah /usr/local/bin/

# Linux arm64
curl -fsSL https://github.com/buchenberg/yaah/releases/latest/download/yaah-linux-arm64 -o yaah
chmod +x yaah && sudo mv yaah /usr/local/bin/

# Windows (PowerShell)
Invoke-WebRequest -Uri "https://github.com/buchenberg/yaah/releases/latest/download/yaah-windows-amd64.exe" -OutFile yaah.exe
```

## Quick start

```bash
# 1. Check your setup
yaah doctor

# 2. Edit your config (add a provider API key)
yaah config edit

# 3. Start the REPL
yaah

# 4. Try a one-shot prompt with streaming
yaah "explain this codebase"

# 5. Check for updates
yaah update
```

## Features

### Streaming responses
Tokens stream in real time with a thinking spinner. The spinner stops on the first token and the response appears as it's generated.

### Tool calling
The agent can use built-in tools and MCP server tools:
- `read` — read files
- `bash` — run shell commands
- `memory_search` / `memory_add` — persistent memory
- `todowrite` — task tracking
- MCP tools from registered servers (e.g. markdownui)

### Skills
Skills follow the cross-tool standard (`SKILL.md` with YAML frontmatter).
Discover skills from `~/.agents/skills/`, `~/.yaah/skills/`, and `./.agents/skills/`.

```bash
yaah skill list              # list all discovered skills
yaah skill show <name>       # show a skill's content
```

### MCP servers
Register MCP servers for additional tool capabilities:

```bash
yaah mcp list                                    # list registered servers
yaah mcp add <name> <command> [args...]          # stdio server
yaah mcp add <name> --url http://localhost:3000   # HTTP server
yaah mcp remove <name>
```

### Persistent memory
SQLite + FTS5 memory that persists across sessions:

```bash
yaah memory add "user prefers dark mode" --tags '["ui"]'
yaah memory search "dark mode"
```

The agent can also use `memory_search` and `memory_add` tools during conversations.

### Project instructions
Project `AGENTS.md` / `CLAUDE.md` files are automatically loaded and injected into the system prompt. yaah walks up from the current directory to find them.

### Todo lists
The agent can create and manage task lists during conversations using the `todowrite` tool.

## Commands

```
yaah                          # interactive REPL with splash screen
yaah "prompt"                 # one-shot with streaming
yaah config show              # effective config (secrets redacted)
yaah config edit              # scaffold or edit ~/.yaah/config.yaml
yaah doctor                   # diagnose installation
yaah skill list               # discover skills
yaah skill show <name>        # show skill content
yaah mcp list                 # list MCP servers
yaah mcp add <name> --url <url>  # register HTTP MCP server
yaah memory add <text>        # add persistent memory note
yaah memory search <query>    # search memory (FTS5)
yaah session list             # list recent sessions
yaah update                   # check for newer release
yaah version                  # version, commit, build date
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
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  model: openai/gpt-4o-mini
  max_iterations: 50
  approval: ask
```

Environment variables referenced as `${VAR_NAME}` are substituted at load time.

## Status

**v0.1.0 released.** All milestones complete.

| Milestone | Scope | Status |
|---|---|---|
| **M0** | Bootstrap: build, version, CI, cross-compile | ✅ |
| **M1** | Config + REPL + `yaah doctor` + `yaah update` | ✅ |
| **M2** | Providers + agent loop + built-in tools + streaming | ✅ |
| **M3** | Skills + `AGENTS.md` instructions | ✅ |
| **M4** | MCP client (stdio + HTTP) | ✅ |
| **M5** | Persistent memory (SQLite + FTS5) | ✅ |

80+ tests across 14 packages. Cross-compiles to 5 platforms.

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
