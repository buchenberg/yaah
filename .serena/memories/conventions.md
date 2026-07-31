# yaah — code conventions

## Style

- `gofmt -w .` before committing — non-negotiable
- `go vet` must be clean
- `staticcheck` must be clean
- Prefer `for ... range` over index-based loops
- Prefer early returns over nested `if/else`
- One file, one concern
- Tests live next to code: `foo.go` ↔ `foo_test.go`
- Use `t.Run("name", func(t *testing.T) { ... })` for subtests

## Architecture rules

- No codegen, no build tags, no `go generate`
- No third-party HTTP client — `net/http` stdlib only
- No globals except build-time vars and serve-mode OTel state
- Errors are values, not panics
- `cobra + pflag` for CLI
- `modernc.org/sqlite` for SQLite (pure Go, no CGo)
- No model SDK — direct HTTP to `chat/completions`

## Project structure

- `internal/` for everything private; `pkg/` reserved for future exports
- `.scratch/`, `.ghost/`, `.claude/`, etc. — local dev dirs, gitignored, never commit
- `.agents/skills/` — project-level skills, tracked in git
- `~/.agents/skills/` — user-level skills, not tracked

## Engine-View pattern

New consumers implement `agent.View` interface (`HandleEvent(Event)`). 
New event types go in `internal/agent/events.go` with `eventMarker()` method.
Add `case` to every `HandleEvent` — type-switch exhaustiveness catches missing cases.
Control-plane messages (todos, questions, approvals) use `tui.ControlMsg` — separate from broker events.

## Docs

- `docs/architecture.md` — detailed architecture
- `docs/sub-agents.md` — sub-agent roles, escalation, contracts
- `docs/features.md` — TUI, REPL, MCP, tools, observability
- `docs/configuration.md` — full config reference