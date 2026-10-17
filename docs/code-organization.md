# Code Organization

yaah favors a clear package boundary over a perfectly uniform file size.
This page describes the current layout and the conventions used when adding
or splitting code.

## Package layout

```
yaah/
├── main.go                     # calls cmd/yaah.Execute()
├── cmd/yaah/                   # cobra commands + composition root
│   ├── wiring*.go              # session construction: providers, tools, prompts
│   ├── build_loop.go           # per-run agent loop builder
│   ├── session.go              # agentSession plumbing
│   ├── serve*.go               # MCP server
│   ├── acp_cmd.go              # ACP server shim
│   ├── web*.go                 # browser UI + SSE view
│   ├── tui.go                  # terminal UI entry point
│   └── ...                     # config, doctor, skill, mcp, memory, trace
│
├── internal/
│   ├── agent/                  # agent loop
│   │   ├── loop.go turn.go     # run loop and turn processing
│   │   ├── types.go options.go # Loop config/state and functional options
│   │   ├── lifecycle_*.go      # setup and teardown
│   │   ├── agent_*.go          # dispatch, tools, safety, context
│   │   ├── events/             # typed events + hooks
│   │   ├── context/            # pure context helpers (tokens, prune, truncation)
│   │   ├── llm/                # provider client: streaming, retry, fallback
│   │   ├── pipeline/           # middleware pipeline
│   │   ├── runner/             # sub-agent dispatch wiring
│   │   ├── subagent/           # role definitions and registry
│   │   ├── budget/             # iteration/turn budget resolution
│   │   └── supervision/        # advisory rules and destructive guard
│   ├── acp/                    # ACP JSON-RPC server
│   ├── config/                 # config load/validate
│   ├── jobs/                   # background sub-agent jobs + I/O contract
│   ├── memory/                 # SQLite sessions, memory, FTS5, embeddings
│   ├── mcp/                    # MCP client + server (stdio + HTTP)
│   ├── prompts/                # embedded identity, roles, tool docs
│   ├── providers/              # OpenAI-compatible + Anthropic clients
│   ├── tui/                    # tview components
│   └── tools/                  # built-in tools
│
├── docs/                       # user and developer documentation
├── bench/                      # benchmark harnesses
├── AGENTS.md                   # repository conventions for AI assistants
├── CONTRIBUTING.md
└── SECURITY.md
```

## File-size guidelines

| Size | Guidance |
|---|---|
| < 300 lines | Ideal |
| 300–500 lines | Acceptable when the file has one clear concern |
| 500–800 lines | Consider splitting by responsibility |
| > 800 lines | Split unless the file is generated or a deliberately flat table |

Tests may be larger. Generated code is exempt. Similar functions (for
example middleware implementations) may live in one file when splitting
would add indirection without improving navigation.

## Splitting principles

1. Split by responsibility, not by line count alone.
2. Keep a type and its methods together unless the type is genuinely
   multi-concern.
3. Avoid introducing dependency cycles; prefer small interfaces at the
   consumer.
4. Keep tests next to the code they test.
5. Split in reviewable steps and run `go build ./...` and `go test ./...`
   after each step.

## Naming conventions

- `types.go` — shared type definitions
- `<name>.go` — main implementation for a type or feature
- `<name>_<subfeature>.go` — a distinct sub-feature
- `<name>_test.go` — tests for that file
- `helpers.go` / `util.go` — use sparingly; prefer a named package

## Package conventions

- **`internal/`** is for implementation packages; `cmd/` is the composition
  root and should not grow domain logic.
- **Dependency injection over globals.** Session-scoped state (providers,
  registries, stores, trace handles) is passed explicitly.
- **Leaf packages stay leaf.** `internal/agent/context` and
  `internal/agent/events` do not import the agent loop.
- **Interfaces are defined at the consumer** when that keeps the dependency
  graph acyclic.
- **Errors are values.** Wrap with `%w`; use sentinel errors for conditions
  callers must classify.

## Established patterns

### Middleware (`internal/agent/pipeline`)

Each middleware is a small file implementing `PrepareStep`, `PostModel`, and
`PostTool`. Builders live in `config.go`; the orchestrator and sub-agent
pipelines share named builders where behavior is identical.

### Tools (`internal/tools`)

Each tool implements `Name`, `Description`, `Schema`, and `Execute`. Leaf
tools are registered in `leafTools`; runtime-wired tools are registered by
the composition root. Filesystem tools implement `FilesystemTool`; host-only
tools are explicitly classified so isolated registries can omit them.

### Providers (`internal/providers`)

The OpenAI-compatible and Anthropic clients share the `types.ChatRequest` /
`ChatResponse` contract. Provider-specific wire translation, streaming, and
error conversion stay in the provider package.

## Tooling

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must be empty
staticcheck ./...
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the review checklist and
[architecture.md](./architecture.md) for the runtime design.
