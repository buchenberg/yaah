# Plan: Dead Code Cleanup — yaah Codebase

## Goal
Remove all confirmed dead code from the yaah codebase to reduce maintenance burden, improve readability, and eliminate confusion about what's actually in use.

## Scope
20 dead code items identified via codebase-wide grep analysis. No false positives included.

---

## Step 1: Delete dead file — `internal/tools/terminal_output.go`

**File:** `internal/tools/terminal_output.go`
**Action:** Delete entire file.

**Contents removed:**
- `GetTerminalOutputTool` struct
- `SnapshotFunc` type
- `StripANSI` function
- Tool name `get_terminal_output`

**Risk:** None — never instantiated, never registered in `leafTools`.

---

## Step 2: Delete dead overflow utilities — `internal/agent/llm/overflow.go`

**File:** `internal/agent/llm/overflow.go`
**Action:** Delete entire file.

**Contents removed:**
- `IsOverflowError(err error) bool` (line 56)
- `IsPayloadTooLarge(err error) bool` (line 69)
- `WrapOverflowError(err error) error` (line 82)
- `SummarizeError(err error) string` (line 92)

**Risk:** Low — these were scaffolding for overflow recovery that was never wired up. The `errorclassify.IsContextOverflow` function (in `internal/agent/errorclassify/`) is the one actually used and is NOT dead.

**Note:** If OPT-2 (overflow recovery) from `optimization-compaction-1.md` is implemented later, these functions would need to be recreated or equivalent logic added to the retry path.

---

## Step 3: Remove dead exported functions

### 3a. `providers.EstimateTokens`
- **File:** `internal/providers/providers.go`, lines 160-169
- **Action:** Delete function.
- **Risk:** None — zero callers.

### 3b. `subagent.Names`
- **File:** `internal/agent/subagent/role.go`, lines 65-75
- **Action:** Delete package-level function.
- **Risk:** None — `RoleRegistry.Names()` (different method) is the one actually used.

### 3c. `subagent.IsSpawnCapable`
- **File:** `internal/agent/subagent/role.go`, lines 49-56
- **Action:** Delete method.
- **Risk:** Low — only referenced in test files. Delete method and update/remove any tests that call it.

---

## Step 4: Remove dead methods on live types

### 4a. `agentSession.SetSystemPrompt`
- **File:** `cmd/yaah/agent_frame.go`, line 456
- **Action:** Delete method.
- **Risk:** None — not part of any interface, zero callers.

### 4b. `Model.updateCommandSuggestions`
- **File:** `internal/tui/tui.go`, line 595
- **Action:** Delete empty no-op method.
- **Risk:** None — never called.

### 4c. `ContextManager.ResetPruner`
- **File:** `internal/agent/context_manager.go`, line 80
- **Action:** Delete method.
- **Risk:** None — zero callers.

### 4d. `ContextManager.PruneFilter`
- **File:** `internal/agent/context_manager.go`, line 88
- **Action:** Delete method.
- **Risk:** None — zero callers.

---

## Step 5: Remove dead option functions

### 5a. `WithPermissionRules`
- **File:** `internal/agent/options.go`, line 142
- **Action:** Delete function.
- **Risk:** None — zero callers. `PermissionMiddleware` still works via direct field assignment.

### 5b. `WithApproveFn`
- **File:** `internal/agent/options.go`, line 147
- **Action:** Delete function.
- **Risk:** None — `loop.ApproveFn` is set directly in `agent_frame.go`, bypassing this option.

---

## Step 6: Remove dead types/interfaces

### 6a. `providers.ModelLister`
- **File:** `internal/providers/providers.go`, lines 85-88
- **Action:** Delete interface.
- **Risk:** None — `ListModels` is called directly on `*OpenAIClient`, never through this interface.

### 6b. `footerKeyMap` + `footerBindings`
- **File:** `internal/tui/keymap.go`, lines 95-113
- **Action:** Delete `footerKeyMap` struct, `ShortHelp()`, `FullHelp()`, and `footerBindings()` function.
- **Risk:** None — `footerKeyMap{}` is never instantiated.

---

## Step 7: Remove dead color helpers

### 7a. `repl.Green` and `repl.Yellow`
- **File:** `internal/repl/color.go`, lines 34-35
- **Action:** Delete both functions.
- **Risk:** None — `repl.Bold`, `repl.Dim`, and `repl.Cyan` are the ones actually used.

---

## Step 8: Remove stale orphaned code

### 8a. `var _ = os.Stdout`
- **File:** `cmd/yaah/update.go`, line 125
- **Action:** Delete line and its comment on line 124.
- **Risk:** None — `os` is already imported and used elsewhere in the file.

### 8b. Orphaned comment blocks in `tui.go`
- **File:** `internal/tui/tui.go`, lines 1596-1619 and line 1727
- **Action:** Delete all orphaned comment blocks left behind when rendering logic was refactored into `render.go`, `palette_component.go`, `message_component.go`.
- **Risk:** None — comments only, no executable code.

---

## Step 9: Verify build

Run `go build ./...` from `C:\Code\Personal\agentic\yaah` to confirm no compilation errors.

---

## Step 10: Verify tests pass

Run `go test ./...` from `C:\Code\Personal\agentic\yaah` to confirm no test failures (especially `subagent` tests that referenced `IsSpawnCapable`).

---

## Files Modified

| File | Action |
|---|---|
| `internal/tools/terminal_output.go` | DELETE |
| `internal/agent/llm/overflow.go` | DELETE |
| `internal/providers/providers.go` | Remove `EstimateTokens` func, `ModelLister` interface |
| `internal/agent/subagent/role.go` | Remove `Names` func, `IsSpawnCapable` method |
| `cmd/yaah/agent_frame.go` | Remove `SetSystemPrompt` method |
| `internal/tui/tui.go` | Remove `updateCommandSuggestions` method, orphaned comments |
| `internal/agent/context_manager.go` | Remove `ResetPruner` method, `PruneFilter` method |
| `internal/agent/options.go` | Remove `WithPermissionRules`, `WithApproveFn` |
| `internal/tui/keymap.go` | Remove `footerKeyMap`, `ShortHelp`, `FullHelp`, `footerBindings` |
| `internal/repl/color.go` | Remove `Green`, `Yellow` |
| `cmd/yaah/update.go` | Remove `var _ = os.Stdout` and comment |

## Risks

- **Overflow recovery scaffolding removed:** If OPT-2 is implemented later, overflow handling will need to be written from scratch. This is acceptable — the current code is dead weight and implementing OPT-2 properly requires different patterns (retry with chunking, not just error wrapping).
- **Test updates needed:** `IsSpawnCapable` is referenced in `role_test.go` and `role_def_test.go`. Those test references must be removed.
