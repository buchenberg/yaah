---
name: tui-mcp-bridge
description: Expose the TUI session as an MCP server so external agents (Kilo, other yaah instances) can interact with the running agent in real time — sending prompts, pulling traces, and reading status.
status: draft
---

# TUI ↔ MCP Bridge

## Goal

Let an external agent (Kilo, another yaah instance) connect to a running
`yaah tui` session via MCP and interact with it — send prompts, inject
steering, pull OTel traces, and read session status — while all activity
renders live in the TUI.

## Motivation

Currently `yaah serve` exposes an agent session as MCP tools, but it is
a separate, headless process. The TUI runs an `agentSession` with a rich
interactive view — Kilo should be able to talk to THAT session, not a
separate one. This also lays the foundation for yaah-to-yaah multi-agent
orchestration (multiple yaah instances collaborating via MCP).

## Architecture

```
yaah tui --mcp                          Kilo (or other agent)
┌─────────────────────────┐            ┌──────────────────┐
│  TUI (bubbletea/tview)  │            │  MCP client       │
│  ┌───────────────────┐  │  stdio     │  yaah_prompt()    │
│  │ agentSession       │◄─┼────────────│  yaah_traces()    │
│  │  RunPrompt()       │  │            │  yaah_status()    │
│  │  Steer()           │  │            │  yaah_steer()     │
│  │  Compact()         │  │            │  yaah_compact()   │
│  └────┬──────────────┘  │            └──────────────────┘
│       │ events           │
│  ┌────▼──────────────┐  │
│  │ HandleEvent        │  │   ← renders whether prompt
│  │ (tokens, tools,    │  │     came from user or MCP
│  │  subagents, done)  │  │
│  └───────────────────┘  │
│  ┌───────────────────┐  │
│  │ BufferingSpanProc  │  │   ← yaah_traces reads from
│  │ (in-memory spans)  │  │     this buffer
│  └───────────────────┘  │
└─────────────────────────┘
```

The MCP server runs in the same process as the TUI, sharing the same
`agentSession`, tool registry, OTel tracer, and span buffer.
`agentSession.RunPrompt` already fires the same events regardless of
who called it — the TUI sees MCP-initiated prompts identically.

## What exists

| Piece | Where | Status |
|-------|-------|--------|
| `registerServeTools(srv, sessPtr, mu, buf, totalTokens, promptCount, ensureSession)` | `cmd/yaah/serve.go:220` | Done — registers `prompt`, `traces`, `status` |
| `BufferingSpanProcessor` | `internal/observability/buffer.go` | Done — in-memory span capture |
| `extraOtelProcessors` / `otelInMemoryOnly` | `cmd/yaah/serve.go:25-26` | Done — serve-mode OTel globals |
| MCP stdio server (`mcp.ServeStdio`) | `cmd/yaah/serve.go` | Done |
| MCP HTTP+SSE server (`mcp.ServeHTTP`) | `cmd/yaah/serve.go` | Done |
| `agentSession` (shared infrastructure) | `cmd/yaah/session.go` | Done |
| TUI creates `agentSession` via `newAgentSessionWithOptions` | `cmd/yaah/tui.go`, `tui2.go` | Done |

**Nothing new needs to be built — only wired together.**

## Implementation (5 steps)

### Step 1 — Factor out serve tool registration

Move `registerServeTools` from `cmd/yaah/serve.go` to its own file
`cmd/yaah/serve_tools.go` so both `serve.go` and `tui.go` can import it.
No logic changes — pure move.

**Files**: `serve.go` (remove 110 lines), `serve_tools.go` (add 110 lines)

### Step 2 — Add `--mcp` flag to TUI commands

Add `--mcp` to both `yaah tui` (bubbletea) and `yaah tui2` (tview).
Default: `false`.

**Files**: `tui.go:25`, `tui2.go:13`

### Step 3 — Start MCP stdio server in TUI goroutine

In `runTUI` / `runTUI2`, after creating the `agentSession`:

```go
if mcpFlag {
    // Create in-memory span buffer for the traces tool.
    buf := observability.NewBufferingSpanProcessor()
    extraOtelProcessors = []sdktrace.SpanProcessor{buf}
    otelInMemoryOnly = true

    // Re-create session WITH OTel in-memory mode.
    sess, _ = newAgentSessionWithOptions(false, false)

    // Start MCP stdio server in background goroutine.
    srv := mcp.NewServer("yaah-tui", "0.1.0")
    var mu sync.Mutex
    var totalTokens types.Usage
    var promptCount int
    sessPtr := &sess
    registerServeTools(srv, sessPtr, &mu, buf, &totalTokens, &promptCount, nil)

    go func() {
        if err := mcp.ServeStdio(srv); err != nil {
            log.Printf("mcp: %v", err)
        }
    }()
}
```

The MCP server uses the SAME `agentSession` that the TUI is wired to.
All prompt/steer/status calls go through the shared session.

**Files**: `tui.go:260-290`, `tui2.go:40-60`

### Step 4 — Add `steer` and `compact` tools to `registerServeTools`

The existing serve tools (`prompt`, `traces`, `status`) are enough for
read-only interaction. Add two write tools for agent manipulation:

```go
// yaah_steer — inject a high-priority steering message mid-turn.
// Shows up as [STEER] prefix in the next LLM request.
srv.AddTool(mcp.Tool{
    Name:        "yaah_steer",
    Description: "Inject a high-priority steering message into the next agent turn.",
    InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    var args struct{ Text string }
    json.Unmarshal(req.Params.Arguments, &args)
    mu.Lock()
    sess := *sessPtr
    mu.Unlock()
    if sess == nil {
        return nil, fmt.Errorf("session not ready")
    }
    sess.Steer(args.Text)
    return mcp.NewTextResult("steered"), nil
})

// yaah_compact — trigger context compaction.
srv.AddTool(mcp.Tool{
    Name:        "yaah_compact",
    Description: "Trigger context compaction on the running session.",
    InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    mu.Lock()
    sess := *sessPtr
    mu.Unlock()
    if sess == nil {
        return nil, fmt.Errorf("session not ready")
    }
    sess.Compact()
    return mcp.NewTextResult("compacted"), nil
})
```

**Files**: `serve_tools.go`

### Step 5 — Configure Kilo MCP connection

Kilo connects to a stdio MCP server by adding a `mcpServers` entry
to the project-level `kilo.json`:

```json
{
  "mcpServers": {
    "yaah-tui": {
      "type": "stdio",
      "command": "C:\\Users\\gbuch\\.local\\bin\\yaah.exe",
      "args": ["tui", "--mcp"],
      "env": {
        "YAAH_OTEL_ENABLED": "true"
      }
    }
  }
}
```

This tells Kilo to spawn `yaah tui --mcp` as a subprocess and
communicate via stdio JSON-RPC. The TUI opens in the terminal, and
Kilo connects to its MCP server over the same stdio pipe.

**Alternative — HTTP transport** (if stdio proves unreliable on Windows):

```json
{
  "mcpServers": {
    "yaah-tui": {
      "type": "http",
      "url": "http://127.0.0.1:7334/mcp"
    }
  }
}
```

Then start yaah separately: `yaah tui --mcp --mcp-http 127.0.0.1:7334`

## Kilo MCP config reference

Kilo project config lives at `C:\Code\Personal\agentic\yaah\kilo.json`
(or the active workspace root). The `mcpServers` map follows the
[MCP configuration schema](https://spec.modelcontextprotocol.io/).

| Field | Value | Notes |
|-------|-------|-------|
| `type` | `"stdio"` or `"http"` | stdio spawns yaah as a child process; HTTP connects to an already-running server |
| `command` | path to `yaah.exe` | Use absolute path from `scripts/install.ps1` output |
| `args` | `["tui", "--mcp"]` | TUI mode with MCP bridge |
| `env` | `{"YAAH_OTEL_ENABLED": "true"}` | Ensures OTel is active for traces tool |
| If HTTP: `url` | `"http://127.0.0.1:7334/mcp"` | Must match `--mcp-http` argument |

After adding to `kilo.json`, Kilo discovers the tools on next startup.
Run `yaah tui --mcp` in a terminal, then Kilo connects.

### Available tools to Kilo

| Tool | Input | Effect on TUI |
|------|-------|---------------|
| `yaah_prompt` | `message` (string) | TUI shows assistant response with tokens, tool calls, sub-agents |
| `yaah_traces` | `trace_id`?, `tree`? | Queries in-memory OTel spans; TUI unaffected |
| `yaah_status` | none | Returns session metadata; TUI unaffected |
| `yaah_steer` | `text` (string) | Injects [STEER] directive into next LLM turn |
| `yaah_compact` | none | Triggers compaction; TUI shows compaction status |

## Concurrency model

```
TUI goroutine          MCP goroutine(s)         Agent goroutine
     │                      │                       │
     ├─ user types prompt   │                       │
     │  calls RunPrompt() ──┼───────────────────────► runs agent loop
     │                      │                       │   fires events
     │◄── HandleEvent() ────┼───────────────────────┤   (tokens, tools)
     │  renders to TUI      │                       │
     │                      │                       │
     │                      ├─ MCP prompt arrives    │
     │                      │  calls RunPrompt() ───► runs agent loop
     │◄── HandleEvent() ────┼───────────────────────┤   fires events
     │  renders to TUI      │                       │
     │                      │                       │
```

- `agentSession.mu` (RWMutex) protects shared state
- `RunPrompt` is already goroutine-safe (called via `OnSubmit` goroutine)
- MCP tool handlers lock `mu` before accessing session fields
- If two prompts arrive simultaneously, the second waits on the mutex
- The span buffer is internally thread-safe

## Risks

| Risk | Mitigation |
|------|------------|
| Stdio MCP on Windows may have pipe issues | Offer HTTP transport as alternative (`--mcp-http`) |
| Two concurrent prompts (user + MCP) | `agentSession` only allows one `RunPrompt` at a time (mutex on `runPrompt`) |
| TUI exit kills MCP connection | Expected — MCP client should handle disconnect gracefully |
| `extraOtelProcessors` is a package global | Already the pattern in serve.go; works with single-process model |

## Out of scope (future)

- Multi-yaah orchestration (yaah→yaah MCP): each instance would expose its session as an MCP server, and the orchestrator connects via MCP client to delegate work. This is natural once `--mcp` exists on every instance.
- Web UI MCP bridge: `yaah web --mcp` would expose the web session the same way.
- Authentication: currently no auth — MCP over local stdio is implicitly trusted.
