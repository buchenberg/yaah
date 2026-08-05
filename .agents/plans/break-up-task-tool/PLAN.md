---
name: break-up-task-tool
description: Split the 590-line internal/tools/task.go into four files — sub-agent context plumbing, the sub-agent params/output contract, the task tool schema, and TaskTool execution — all same-package moves with zero import or test changes.
status: draft
---

# Break Up task.go

## Context

`internal/tools/task.go` is **590 lines** (>500 = should-split per
`docs/code-organization.md`) and mixes three unrelated concerns behind one
filename:

1. **Sub-agent context plumbing** (lines 18-111) — four context-key
   protocols (model pointer, start notifier, usage accumulator, heartbeat)
   plus `ErrStuckChild`. This is a *contract between `internal/tools`,
   `internal/agent`, and `cmd/yaah`* — consumed by `agent_tools.go`
   (spawn_subagent wiring), `loop.go` (`SendHeartbeat` per iteration), and
   `subagent_runner.go` (writes back model/usage). It is not TaskTool logic.
2. **Sub-agent I/O contract** (lines 113-188) — `SubAgentParams`,
   `Escalation`/`EscalationSeverity`, `SubAgentOutput`, and
   `ParseSubAgentOutput` (escalation-fence regex parsing).
3. **TaskTool itself** (lines 190-590) — struct, schema building
   (66-line `BuildTaskSchema` + 20-line legacy static schema), 131-line
   `Execute`, clamps, timeout resolution, structured result builders.

### Current map

| Lines | Content | Destination |
|---|---|---|
| 18-28 | `subAgentModelKey` (legacy string const), `SubAgentModelFromContext` | subagent_ctx.go |
| 32-45 | model-pointer key, `WithSubAgentModelPtr`, `WriteSubAgentModel` | subagent_ctx.go |
| 50-65 | start-notifier key, `WithSubAgentStartNotifier`, `NotifySubAgentStart` | subagent_ctx.go |
| 69-84 | usage key, `WithSubAgentUsage`, `AddSubAgentUsage` | subagent_ctx.go |
| 89-107 | heartbeat key, `WithSubAgentHeartbeat`, `SendHeartbeat` | subagent_ctx.go |
| 111 | `ErrStuckChild` | subagent_ctx.go |
| 116-136 | `SubAgentParams` | subagent_output.go |
| 139-155 | `EscalationSeverity` + consts, `Escalation` | subagent_output.go |
| 159-188 | `SubAgentOutput`, `escalationPattern`, `ParseSubAgentOutput` | subagent_output.go |
| 193-198 | `TaskRunner`, `BackgroundResultNotifier` types | task.go |
| 212-252 | `TaskTool` struct | task.go |
| 253-277 | `Name`, `Description`, `Schema` (legacy static branch) | task_schema.go |
| 282-301 | `roleNames()` | task_schema.go |
| 306-371 | `BuildTaskSchema` | task_schema.go |
| 373-503 | `Execute` | task.go |
| 510-543 | `clampTimeoutSeconds`, `clampMaxLoopCycles`, `resolveTaskTimeout` | task.go |
| 548-586 | `structuredBackgroundResult`, `structuredTaskResult` | task.go |
| 588-590 | `coerceInt` | task.go |

### Consumers (all keep working unchanged)

- `internal/agent/agent_tools.go` — `WithSubAgentModelPtr`,
  `WithSubAgentStartNotifier`, `WithSubAgentUsage`, `WithSubAgentHeartbeat`,
  `ParseSubAgentOutput`, `ErrStuckChild`
- `internal/agent/loop.go:93` — `tools.SendHeartbeat`
- `cmd/yaah/subagent_runner.go` — `WriteSubAgentModel`,
  `NotifySubAgentStart`, `AddSubAgentUsage`
- `internal/agent/task_test.go` — exercises Execute/clamps through the tool

Everything is referenced as `tools.X` from other packages; all moves are
**within `package tools`**, so no import, signature, or test change is
required anywhere.

### Baseline

```
go build ./...                        → ok
go test ./internal/tools/... ./internal/agent/... -count=1 → ok
```

## Target layout

```
internal/tools/
├── subagent_ctx.go      ~105   four context-key protocols + ErrStuckChild
├── subagent_output.go   ~90    SubAgentParams, Escalation, SubAgentOutput,
│                               ParseSubAgentOutput
├── task.go              ~330   TaskTool struct, Execute, clamps, timeouts,
│                               structured results, coerceInt
└── task_schema.go       ~100   Schema(), roleNames(), BuildTaskSchema()
```

All files < 350 lines. ✅

---

## Steps (one commit each, mechanical moves only)

Same-package cut/paste; **build after each step**. No logic changes, no
renames, no doc-comment edits beyond moving them with their symbols.

### Step 1: `subagent_ctx.go`

Move lines 18-111 verbatim (imports: `context`, `errors`, `types`).
Add a file-header comment:

```go
// subagent_ctx.go defines the context-key contract between the task tool,
// the sub-agent runner, and the agent loop: model writeback, start
// notification, usage accumulation, and stuck-child heartbeats. These
// keys are set by the spawner and read/written by the runner and loop.
```

Commit: `refactor(tools): extract sub-agent context plumbing`.

### Step 2: `subagent_output.go`

Move lines 113-188 (`SubAgentParams` through `ParseSubAgentOutput`;
imports: `encoding/json`, `errors` (none — runErr is passed in), `regexp`).
Note: `SubAgentParams` is referenced by `TaskRunner`/`Execute` — same
package, no change.

Commit: `refactor(tools): extract sub-agent params and output contract`.

### Step 3: `task_schema.go`

Move `Schema` (253-277), `roleNames` (279-301), `BuildTaskSchema`
(303-371). Imports: `encoding/json`, `fmt`, `slices`, `strings`.

Commit: `refactor(tools): extract task tool schema building`.

### Step 4: Rename remainder, quality gates, delete nothing

`task.go` is now ~330 lines and cohesive — keep the name. Verify:

```powershell
gofmt -l internal/tools/                # empty
go vet ./internal/tools/...
staticcheck ./internal/tools/...
go test ./internal/tools/... ./internal/agent/... -count=1   # no test edits
go test ./... -count=1                  # full suite
git diff main -w -- internal/tools/task.go internal/tools/subagent_ctx.go internal/tools/subagent_output.go internal/tools/task_schema.go
# must show only moved code
```

Commit (if step 3 touched formatting): fold into step 3.

## Observations (out of scope, file as follow-ups)

- **F1 — two context-key styles coexist**: `subAgentModelKey` uses the
  legacy string-typed `contextKey` const (line 20) while the other three
  use struct keys. Unify on struct keys in a follow-up; the legacy key's
  reader (`SubAgentModelFromContext`) appears unused by the current runner
  path — confirm before removing.
- **F2 — naming drift**: file/tool is "task" but the tool name is
  `spawn_subagent` and a warning string references "the delegate tool"
  (Execute, line ~430). Cosmetic; not part of this split.
- **F3 — 17-argument `newTaskTool`** in `cmd/yaah/subagent_runner.go:23`
  constructs this tool; already tracked as a Phase-2 follow-up of the
  `break-up-agent-frame` plan.

## What does NOT change

- Any exported symbol name, signature, or behavior.
- `internal/agent`, `cmd/yaah`, tests — zero edits expected.
- Schema content (both legacy static and dynamic role-enum variants).
- Execute semantics: clamping, background-mode goroutine/cancellation
  handling, structured error results.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Missed symbol during move | Low | Compiler errors name it; build per step |
| Doc comments separated from symbols | Low | Move comments with their symbols verbatim |
| Context-key constants/types duplicated by accident | Low | Redeclaration is a compile error |

## Rollback

Each step is an independent commit; revert in reverse order. Pure code
movement — no state, API, or behavior changes.
