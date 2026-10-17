# AGENTS.md — yaah

> Notes for AI coding assistants (and humans) working on the yaah codebase.

## What yaah is

yaah is a vendor-free AI agent harness: one Go static binary, minimal config
at `~/.yaah/`, skills at `./.agents/` (project, tracked) and `~/.agents/`
(cross-tool standard, per-machine), and MCP over stdio and HTTP. See
[README.md](./README.md) for the user-facing pitch and
[docs/architecture.md](./docs/architecture.md) for the runtime design.

## Local-only directories

These are local to a developer's working copy and are gitignored. Never commit
them:

- **`.scratch/`** — scratch space for cloning third-party repos while building
  a skill. Real dependencies are pinned in `go.sum`.
- **`.ghost/`, `.claude/`, `.qwen/`, `.hermes/`, etc.** — metadata for AI
  coding tools.

## Repo layout

```
yaah/
├── main.go                      # calls cmd/yaah.Execute()
├── cmd/yaah/                    # cobra commands + composition root
│   ├── root*.go                 # build-time vars, persistent flags, dispatch
│   ├── session*.go              # agentSession plumbing and options
│   ├── wiring*.go               # providers, tools, prompts, OTel, MCP
│   ├── build_loop.go            # per-run LoopBuilder + runPrompt
│   ├── provider_resolve.go      # provider/model/fallback resolution
│   ├── repl_loop.go             # interactive REPL
│   ├── serve*.go                # MCP tool server (stdio + HTTP)
│   ├── acp_cmd.go               # ACP server shim (server in internal/acp)
│   ├── web*.go                  # browser UI + SSE view
│   ├── tui.go                   # terminal UI entry point
│   └── ...                      # config, doctor, skill, mcp, memory, trace, update
├── internal/
│   ├── acp/                     # ACP JSON-RPC server
│   ├── agent/                   # agent loop, events, pipeline, tools, context
│   │   ├── budget/              #   pure sub-agent budget resolution
│   │   ├── context/             #   pure context helpers (tokens, split, prune)
│   │   ├── errorclassify/       #   provider error classification
│   │   ├── events/              #   typed events + hooks
│   │   ├── llm/                 #   LLM client: streaming, retry, fallback
│   │   ├── pipeline/            #   middleware pipeline
│   │   ├── runner/              #   sub-agent dispatch wiring
│   │   ├── subagent/            #   role definitions and registry
│   │   └── supervision/         #   advisory rules and destructive guard
│   ├── config/                  # config load, env substitution, validation
│   ├── doctor/                  # `yaah doctor` checks
│   ├── gitsandbox/              # untracked-file-preserving git sandbox wrapper
│   ├── instructions/            # AGENTS.md/CLAUDE.md discovery
│   ├── jobs/                    # background jobs + sub-agent I/O contract
│   ├── mcp/                     # MCP client + server (stdio + HTTP)
│   ├── memory/                  # SQLite sessions, memory, FTS5, embeddings
│   ├── observability/           # OpenTelemetry tracing + span buffer
│   ├── plans/                   # PLAN.md discovery/parsing
│   ├── process/                 # background process manager
│   ├── prompts/                 # identity, roles, tool docs, prompt assembly
│   ├── providers/               # OpenAI-compatible + Anthropic clients
│   ├── pubsub/                  # typed event broker
│   ├── repl/                    # REPL history, slash commands, colors
│   ├── rolefile/                # role frontmatter parsing/validation
│   ├── sandboxfake/             # in-memory sandbox test double
│   ├── skills/                  # SKILL.md discovery/parsing
│   ├── toolfmt/                 # shared tool result formatting
│   ├── tools/                   # built-in tools
│   ├── tui/                     # tview UI components
│   ├── types/                   # message and chat request types
│   └── update/                  # release checking and self-update
├── docs/                        # architecture, configuration, features, sub-agents,
│                                # prompts, agent instructions, code organization,
│                                # TUI, web UI
├── bench/                       # benchmark harnesses
├── .agents/                     # project skills and roles
├── .github/workflows/ci.yml     # CI: gofmt, vet, staticcheck, test, native build (ubuntu/macos/windows)
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── AGENTS.md
└── LICENSE                      # MIT OR Apache-2.0
```

## Build and test

```bash
go build .                      # produces ./yaah
go test ./...                   # all tests
go vet ./...                    # vet
gofmt -l .                      # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...   # staticcheck

# Cross-compile
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64    .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

Install locally:

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
ditto --norsrc yaah ~/.local/bin/yaah  # macOS: avoids Gatekeeper quarantine
```

## Dev loop (MCP hot-reload)

Use the HTTP MCP transport so the host agent can connect once and survive
rebuilds:

```bash
yaah serve --http 127.0.0.1:7333
```

After a code change, rebuild and restart only the server process; the host
agent reconnects on the next request. Verify the swap via `status`
(`pid` must change), then inspect behavior with `traces` (`tree: true` on a
known `trace_id`). One observable change per iteration; trust trace data over
model self-report. Full workflow: `.agents/skills/yaah-dev-loop/SKILL.md`.

`cmd/yaah/serve.go` registers the same tools for stdio and HTTP via
`registerServeTools()`.

## Conventions

- **Go 1.26+** (the croft dependency requires go >= 1.26.3).
- **No codegen, no build tags, no `go generate`.**
- **cobra + pflag** for CLI.
- **`internal/` for everything private.** `pkg/` is reserved for future
  exports.
- **Avoid globals** except build-time vars, persistent-flag parse targets,
  the embedded web FS, the atomic role registry, and OTel instruments
  initialised once at startup.
- **Errors are values, not panics.**
- **No third-party HTTP client.** Use `net/http` from the stdlib.
- **`gopkg.in/yaml.v3`** for config parsing.
- **`modernc.org/sqlite`** for SQLite (pure Go, no CGo, FTS5 included).
- **No model SDK for the OpenAI-compatible path.** Direct HTTP to
  `chat/completions` preserves provider extension fields
  (`reasoning_content`, Copilot mode, thinking overrides). The native
  Anthropic path uses `github.com/anthropics/anthropic-sdk-go` for wire types,
  SSE decoding, and typed errors; retries, fallback, and classification stay
  in yaah.

## Style

- Run `gofmt -w .` before committing.
- `go vet` and `staticcheck` must be clean.
- Prefer `for ... range` and early returns.
- One file, one concern.
- Tests live next to the code (`foo.go` ↔ `foo_test.go`).
- Use subtests (`t.Run("name", ...)`) for table-driven cases.

## Engine-view architecture

The agent loop communicates with consumers (TUI, REPL, web, ACP) through a
single typed event interface. View delivery is broker-based; a few control
hooks (`ApproveFn`, `ContinueAfterMaxIter`, `OnUsage`, `OnSubUsage`) remain on
the loop.

### Adding a consumer

Implement `agent.View`:

```go
type View interface {
    HandleEvent(Event)
}
```

The loop creates an internal `pubsub.Broker[Event]` and a `BrokerView`
adapter. Events are typed structs implementing the sealed `Event` interface;
see `internal/agent/events/events.go`.

### Adding an event type

1. Add the struct in `internal/agent/events/events.go` with an
   `eventMarker()` method.
2. Add it to `allEvents()` in `internal/agent/events/exhaustive_test.go`.
   The exhaustive tests fail until every consumer handles the event (an
   explicit "intentionally ignored" case counts).
3. Publish it from the agent loop's internal broker.

### Consumers

| Consumer | View impl | File |
|---|---|---|
| TUI | `App.HandleEvent` (`App` is the `agent.View`) | `internal/tui/proxy.go` |
| REPL | `terminalView` / `replView` | `cmd/yaah/view_terminal.go` |
| Web | `sseView` | `cmd/yaah/web_view.go` |
| Sub-agents | `agent.NoopView` | `internal/agent/runner/runner.go` |
| MCP serve | `agent.NoopView` | `cmd/yaah/serve.go` |
| ACP serve | `acp.View` + `acp.ViewWithWrite` | `internal/acp/view.go` |

Control-plane messages (todos, questions, approvals, model lists) use
`control.Msg`, a separate channel from broker events.

## Skills

Project skills live in `.agents/skills/`; user skills live in
`~/.agents/skills/`. Load a skill when the task matches its description using
the `skill` tool (`action: "load"`). See the directory for the current list;
`yaah-dev-loop`, `beads`, and `dolt-server-lifecycle` are the core development
skills.

## Beads issue tracker

This project uses **bd (beads)** for task tracking. Run `bd prime` for the
full workflow.

```bash
bd ready                 # available work
bd show <id>             # details
bd update <id> --claim   # claim work
bd close <id>            # complete work
```

Rules:

- Use `bd` for all task tracking. Do not create markdown TODO lists or use
  TodoWrite/TaskCreate.
- Use `bd remember` for durable knowledge; do not create MEMORY.md files.
- Conservative git policy by default: do not commit, push, or run Dolt remote
  sync unless the user asks.

## What not to do

- Don't add a `web/` or `gateway/` package, or integrations for
  Telegram/Discord/Slack.
- Don't add a hosted service or a paid-only integration.
- Don't add Anthropic-specific features outside the provider/MCP surface.
- Don't change the license (`MIT OR Apache-2.0`).
- Don't bump the version in a PR; maintainers cut releases.
