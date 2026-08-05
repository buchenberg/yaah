---
name: break-up-agent-frame
description: Split the 1005-line cmd/yaah/agent_frame.go god file into six focused files — session contract, session wiring, resume logic, per-prompt loop runner, the standalone :compact command, and the REPL terminal view — with the 340-line constructor decomposed into named steps.
status: draft
---

# Break Up agent_frame.go God File

## Context

`cmd/yaah/agent_frame.go` is the largest remaining non-TUI file at
**1005 lines** (>800 = must-split per `docs/code-organization.md`). It is
the single choke point where every UI driver (REPL, TUI, web, serve, acp)
meets the agent loop: it owns the `Session` contract, 340 lines of
infrastructure wiring, session-resume logic, per-prompt loop construction,
a standalone compaction implementation, and the REPL terminal event view.

`docs/code-organization.md` §3 sketched this split (session.go / wiring.go /
loop_builder.go / tools.go) but was never executed. This plan replaces that
sketch with a line-verified version. Some of its targets already happened
organically: provider resolution lives in `provider_resolve.go`, and
`newTaskTool`/role helpers live in `subagent_runner.go`.

### Content map (verified line numbers)

| Lines | Content | Problem |
|---|---|---|
| 1-30 | "UI Driver Pattern" doc comment | Keep verbatim |
| 53-66 | `Session` interface | Contract — belongs with struct |
| 74-106 | `agentSession` struct (25 fields) | Struct itself is fine |
| 116-454 | `newAgentSessionWithOptions` — **340-line constructor** | 🔴 Nine unrelated wiring steps in one function |
| 456-477 | `close()` | Lifecycle |
| 483-530 | `buildToolQuickRef`, `schemaSignature` | Pure prompt helpers |
| 532-618 | Session method impls (Close…SetModel) | Small accessors + channel ops |
| 620-743 | `compactContext()` — **124-line standalone compaction** | ⚠️ Duplicates `agent.Loop.compactContext` (see F1) |
| 745-774 | `reloadRoles()` | Role hot-reload |
| 776-880 | `terminalView` — REPL `agent.View` impl | Unrelated to session wiring |
| 882-1005 | `runPrompt()` — loop construction + run + writeback | Per-turn orchestration |

### Findings

- **F1 — Two compaction implementations.** `agentSession.compactContext`
  (the `:compact` command path) re-implements summarization independently of
  `agent.Loop.compactContext`: chars/4 estimate, keep-6-recent split,
  `ReasoningProtect` guard, `SmallModel` summary call, trim fallback. It
  bypasses the loop compactor's cooldowns, adaptive budget, chunked fallback,
  events, and OTel metrics. **Unifying them is out of scope** (it depends on
  `agent-context-split` Phase 4's compaction sub-package); this plan only
  relocates the method and documents the duplication.
- **F2 — No unit tests cover this file.** `cmd/yaah` tests cover config,
  doctor, redaction, role discovery, subagent runner — not the session.
  The split is therefore unconstrained by test edits but must be verified
  via build + full suite + a one-shot smoke run.
- **F3 — Helpers already external.** `resolveProvider/Model/Compact/Fallback/
  SubAgent/Approval`, `makeProvider`, `buildStuckChildTimeouts`
  (provider_resolve.go); `newTaskTool`, `builtinRoleFiles`, `roleSearchPaths`
  (subagent_runner.go); `skillSearchPaths` (skill.go); `planSearchPaths`
  (plan.go); `resumeSessionID` (root.go); `extraOtelProcessors`,
  `otelInMemoryOnly` (serve.go). The split must not re-move these.

### Consumers of the Session contract

`repl_loop.go`, `tui.go`, `serve.go`, `web.go`, `acp.go`, `session.go`
(one-shot), `update.go`-adjacent login helpers. All depend only on the
`Session` interface + `newAgentSession()` — both keep their signatures.

### Baseline

```
go build ./...                     → ok (main @ 496d422)
go test ./... -count=1             → ok
smoke: yaah -p "reply with ok"     → one-shot works (repeat after each step)
```

---

## Target layout

```
cmd/yaah/
├── session.go           ~200   UI-driver contract: doc comment, Session
│                               interface, agentSession struct, interface
│                               methods, close()
├── wiring.go            ~250   newAgentSession + named wiring steps
├── resume.go            ~110   session restore/create from DB
├── build_loop.go        ~140   runPrompt: per-turn loop construction
├── compact_cmd.go       ~130   agentSession.compactContext + reloadRoles
├── view_terminal.go     ~110   terminalView (REPL agent.View)
├── quickref.go          ~55    buildToolQuickRef + schemaSignature
└── agent_frame.go       DELETED
```

All files < 300 lines. The 340-line constructor becomes a ~60-line
orchestrator calling named steps.

---

## Phase 1 — Mechanical moves (6 commits, zero behavior change)

Branch `break-up-agent-frame` from clean main. Same-package cut/paste;
build after every step. No public API changes: `Session`,
`newAgentSession()`, `newAgentSessionWithOptions()`, all method signatures
frozen.

### Step 1: `quickref.go`

Move `buildToolQuickRef` (483-499) and `schemaSignature` (501-530).
Imports: `encoding/json`, `strings`, `tools`, `prompts`.
Leaf move — nothing else in the file calls these except the constructor.
Commit: `refactor(cmd): extract tool quick-ref builder`.

### Step 2: `view_terminal.go`

Move `terminalView` struct + `newTerminalView` + `start` + `HandleEvent`
(776-880). Imports: `fmt`, `os`, `sync`, `agent`, `subagent`, `spinner`.
Depends on package-local color helpers (`Bold`, `Dim`, `replYellow`,
`replRed`, `formatDuration` — defined elsewhere in cmd/yaah), no move needed.
Commit: `refactor(cmd): extract REPL terminal view`.

### Step 3: `compact_cmd.go`

Move `agentSession.compactContext` (620-743) and `reloadRoles` (745-774).
At the top of the file, add a doc comment recording F1:

```go
// compact_cmd.go implements the interactive :compact command and role
// hot-reload. NOTE: compactContext is a standalone summarizer that
// predates the agent loop's compactor; it does NOT share cooldowns,
// adaptive budgets, chunked fallback, or events with
// agent.Loop.compactContext. Unification is tracked by the
// agent-context-split plan (compaction sub-package phase).
```

Imports: `context`, `fmt`, `os`, `strings`, `agent`, `prompts`,
`subagent`, `types`.
Commit: `refactor(cmd): extract :compact command and role reload`.

### Step 4: `build_loop.go`

Move `runPrompt` (882-1005). Imports: `context`, `time`, `agent`,
`memory`, `observability`, `providers`, `types`.
Commit: `refactor(cmd): extract per-prompt loop runner`.

### Step 5: `session.go`

Move: the UI Driver Pattern doc comment (1-30 — adapted as the file's
header), `Session` interface (53-66) + compile-time check (69),
`agentSession` struct (71-106), `close()` (456-477), and the interface
method implementations (532-618: `Close`, `Compact`, `ProviderName`,
`ModelName`, `MCPInfos`, `RunPrompt`, `Steer`, `FollowUp`, `sendCtrl`,
`SetView`, `SetCtrlCh`, `GetCtrlCh`, `SetApproveFn`, `SetModel`).
Imports: `context`, `fmt`, `sync`, `agent`, `config`, `mcp`, `memory`,
`tools`, `types`.
Commit: `refactor(cmd): extract Session contract and agentSession core`.

### Step 6: `resume.go`, then restructure wiring, delete agent_frame.go

1. Move the session restore/create block — the `resumeSessionID` branch
   (282-341) — into `resume.go` as:

   ```go
   // restoreSession loads a prior session from the DB (resumeSessionID)
   // or creates a new one. Returns messages, sessionID, msgIdx, and a
   // possibly-replaced systemPrompt.
   func restoreSession(db *memory.DB, systemPrompt string) (messages []types.Message, sessID string, msgIdx int, prompt string, err error)
   ```

   The constructor calls it in place of the inline block. This is the only
   step that *restructures* rather than moves; keep logic identical.

2. Move `newAgentSession` + `newAgentSessionWithOptions` into `wiring.go`,
   decomposing the 340-line body into named steps (same order, same logic):

   | Helper | Covers | ~Lines |
   |---|---|---|
   | `setupOTel(cfg, skipOtel) (func(context.Context) error, bool, error)` | 131-168 | 38 |
   | `loadRoleRegistry(cwd)` | 172-181 | 10 |
   | `buildPromptLayers(cfg, cwd, db) prompts.Layers` | 183-189 + memory injection 195-206 | 30 |
   | `registerCoreTools(toolReg, db, cfg) *todo.Store` | 208-212, 245-280 | 55 |
   | `startMCP(cfg, toolReg, skipMCP) (clients, infos, error)` | 220-243 | 25 |
   | `wireTaskTool(...) *tools.TaskTool` | 348-385 | 40 |
   | constructor body | orchestration + struct return | ~60 |

   Memory-guidelines prompt append (216-218) stays inline next to
   `prompts.Build` — it's prompt policy, not wiring.

3. `agent_frame.go` is now empty → **delete it**.

Commit: `refactor(cmd): extract session resume, restructure wiring, delete agent_frame.go`.

### Step 7: Quality gates

```powershell
gofmt -l cmd/yaah/                 # empty
go vet ./cmd/yaah/...
staticcheck ./cmd/yaah/...
go build ./...
go test ./... -count=1             # full suite — no test edits expected
# smoke: one-shot + REPL + TUI startup
yaah -p "reply with exactly: ok"
```

Optional: add `quickref_test.go` unit tests for `schemaSignature`
(required/optional params, empty schema, invalid JSON) — it was previously
untestable inline; cheap win while the code is moving.

---

## Phase 2 — Follow-ups (separate PRs, not part of this split)

1. **Compaction unification** (depends on `agent-context-split` Phase 4):
   make `:compact` delegate to the shared compactor instead of F1's
   parallel implementation. The relocated doc comment marks the spot.
2. **Constructor dependency graph**: `wireTaskTool` currently takes a
   17-argument call (line 357). After this split it is visible as its own
   function — candidate for a `TaskToolConfig` struct in a follow-up.
3. **Update `docs/code-organization.md`**: mark §3 and Future-Work item 4
   done; update the large-files table (agent_frame.go row).

## What does NOT change

- `Session` interface and `newAgentSession`/`newAgentSessionWithOptions`
  signatures — all six UI drivers untouched.
- Wiring order and semantics (OTel before roles, MCP tools before skills
  index, todo store before task tool, prompt assembly last).
- The serve-mode OTel globals (`extraOtelProcessors`, `otelInMemoryOnly`)
  and how they flow into `setupOTel`.
- `provider_resolve.go`, `subagent_runner.go` — already split, untouched.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Package-init or global-var ordering breaks (serve-mode OTel vars) | Low | Step 6 keeps references, not definitions; build+smoke each step |
| `restoreSession` signature loses a nuance (systemPrompt replacement) | Medium | Four explicit return values incl. prompt; caller assigns all |
| No unit tests means silent regression | Medium | Per-step builds, full suite, one-shot + REPL + TUI smoke per step |
| Decomposing the constructor changes evaluation order | Medium | Helpers called in the exact original order; diff review with `-w` |
| Dirty working tree on main | Low | Branch from clean base only |

## Rollback

Each step is an independent commit; revert in reverse order restores the
monolith. No state, no schema, no API — pure code movement.
