---
name: yaah-dev-loop
description: Build, run, and iterate on the yaah MCP server from inside a Kilo session. Use when developing yaah itself (internal/mcp, cmd/yaah/serve, prompt/tools) and you want to exercise the change through a live MCP connection without restarting the host agent.
version: 1.0.0
author: local
license: MIT
platforms: [windows, linux, macos]
prerequisites:
  commands: [go, powershell or bash]
  env_vars: [DEEPSEEK_API_KEY or another provider key yaah is configured to use]
metadata:
  hermes:
    tags: [yaah, mcp, dev-loop, hot-reload, http, sse, otel]
---

# yaah Dev Loop (MCP hot-reload via HTTP+SSE)

The dev loop: **edit yaah source → `go build` → restart `yaah serve --http` → call
MCP tools from Kilo → inspect OpenTelemetry spans**. Kilo itself does **not**
restart — the running yaah process is just swapped underneath it.

This is a port of Karpathy's [autoresearch](https://github.com/karpathy/autoresearch)
philosophy to MCP server development: one process to modify, one fixed restart
ritual, one comparable signal (the next MCP tool call returns the new behavior).

---

## Prerequisites

| Requirement | Why |
|---|---|
| Go 1.25+ | yaah uses Go 1.25+ generics and `log/slog` patterns |
| Kilo with `mcp__yaah__*` tools loaded | otherwise there is nothing to call |
| Port `127.0.0.1:7333` free | the MCP server bind address (configurable) |
| `~/.yaah/config.yaml` with a working provider | needed for the `prompt` tool to actually call an LLM |
| `git` | for diffing what you changed |

The dev loop itself does **not** require an external OTel collector — yaah's
`serve` mode installs an in-memory `BufferingSpanProcessor` so `yaah_traces`
works standalone.

---

## One-time setup (per machine)

### 1. Build yaah and put it on PATH

```bash
go build -trimpath -ldflags '-s -w' -o yaah.exe .
go install ./...   # optional, populates $GOBIN
```

Both `C:\Code\Personal\agentic\yaah\yaah.exe` (repo) and `C:\Users\gbuch\go\bin\yaah.exe`
(`go install` target) must point at the **same** binary. Kilo may resolve either
depending on which config it loads — keep them in sync or remove the stale copy.

### 2. Configure Kilo to talk to yaah over HTTP

In `~/.config/kilo/kilo.json` (global) or `kilo.json` at the repo root:

```json
{
  "mcp": {
    "yaah": {
      "type": "remote",
      "url": "http://127.0.0.1:7333/mcp"
    }
  }
}
```

Then **fully restart Kilo** (`Ctrl+C` then `kilo`). Kilo spawns MCP clients once
at process start; config changes are not picked up by a new session alone.

### 3. Start the dev-loop server

Pick one port — `7333` is the convention. Run from the repo root:

```bash
yaah serve --http 127.0.0.1:7333
```

You should see:

```
yaah serve: starting MCP tool server (HTTP at 127.0.0.1:7333/mcp)...
  provider: <your-provider>/<model>
  session: sess-...
```

Keep this process running in a dedicated terminal. It survives Kilo restarts but
**not** a machine reboot — relaunch it after reboots.

---

## The loop (single iteration)

```bash
# 1. Edit source (anywhere under cmd/, internal/, main.go)
# 2. Rebuild
go build -o yaah.exe .                                # ~3 s cold, <1 s warm

# 3. Swap the running process (Windows PowerShell)
Get-Process yaah | Stop-Process -Force
yaah.exe serve --http 127.0.0.1:7333 &                 # background

# (Linux/macOS)
pkill -f 'yaah serve --http' && ./yaah serve --http 127.0.0.1:7333 &
```

Total swap time: **~1 s** (kill + relaunch). Kilo's MCP client sees the new
binary on the next tool call. No Kilo restart.

### What to call

| Tool | Purpose | Latency |
|---|---|---|
| `mcp__yaah__status` | Cheap smoke test — `pid` field confirms you got the new binary | <100 ms |
| `mcp__yaah__prompt` | Multi-turn agent run; first call also lazily builds the session | 5–30 s |
| `mcp__yaah__traces` | Query the in-memory OTel ring buffer; supports `trace_id` + `tree: true` | <50 ms |

Always start with `status`. If the `pid` field matches your new build, the
swap succeeded. Only then burn tokens on `prompt`.

### The autoresearch discipline

Karpathy's pattern: **agent edits, agent runs, agent judges, human reviews the
log.** Apply it here:

- Make **one** observable change per iteration.
- Use `status` to confirm the change is live (don't just trust `go build`).
- Use `traces` with `tree: true` to read the span hierarchy before judging
  behavior — what's the slowest tool, which turn blew up, did `prune` actually
  reclaim tokens?
- Use `prompt` for end-to-end checks, not micro-benchmarks.
- Do **not** trust the model. The `prune` middleware has been observed running
  with `candidates=0, marked=0` for every span — always check the trace data,
  not the model's narrative.

---

## Comparison to autoresearch

| | autoresearch | yaah-dev-loop |
|---|---|---|
| What the agent edits | one Python file | any yaah `.go` file |
| Time budget per experiment | fixed 5 min wall clock | swap time ≈ 1 s, `prompt` runs are unbounded |
| Primary metric | `val_bpb` (lower is better) | `mcp__yaah__status.pid` change + `traces` shape |
| Storage | results table, no DB | in-memory OTel ring (~1k spans) |
| Human review | wake up to a log | inspect spans after each `prompt` call |
| Iteration rate | ~12/hour | ~60/hour (most of the time is LLM, not build) |

The **philosophy is identical**: minimize the cost of one experiment so you can
do many, and keep the experiment self-contained so results are comparable.

---

## Troubleshooting

### `mcp__yaah__*` tools missing from Kilo

Most common cause: Kilo started before the server, or Kilo cached a previous
connection failure (e.g. from before HTTP+SSE support existed).

1. Verify the server is up: `curl http://127.0.0.1:7333/health` → `{"name":"yaah","status":"ok"}`.
2. If yes, fully restart Kilo (`Ctrl+C` + `kilo`). New sessions alone do not
   re-spawn MCP clients.

### Kilo logs `SSE error: Non-200 status code (405)`

Kilo's MCP client opens `GET /mcp` to open an SSE stream. The server must
return `200 text/event-stream`. If you see 405, you are running a yaah binary
that was built before `internal/mcp/http_server.go` supported the legacy
HTTP+SSE transport. Rebuild:

```bash
go build -o yaah.exe .
Get-Process yaah | Stop-Process -Force
yaah.exe serve --http 127.0.0.1:7333
```

Verify with:

```bash
curl -i -H 'Accept: text/event-stream' http://127.0.0.1:7333/mcp
# expect: HTTP/1.1 200 OK + Content-Type: text/event-stream
```

### Kilo logs `Operation timed out after 30000ms`

yaah responded but the response was missing `id`. JSON-RPC requires the
response to echo the request id even when it's `0`. Some MCP clients send
`initialize` with `id: 0`. If the server's `JSONRPCMessage.ID` field still has
the `omitempty` tag, the response will silently drop the id and the client will
hang waiting for it.

Fix is in `internal/mcp/framing.go`: `ID int64 \`json:"id"\`` (no omitempty).
Rebuild after any change there.

### `yaah serve --http` exits immediately with `unknown command serve`

The binary on `PATH` is older than your checkout. Check both:

```bash
Get-Item C:\Code\Personal\agentic\yaah\yaah.exe      # repo build
Get-Item C:\Users\gbuch\go\bin\yaah.exe               # go install target
```

The `go install` target is the one Kilo's `local` MCP config uses if it resolves
through PATH. Either keep both in sync (`Copy-Item` after every `go build`),
or switch the Kilo config to a `remote` URL pointing at the server you run
yourself (recommended — the whole point of this skill).

### Stale binary at `C:\Users\gbuch\go\bin\yaah.exe`

Older Kilo sessions and orphaned `yaah serve` processes can leave a binary on
disk that lacks `serve`. Symptoms: command not found, immediate exit, Kilo
30 s timeout. Fix:

```bash
Get-Process yaah -ErrorAction SilentlyContinue | Stop-Process -Force
Copy-Item -Force C:\Code\Personal\agentic\yaah\yaah.exe C:\Users\gbuch\go\bin\yaah.exe
yaah --version   # confirm commit hash matches your checkout
```

### Server keeps an old `pid` in `status`

You swapped but Kilo is still talking to a child process, or you forgot to
kill the previous `yaah serve`. Verify only one `yaah.exe` is running:

```bash
Get-Process yaah | Format-Table Id, StartTime
```

Should show **exactly one** row. If more than one, kill the older ones.

### `prompt` tool returns `session init: ...`

The first `prompt` call after a swap is slow (5–10 s) because it lazily builds
the agent session: loads config, opens the SQLite DB, discovers MCP servers,
indexes skills. If it then errors, run `yaah doctor` from the repo to surface
config problems.

### `traces` returns empty

Either no `prompt` has run in this session yet, or the in-memory ring has been
flushed (process restart with no new calls). Run a `prompt` first, then query
traces within the same process lifetime. The ring holds the last ~1k spans
across all sessions hosted by one `yaah serve` process.

### Tests fail after edit but `go build` is clean

`go build` does not run tests. The MCP HTTP+SSE layer has tests in
`internal/mcp/http_server_test.go` — run them:

```bash
go test ./internal/mcp/... -run TestHTTP -v
go test ./...
```

CI parity is `gofmt -l . && go vet ./... && go test ./... && staticcheck ./...`.

### Port 7333 already in use

Either another yaah is running, or something else owns the port. Pick a new
port and update both the server flag and `kilo.json`:

```bash
yaah serve --http 127.0.0.1:7334
# then edit kilo.json: "url": "http://127.0.0.1:7334/mcp"
# restart Kilo once
```

---

## Sanity script (run once after setup)

Save as `scripts/dev-loop-smoke.ps1` and run from the repo root:

```powershell
# Build, start, exercise the three tools, kill, report
$ErrorActionPreference = 'Stop'
go build -o yaah.exe . | Out-Null
$port = 7333
$proc = Start-Process .\yaah.exe -ArgumentList "serve --http 127.0.0.1:$port" `
    -RedirectStandardOutput nul -RedirectStandardError nul -PassThru -NoNewWindow
try {
    Start-Sleep -Seconds 1
    $health = (Invoke-WebRequest "http://127.0.0.1:$port/health" -UseBasicParsing).Content
    $init   = (Invoke-WebRequest "http://127.0.0.1:$port/mcp" -Method Post `
        -ContentType "application/json" `
        -Body '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}' `
        -UseBasicParsing).Content
    if ($init -match '"protocolVersion"') {
        Write-Host "[ok] health + initialize handshake work" -ForegroundColor Green
    } else {
        Write-Host "[FAIL] initialize response missing protocolVersion" -ForegroundColor Red
    }
} finally {
    Stop-Process $proc -Force -ErrorAction SilentlyContinue
}
```

If green, the dev loop is ready.