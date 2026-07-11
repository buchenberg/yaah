# yaah — Yet Another Agent Harness

> A vendor-free, open-source, fully customizable AI agent harness.
> One static Go binary. Minimal config at `~/.yaah/`. Skills at `~/.agents/`.

```
$ yaah --version
yaah 0.0.0
```

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
# 1. Scaffold your config
yaah config edit          # opens ~/.yaah/config.yaml in $EDITOR

# 2. Try a one-shot prompt
yaah "summarize the last 3 commits in this repo"

# 3. Start the REPL
yaah

# 4. List the skills yaah discovered
yaah skill list

# 5. Diagnose anything that's broken
yaah doctor
```

## Where things live

```
~/.yaah/                          # yaah-specific (minimal!)
├── config.yaml                   #   providers, defaults, keybinds
├── AGENTS.md                     #   optional global instructions
└── state.db                      #   SQLite: sessions, memory

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

**v0.0.0 — bootstrap.** M0 just landed: the binary builds, `--version`
and `--help` work, and a CI matrix is in place. The plan is to ship
**v0.1.0** in 5 milestones:

| Milestone | Scope | Time |
|---|---|---|
| **M0** ✅ | Bootstrap: build, version, CI, cross-compile | 1 day |
| **M1** | Config + REPL + `yaah doctor` + `yaah update` | 3-4 days |
| **M2** | Providers + agent loop + built-in tools | 4-5 days |
| **M3** | Skills + `AGENTS.md` instructions | 2-3 days |
| **M4** | MCP stdio client | 3-4 days |
| **M5** | Persistent memory (SQLite + FTS5) | 2-3 days |

The full design plan lives at
[`Markdown/agentic/yaah-plan.md`](https://github.com/buchenberg/MarkdownUI)
(under the `agentic/` folder in the yaah author's note vault). It is the
canonical source for the v0.1 scope, design decisions, and rejected
alternatives.

## License

`MIT OR Apache-2.0` — your choice, at your option. See [LICENSE](./LICENSE).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). tl;dr: conventional commits, no
vendor PRs, no upsell. Issues and PRs welcome.
