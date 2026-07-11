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
- **MCP** (Model Context Protocol) over stdio JSON-RPC for tool servers
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

# From a release (macOS / Linux)
curl -fsSL https://github.com/buchenberg/yaah/releases/latest/download/install.sh | sh

# Or download a binary from https://github.com/buchenberg/yaah/releases
```

## Quick start

```bash
# 1. Check your setup
yaah doctor

# 2. Scaffold or edit your config
yaah config edit          # opens ~/.yaah/config.yaml in $EDITOR

# 3. Start the REPL
yaah

# 4. See available commands
yaah --help

# 5. Check for updates
yaah update
```

## Where things live

```
~/.yaah/                          # yaah-specific (minimal!)
├── config.yaml                   #   providers, defaults
├── AGENTS.md                     #   optional global instructions
├── history                       #   plain-text REPL history
└── state.db                      #   SQLite: sessions, memory (M5)

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

## Status

**Milestones 0 & 1 complete.** The binary builds on 5 platforms, CI is green
across Linux / macOS / Windows, and the config, REPL, doctor, and update
check commands all work. Next up: the agent loop and built-in tools.

| Milestone | Scope | Status |
|---|---|---|
| **M0** | Bootstrap: build, version, CI, cross-compile | ✅ done |
| **M1** | Config + REPL + `yaah doctor` + `yaah update` | ✅ done |
| **M2** | Providers + agent loop + built-in tools | 🚧 next |
| **M3** | Skills + `AGENTS.md` instructions | ⬜ |
| **M4** | MCP stdio client | ⬜ |
| **M5** | Persistent memory (SQLite + FTS5) | ⬜ |

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
