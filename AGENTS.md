# AGENTS.md — yaah

> Notes for AI coding assistants (and humans) working on the yaah codebase.

## What yaah is

yaah is a vendor-free AI agent harness. One Go static binary, minimal
config at `~/.yaah/`, skills at `./.agents/` (project, tracked in git)
and `~/.agents/` (cross-tool standard, per-machine), MCP over stdio and
HTTP for tool servers. See `README.md` for the user-facing pitch.

## Local-only directories

The following are local to a developer's working copy and are gitignored.
They are not part of the yaah repo and should never be committed:

- **`.scratch/`** — scratch space for AI coding assistants to clone
  third-party repos when building yaah skills. For example, building the
  `charm-bubbles` skill clones `github.com/charmbracelet/bubbles` here
  for reading. These clones are working copies only — the real upstream
  dependencies are pinned in `go.sum` as Go modules.
- **`.ghost/`, `.claude/`, `.qwen/`, `.hermes/`, etc.** — metadata dirs
  for various AI coding tools. Never commit.

## Repo layout (canonical)

```
yaah/
├── main.go                      # calls cmd/yaah.Execute()
├── go.mod                       # Go 1.25+, cobra, modernc.org/sqlite
├── cmd/yaah/                    # cobra commands
│   ├── root.go                  # build-time vars (version, commit, date)
│   ├── root_cmd.go              # rootCmd: REPL, one-shot, prompt dispatch
│   ├── agent_frame.go           # agent wiring (providers, tools, middleware)
│   ├── repl_loop.go             # interactive REPL loop + slash commands
│   ├── subagent_runner.go       # sub-agent dispatch + role discovery
│   ├── provider_resolve.go      # provider/model resolution helpers
│   ├── serve.go                 # yaah serve — MCP tool server (stdio + HTTP)
│   ├── acp_cmd.go               # yaah acp-serve cobra shim (server in internal/acp)
│   ├── web.go web_view.go       # yaah web — browser UI + WebSocket view
│   ├── tui.go                   # yaah tui (bubbletea) + tui_unix.go / tui_windows.go
│   ├── plan.go                  # plan tool wiring
│   ├── goat.go                  # easter-egg `yaah yaah` ASCII goat
│   ├── version.go               # yaah version
│   ├── config.go                # yaah config show/edit
│   ├── doctor.go                # yaah doctor
│   ├── update.go                # yaah update
│   ├── skill.go                 # yaah skill list/show/create/edit
│   ├── mcp.go                   # yaah mcp list/add/remove
│   ├── memory.go                # yaah memory search/add
│   ├── session.go               # yaah session list/show
│   └── color.go                 # ANSI color helpers
├── internal/
│   ├── acp/                     # ACP protocol server (JSON-RPC wire types, view, dispatch loop)
│   ├── agent/                   # agent loop, typed events, tool dispatch, context, hooks, persistence
│   │   ├── context/              #   pure context helpers (tokens, split, prune, chunk, truncation) — leaf
│   │   ├── errorclassify/        #   structured LLM provider error classification
│   │   ├── llm/                  #   LLM client wrapping (streaming, retry, fallback, usage)
│   │   ├── pipeline/             #   middleware pipeline (compaction, approval, permissions, etc.)
│   │   └── subagent/             #   sub-agent role definitions and registry
│   ├── banner/                  # figlet + lolcat banner for the TUI/REPL
│   ├── config/                  # load ~/.yaah/config.yaml, env subst, validate
│   ├── control/                 # control-plane message types (CtrlMsg, approvals, questions, todos)
│   ├── instructions/            # walk up cwd, load AGENTS.md/CLAUDE.md
│   ├── jobs/                    # background sub-agent jobs (manager, TaskRunner, sub-agent I/O contract)
│   ├── mcp/                     # MCP client + server (stdio + HTTP), manifests
│   ├── memory/                  # SQLite + FTS5 (sessions, messages, memory)
│   ├── observability/           # OpenTelemetry tracing, in-memory span buffer
│   ├── plans/                   # PLAN.md discovery/parsing (project + user, like skills + status)
│   ├── process/                 # background process manager
│   ├── providers/               # OpenAI & Anthropic API clients, streaming, model info
│   ├── prompts/                 # system prompt assembly (identity, env, memory, project)
│   ├── pubsub/                  # typed pub/sub broker (agent event fan-out)
│   ├── repl/                    # REPL, history, slash commands, colors, banner
│   ├── skills/                  # SKILL.md discovery, frontmatter parsing
│   ├── spinner/                 # animated thinking spinner
│   ├── todo/                    # in-memory todo store
│   ├── toolfmt/                 # shared tool result formatting (TUI + web views)
│   ├── tools/                   # built-in tools (read, write, edit, replace, delete, patch, sed, json_query, grep, glob, ls, bash, powershell, git, question, webfetch, http, go_outline, go_refactor, go_test, go_mod, bisect, diff, staticcheck, calculate, file_info, task, background_process, memory, todo, plan, skill)
│   ├── tui/                     # bubbletea TUI (component system: renderers in *_component.go, styled via theme.go)
│   ├── types/                   # OpenAI message types
│   └── update/                  # GitHub release checking
├── .github/workflows/ci.yml     # CI: test, vet, staticcheck, cross-compile
├── docs/
│   ├── architecture.md          # detailed architecture documentation
│   ├── sub-agents.md            # sub-agent team, roles, escalation, contracts
│   ├── features.md              # TUI, REPL, MCP, tools, observability, middleware
│   ├── configuration.md         # full config reference
│   ├── tui-components.md        # TUI component system reference
│   └── otel-setup.md            # OpenTelemetry/SigNoz setup guide
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── AGENTS.md                    # this file
└── LICENSE                      # MIT OR Apache-2.0
```

## Build & test

```bash
go build .                      # produces ./yaah
go test ./...                   # all tests
go vet ./...                    # vet
gofmt -l .                      # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...   # staticcheck

# Cross-compile matrix
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64    .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

## Install locally

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
ditto --norsrc yaah ~/.local/bin/yaah  # macOS: avoids Gatekeeper quarantine
```

## Dev loop (MCP hot-reload)

When editing yaah source code, do **not** expect the user (or your own Kilo
session) to restart to pick up the change. Use the HTTP+SSE MCP transport —
`yaah serve --http 127.0.0.1:7333` — and let the host agent connect to the
running yaah once. After that, every code change is just:

```bash
go build -o yaah.exe .
# kill the running yaah process and start the new one with the same flags
Get-Process yaah -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Process ./yaah.exe -ArgumentList 'serve','--http','127.0.0.1:7333' -NoNewWindow
```

Total swap: ~1 s. The host agent reconnects on the next MCP request — no
agent restart, no config reload, no Kilo exit/relaunch. Verify the swap took
effect by calling `mcp__yaah__status` and checking the `pid` field.

Use the same discipline as
[karpathy/autoresearch](https://github.com/karpathy/autoresearch): one
observable change per iteration; always check the trace data with
`mcp__yaah__traces` (use `tree: true` on a known `trace_id`) before trusting
the model's self-report; the cheapest signal (`status`) goes first.

Full troubleshooting, comparison table, and sanity script are in
`.agents/skills/yaah-dev-loop/SKILL.md`. The `cmd/yaah/serve.go` tool
registration is shared between stdio and HTTP transports via
`registerServeTools()` — changes there apply to both `yaah serve` (stdio) and
`yaah serve --http`.

## Conventions

- **Go 1.25+** (per `go.mod`).
- **No codegen, no build tags, no `go generate`.**
- **cobra + pflag** for CLI.
- **`internal/` for everything private.** `pkg/` reserved for future exports.
- **No globals except** build-time vars in `cmd/yaah/root.go`, serve-mode
  state (`extraOtelProcessors`, `otelInMemoryOnly`, `tuiMCPBuf`) in
  `cmd/yaah/serve.go`, and the atomic role registry
  (`defaultRoleReg` in `internal/agent/subagent/role.go`, set at startup
  via `SetDefaultRoleRegistry` for hot-reloadable role profiles).
  OTel metric instruments in `internal/observability/metrics.go` are
  initialised once by `initMetrics()` and are effectively const after setup.
- **Errors are values, not panics.**
- **No third-party HTTP client.** Use `net/http` from stdlib.
- **`gopkg.in/yaml.v3`** for config parsing.
- **`modernc.org/sqlite`** for SQLite (pure Go, no CGo, FTS5 included).
- **No model SDK.** Direct HTTP to `chat/completions`.

## Style

- Run `gofmt -w .` before committing.
- `go vet` must be clean.
- `staticcheck` must be clean.
- Prefer `for ... range` over index-based loops.
- Prefer early returns over nested `if/else`.
- One file, one concern.
- Tests live next to the code they test (`foo.go` ↔ `foo_test.go`).
- Use `t.Run("name", func(t *testing.T) { ... })` for subtests.

## Engine-View Architecture

The agent loop (`internal/agent/`) communicates with consumers (TUI, REPL,
sub-agent runner) through a single typed event interface. There are no
callbacks on `agent.Loop` — everything flows through the broker.

### Adding a new consumer

Implement `agent.View`:

```go
type View interface {
    HandleEvent(Event)
}
```

The agent loop creates an internal `pubsub.Broker[Event]` and a `BrokerView`
adapter. All events (token deltas, tool calls, sub-agent lifecycle, flush,
done) are published as typed structs implementing the sealed `Event`
interface. See `internal/agent/events.go` for the event types.

### Adding a new event type

1. Add a struct in `internal/agent/events.go` with an `eventMarker()` method
2. Add a `case` to each `HandleEvent` implementation (the compiler will find
   missing cases thanks to the exhaustiveness of type switches)
3. Publish from the agent loop's internal broker

### Consumers

| Consumer | View impl | File |
|----------|-----------|------|
| TUI | `Model.HandleEvent` (type switch) | `internal/tui/tui.go` |
| REPL | `terminalView` / `replView` | `cmd/yaah/agent_frame.go` |
| Sub-agents | `agent.NoopView` | `cmd/yaah/subagent_runner.go` |
| MCP serve | `agent.NoopView` | `cmd/yaah/serve.go` |
| ACP serve | `acp.View` + `acp.ViewWithWrite` | `internal/acp/view.go` |

Control-plane messages (todos, questions, approvals, model lists) use
`tui.ControlMsg` — a separate channel from the broker events.

### History

The engine-view boundary was refactored in PRs #60 and #62 (plan:
`.agents/plans/engine-view-separation/PLAN.md`). Before this, the agent
loop had dual delivery (callbacks + broker) with a 25-field `AgentMsg`
god struct and an 8-hop TUI delivery pipeline. The refactor removed
callbacks entirely, internalized the broker, cut the pipeline to 4 hops,
and replaced `AgentMsg` with compile-time-exhaustive typed events.

## Skills

Project-level skills live in `.agents/skills/` and are tracked in git.
Load a skill when the task at hand matches its description. Use the
`skill` tool with `action: "load"` to inject its instructions into the
current context.

Available skills:

| Skill | When to load |
|---|---|
| `yaah-testing` | Smoke testing the CLI, sub-agents, OTel traces, Docker containers, or running CI checks |
| `yaah-dev-loop` | Building, running, and iterating on the yaah MCP server from inside a Kilo session |
| `yaah-benchmark` | Running the standard multi-step benchmark and capturing metrics from Jaeger traces |

## What NOT to do

- Don't add a `web/` package, a `gateway/` package, or anything that talks to
  Telegram/Discord/Slack. Out of scope.
- Don't add a "yaah cloud" or any hosted service.
- Don't add Anthropic-specific features unless via MCP.
- Don't change the license. `MIT OR Apache-2.0` is the deal.
- Don't bump the version in a PR. Maintainers cut releases.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:6cd5cc61 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
