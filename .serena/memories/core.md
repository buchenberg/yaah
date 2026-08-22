# yaah — project core

yaah is a vendor-free AI agent harness built as a single Go static binary.
Config at `~/.yaah/`, skills at `./.agents/` (project) and `~/.agents/` (user).

## Source map

- `main.go` — entry point, calls `cmd/yaah.Execute()`
- `cmd/yaah/` — cobra CLI commands (root, serve, tui, web, acp, config, doctor, etc.)
- `internal/agent/` — agent loop, typed events, tool dispatch, context, hooks, persistence
- `internal/tools/` — all built-in tools (read, write, edit, grep, glob, bash, powershell, git, etc.)
- `internal/tui/` — tview TUI (paned layout, components/ subpackages)
- `internal/mcp/` — MCP client + server (stdio + HTTP)
- `internal/memory/` — SQLite + FTS5 (sessions, messages, memory store)
- `internal/providers/` — OpenAI & Anthropic API clients
- `internal/skills/` — SKILL.md discovery, frontmatter parsing
- `internal/config/` — load `~/.yaah/config.yaml`, env substitution
- `internal/instructions/` — walk up cwd, load AGENTS.md/CLAUDE.md

## Key invariants

- **No `pkg/` exports** — everything private in `internal/`. `pkg/` reserved for future.
- **No third-party HTTP client** — stdlib `net/http` only.
- **No model SDK** — direct HTTP to `chat/completions` endpoints.
- **No codegen, no build tags, no `go generate`**.
- **No globals** except build-time vars in `cmd/yaah/root.go` and serve-mode state in `cmd/yaah/serve.go`.
- **One file, one concern.**
- **Engine-View Architecture**: agent loop communicates with consumers (TUI, REPL, sub-agents) through a typed event interface via `pubsub.Broker[Event]`. No callbacks on `agent.Loop`.

For tech stack details, see `mem:tech_stack`.
For build/test/lint commands, see `mem:suggested_commands` and `mem:task_completion`.
For code conventions, see `mem:conventions`.