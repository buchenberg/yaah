# Entire.io Integration Plan

Changes needed in yaah to support first-class Entire.io integration via the
external agent protocol. The external agent binary (`entire-agent-yaah`) is
covered separately in the `external-agents` repo; this doc covers only yaah's
own source.

## Overview

Both Kiro and Amp follow the same architecture for Entire.io integration:

```
Agent → native plugin/hook system → Plugin → entire hooks <agent> <event>
                                              ↓
                                       entire-agent-<name> parse-hook
                                              ↓
                                       writes .entire/tmp/<agent>/<session>.jsonl
```

yaah currently has no hook/plugin system. The minimal addition is a
**hook event emitter** that writes structured JSONL to a configurable
directory on session boundaries, turn boundaries, and tool calls. The
`entire-agent-yaah` binary reads these JSONL files for transcript
analysis, session management, and protocol compliance.

## Files Changed

| File | Change |
|---|---|
| `internal/agent/agent.go` | Add `HookDir` field, `emitHook()` helper, 4 call sites |
| `internal/config/load.go` | Add `Hooks` struct, add to `Config` |
| `internal/config/create.go` | Add `hooks:` section to default YAML |
| `cmd/yaah/root_cmd.go` | Add `--approval` / `-a` flag |
| `cmd/yaah/root.go` | Wire `--approval` flag through to `agentSession` |

No files to create. All additions are surgical, under 100 lines total.

## 1. Hook Event Emitter

### 1.1 Loop struct addition

`internal/agent/agent.go`, in the `Loop` struct (after line 119):

```go
// HookDir is the directory where yaah writes JSONL hook event files.
// When set, structured events are appended to <HookDir>/<session-id>.jsonl
// on session boundaries, turn boundaries, and tool calls. Used by
// external agents (e.g. entire-agent-yaah) for checkpoint/transcript
// integration. Empty string means no hook events are written.
HookDir string
```

### 1.2 Helper method

New method on `*Loop`:

```go
// emitHook writes a structured JSONL line to the hook directory.
// It is a no-op when HookDir is empty.
func (l *Loop) emitHook(sessionID string, event HookEvent) {
    if l.HookDir == "" {
        return
    }
    event.SessionID = sessionID
    if event.Timestamp == 0 {
        event.Timestamp = time.Now().UnixMilli()
    }
    line, err := json.Marshal(event)
    if err != nil {
        return // best-effort; hook emission must never break the agent
    }
    path := filepath.Join(l.HookDir, sessionID+".jsonl")
    f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
    if err != nil {
        return
    }
    f.Write(append(line, '\n'))
    f.Close()
}
```

### 1.3 Hook event types

New types file `internal/agent/hookevent.go`:

```go
package agent

// HookEvent is a structured event emitted to the hook directory as JSONL.
// The entire-agent-yaah binary reads these for transcript analysis.
type HookEvent struct {
    Event     string `json:"event"`
    SessionID string `json:"session_id"`
    Timestamp int64  `json:"timestamp_ms"`

    // session.start / session.end
    Model string `json:"model,omitempty"`

    // turn.start
    Prompt string `json:"prompt,omitempty"`
    Turn   int    `json:"turn,omitempty"`

    // tool.start / tool.end
    ToolName string `json:"tool_name,omitempty"`
    ToolArgs string `json:"tool_args,omitempty"`  // JSON string
    // tool.end only
    ToolResult string `json:"tool_result,omitempty"`
    DurationMs int64  `json:"duration_ms,omitempty"`
    ToolError  string `json:"tool_error,omitempty"`

    // session.end only
    ExitReason string `json:"exit_reason,omitempty"`
}
```

### 1.4 Call sites in `Run()`

**Session start** — In `Run()`, after the user message is appended to
messages (after line 147 in the current code, where `l.Messages` is
initialized or appended to):

```go
sessionID := l.resolveSessionID()
l.emitHook(sessionID, HookEvent{
    Event: "session.start",
    Model: l.Model,
})
```

Add a `resolveSessionID()` helper — yaah already creates session IDs in
`cmd/yaah/root_cmd.go:278` as `sess-<unix-nano>`. The `Loop` needs to
know its session ID. Simplest approach: pass it through from the caller
via a new `SessionID string` field on `Loop`, set by `agentSession` in
`root_cmd.go`. The caller already generates it (`sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())`).

Add to `Loop` struct:

```go
SessionID string // stable identifier for the session, set by caller
```

**Turn start** — In `Run()`, at the top of the `for iter` loop (after line 157,
after the `ctx.Done()` select):

```go
l.emitHook(l.SessionID, HookEvent{
    Event:  "turn.start",
    Prompt: userInput,
    Turn:   iter,
    Model:  l.Model,
})
```

**Tool start** — In `executeToolsParallel()`, inside the goroutine for each
tool call (after line 337, before the tool executes):

```go
l.emitHook(l.SessionID, HookEvent{
    Event:    "tool.start",
    ToolName: tc.Function.Name,
    ToolArgs: tc.Function.Arguments,
})
```

**Tool end** — Same method, after the tool result is collected (after line 346,
after duration is computed):

```go
l.emitHook(l.SessionID, HookEvent{
    Event:      "tool.end",
    ToolName:   tc.Function.Name,
    ToolArgs:   tc.Function.Arguments,
    ToolResult: res,
    DurationMs: duration.Milliseconds(),
    ToolError:  errStr,
})
```

Where `errStr` is `""` if `err == nil`, else `err.Error()`.

**Session end** — In `Run()`, before every `return` statement (after the
loop succeeds, after max iterations, after each error return):

```go
exitReason := "completed"
if err != nil {
    exitReason = "error"
}
l.emitHook(l.SessionID, HookEvent{
    Event:      "session.end",
    ExitReason: exitReason,
})
```

This should be in a `defer` at the top of `Run()` to guarantee it fires
even on early returns:

```go
func (l *Loop) Run(ctx context.Context, userInput string) (resp string, err error) {
    // ... setup ...
    defer func() {
        reason := "completed"
        if err != nil {
            reason = "error"
        }
        l.emitHook(l.SessionID, HookEvent{
            Event:      "session.end",
            ExitReason: reason,
        })
    }()
    // ... existing loop ...
}
```

### 1.5 Thread safety

`emitHook` writes to a file. It is called from the main goroutine (session
start/end, turn start) and from goroutines inside `executeToolsParallel`
(tool start/end). The `os.OpenFile` with `O_APPEND` on POSIX is atomic
for writes under `PIPE_BUF` (4096 bytes). yaah's hook events are well under
that limit. For extra safety, a `sync.Mutex` can guard the write — but in
practice, append-only JSONL is safe without locking on Darwin and Linux.

## 2. Config Changes

### 2.1 Add `Hooks` struct

`internal/config/load.go`, after line 27 (after `Defaults` struct):

```go
// Hooks holds configuration for external integrations via JSONL hook events.
type Hooks struct {
    Dir string `yaml:"dir"` // directory for JSONL hook event files
}
```

### 2.2 Add to `Config` struct

`internal/config/load.go`, in the `Config` struct (after line 33):

```go
type Config struct {
    Providers map[string]Provider `yaml:"providers"`
    Default   Defaults            `yaml:"default"`
    Hooks     Hooks               `yaml:"hooks"`
    LogLevel  string              `yaml:"log_level"`
}
```

### 2.3 Add to default YAML

`internal/config/create.go`, add to `defaultConfigYAML` after `log_level:`:

```yaml
# hooks:
#   dir: ~/.yaah/hooks               # optional: JSONL event log for external integrations
```

### 2.4 Resolve home directory in hook path

In `Load()`, expand `~/` and `$HOME/` in `Hooks.Dir`. Add to `Load()` after
the provider env-var substitution (after line 88):

```go
if cfg.Hooks.Dir != "" {
    cfg.Hooks.Dir = expandHomeDir(cfg.Hooks.Dir)
}
```

`expandHomeDir` is a small helper replacing `~` or `$HOME` prefixes:

```go
func expandHomeDir(path string) string {
    if strings.HasPrefix(path, "~/") {
        home, _ := os.UserHomeDir()
        return filepath.Join(home, path[2:])
    }
    if strings.HasPrefix(path, "$HOME/") {
        home, _ := os.UserHomeDir()
        return filepath.Join(home, path[6:])
    }
    return path
}
```

### 2.5 Wire hook dir to agent loop

`cmd/yaah/root_cmd.go`, in `runPrompt()` (around line 392, where `agent.Loop`
is constructed):

```go
loop := &agent.Loop{
    // ... existing fields ...
    HookDir:   cfg.Hooks.Dir,       // added
    SessionID: s.sessionID,         // added
    // ...
}
```

## 3. CLI Flag: `--approval`

### 3.1 Flag definition

`cmd/yaah/root.go`, add a persistent flag:

```go
var approvalOverride string

func init() {
    rootCmd.PersistentFlags().StringVarP(&approvalOverride,
        "approval", "a", "",
        "override approval mode: allow, ask, or deny")
}
```

### 3.2 Apply in `runPrompt()`

`cmd/yaah/root_cmd.go`, in `runPrompt()` (after line 401, where
`ApprovalMode` is set from config):

```go
approvalMode := s.cfg.Default.Approval
if approvalOverride != "" {
    approvalMode = approvalOverride
}
loop := &agent.Loop{
    // ...
    ApprovalMode: approvalMode,
    // ...
}
```

### 3.3 Non-interactive env var

Also support `YAAH_APPROVAL=allow` env var for headless/test runs:

```go
if approvalMode == "" && os.Getenv("YAAH_APPROVAL") != "" {
    approvalMode = os.Getenv("YAAH_APPROVAL")
}
```

Order: CLI flag → env var → config file → built-in default.

## 4. Hook Payload Format (JSONL)

Each line in `<hooks.dir>/<session-id>.jsonl` is a single JSON object:

```json
{"event":"session.start","session_id":"sess-123","timestamp_ms":1234567890000,"model":"gpt-4o-mini"}
{"event":"turn.start","session_id":"sess-123","timestamp_ms":1234567890100,"prompt":"write hello world","turn":0,"model":"gpt-4o-mini"}
{"event":"tool.start","session_id":"sess-123","timestamp_ms":1234567890200,"tool_name":"write","tool_args":"{\"filePath\":\"hello.txt\",\"content\":\"hello world\"}"}
{"event":"tool.end","session_id":"sess-123","timestamp_ms":1234567890500,"tool_name":"write","tool_args":"{\"filePath\":\"hello.txt\",\"content\":\"hello world\"}","tool_result":"File written successfully.","duration_ms":300}
{"event":"turn.start","session_id":"sess-123","timestamp_ms":1234567890600,"prompt":"write hello world","turn":1,"model":"gpt-4o-mini"}
{"event":"session.end","session_id":"sess-123","timestamp_ms":1234567891000,"exit_reason":"completed"}
```

This is the same pattern Amp uses — a flat JSONL file of events. The
`entire-agent-yaah` binary reads these, maps them to protocol event types,
and produces the structured session JSON that Entire.io expects.

## 5. What the external agent binary does with these events

The `entire-agent-yaah` binary (in the `external-agents` repo) implements
the Entire.io external agent protocol. Its `parse-hook` subcommand reads
the JSONL hook file and maps events to protocol event IDs:

| JSONL event | Protocol event type | Description |
|---|---|---|
| `session.start` | 1 (SessionStart) | Session began |
| `turn.start` | 2 (TurnStart) | User submitted a prompt |
| `turn.start` with tool results preceding | 3 (TurnEnd) | Agent finished a response round |
| `session.end` | 5 (SessionEnd) | Session terminated |

The binary's `read-transcript` subcommand returns the raw JSONL file.
`extract-modified-files` parses tool.end events with tool_name=write/edit/delete.
`extract-prompts` parses turn.start events.
`compact-transcript` reformats JSONL to the checkpoint format.

## 6. E2E Test Integration

The e2e adapter in `external-agents/e2e/agents/yaah.go` runs:

```bash
yaah --approval allow "write a file called hello.txt with 'hello world'"
```

Then waits for the tool call to complete and validates:
- `entire enable --agent yaah` succeeds
- `are-hooks-installed` returns true
- Session file exists in `.entire/tmp/yaah/`
- Checkpoint is created after commit
- Rewind restores pre-commit state

The `--approval allow` flag ensures yaah runs without interactive prompts
during headless tests.

## 7. Rollout

### Phase 1: Hook emission (this plan)

Add `HookDir`, `SessionID` to `Loop`, add `emitHook()` helper, wire 4 call
sites. Add config + CLI flag. ~80 lines of Go. Zero behavior change when
`hooks.dir` is empty (the default).

### Phase 2: External agent binary

Create `entire-agent-yaah` in the `external-agents` repo following the
Kiro/Amp template. The binary reads yaah's JSONL hook files and SQLite DB.
~600 lines of Go. Covered in the external-agents repo, not here.

### Phase 3: E2E tests

Add `e2e/agents/yaah.go` adapter to the `external-agents` e2e harness.
~100 lines of Go. Covered in the external-agents repo, not here.

## 8. Default behavior

When `hooks.dir` is unset (the default in the scaffold config), yaah
behaves identically to current behavior — `emitHook` is a no-op. Users
who don't use Entire.io see zero change. No performance impact (the
`HookDir == ""` check is a single string comparison per event).
