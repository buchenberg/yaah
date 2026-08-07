# yaah Architectural Review

**Date**: 2026-08-06 | **Branch**: `chore/arch-improvements` (PR #171) | **LOC**: ~15,000 production Go, ~8,000 tests

---

## Summary

yaah is a well-structured, mature Go codebase (Go 1.25+, 16 direct dependencies, 47 goroutine patterns). The architecture follows clean layering with `cmd/yaah` as composition root, `internal/agent` as the core loop, and typed events flowing through a pubsub broker. Dependency direction is inward and strict — zero circular dependencies.

**Overall grade: B+**. The primary issue is `cmd/yaah` carrying too much business logic. Secondary concerns: dual TUI maintenance burden and two large un-implemented plans (sub-agent lifecycle, custom agent profiles).

---

## 1. Dependency Graph — ✅ Clean

```
cmd/yaah (22 imports — composition root)
  ↓
internal/tui, internal/tui2 (presentation)
  ↓
internal/agent (core loop + pipeline + llm)
  ↓
internal/tools, internal/mcp, internal/providers
  ↓
internal/types, internal/todo, internal/pubsub (leaf types)
```

**No circular dependencies.** `types` is the universal leaf (13 dependents, only depends on `todo`). `agent/pipeline` correctly depends on `prompts` and `types` but never back-references into `agent/`.

---

## 2. Package Structure — ✅ Good

`internal/` enforces compiler-level encapsulation with 22 well-named packages. Clean leaf packages with zero or one internal import: `banner`, `config`, `instructions`, `mcp`, `plans`, `process`, `pubsub`, `skills`, `spinner`, `update`. No `utils/`, `common/`, or `helpers/` packages.

The `agent/` sub-package tree is well-decomposed:
```
agent/
├── events/       — sealed event interface (11 types, typed pubsub)
├── pipeline/     — middleware chain (10 middleware with 3 lifecycle hooks)
├── llm/          — streaming/non-streaming dispatch with retry + errorclassify
├── subagent/     — role definitions and registry
└── errorclassify/— 15 error reasons, 11 pattern catalogs
```

---

## 3. Agent Loop Architecture — ✅ Strong

The loop follows a clean middleware pipeline pattern:

```
User Input → Loop.Run()
  → Pipeline.PrepareStep (steer, followup, compaction, soft_prune, ...)
  → LLM.Call (streaming + retry + errorclassify-based recovery)
  → Pipeline.PostModel (permission filtering, staleness)
  → executeAndCollect (concurrent tools: approval, sub-agent watchdog, quality gates)
  → Pipeline.PostTool (compaction, loop detection, staleness)
  → Events to View via Broker (TokenDelta, ToolStart/End, Done)
```

**Strengths**:
- Typed event system with compile-time exhaustiveness checks (sealed interface)
- `View` interface decouples agent from UI (5 implementations: REPL, TUI, TUI2, Web, ACP)
- Error classification taxonomy is production-grade (15 reasons, context-aware recovery hints)
- Middleware is config-driven with enable/disable lists
- `LoopConfig` uses functional options pattern (20+ options, replaces 30-field struct literal)

**ContextManager**: The 684-line `context_manager.go` holds compaction thresholds, pruning tuning, token tracking, and truncation logic. The state sync dance between `Loop.State` and `ContextManager` fields has been eliminated (PR #174): `ContextManager` now holds a `State *LoopState` pointer and reads/writes mutable state directly, removing 9 duplicate fields and the copy-in/copy-out sync in `Loop.compactContext`/`Loop.trimContext`.

---

## 4. cmd/yaah Wiring — 🟡 Too Thick

`cmd/yaah/` is 40 files totaling ~5,700 lines. It should be thin cobra command wiring, but contains substantial business logic:

| File | Lines | Issue | Recommended Location | Status |
|---|---|---|---|---|
| `subagent_runner.go` | 563 | Entire sub-agent construction engine (role resolution, tool registry, prompt assembly, result trimming) | `internal/agent/runner/runner.go` | ✅ Done (PR #173) |
| `provider_resolve.go` | 311 | Provider creation, OAuth tokens, API key extraction, timeout mapping | `internal/providers/resolve.go` | |
| `wiring.go` | 396 | MCP init (`initMCP`), OTel init (`initOtel`), prompt layer assembly | Split into `wiring_otel.go`, `wiring_mcp.go`, `wiring_prompt.go` | ✅ Done (PR #174) |
| `doctor.go` | 416 | 40+ diagnostic checks | `internal/doctor/checks.go` | ✅ Done (PR #173) |
| `acp.go` | 478 | Full JSON-RPC 2.0 protocol server | `internal/acp/server.go` | |
| `login.go` | 265 | OAuth device flow, token persistence | `internal/providers/oauth.go` | |
| `compact_cmd.go` | 176 | Manual compaction parallel to agent loop's built-in compactor | Unify with `internal/agent/compactor.go` | |

**`agentSession` (260 lines)** is well-placed as a coordinator struct, but its constructor `newAgentSession` (previously 300+ lines in `wiring.go`) has been split into focused builders (PR #174): `wiring_otel.go`, `wiring_mcp.go`, and `wiring_prompt.go` handle OTel init, MCP init, and prompt assembly respectively, slimming `wiring.go` to ~210 lines.

---

## 5. Dual TUI — 🟡 Maintenance Burden

Two completely independent TUI implementations:

| | `internal/tui` (default) | `internal/tui2` (experimental) |
|---|---|---|
| Framework | bubbletea v2 + lipgloss v2 | tview + tcell v2 |
| Files | 22 (~3,750 lines prod) | 40 (~2,840 lines prod) |
| Test lines | ~2,410 | 53 |
| CLI | `yaah tui` | `yaah tui2` |
| Architecture | Flat, monolithic Model (79 fields) | Hierarchical, 22 component sub-packages |

**Duplicated between them**:
- Role colors (`role_colors.go` vs `colors/rolecolors.go`) — identical hex values
- Lolcat algorithm (`banner.Lolcat()` vs `lolcat.Rainbow()`)
- Markdown rendering (`glamour/v2`, two independent singleton renderers)

**Neither is deprecated.** The bubbletea TUI has full test coverage; the tview TUI has a richer component model but almost no tests. Maintaining both indefinitely adds ~4 extra direct dependencies (tview + tcell + 2 indirect).

---

## 6. Error Handling — ✅ Strong, One Gap

| Metric | Status |
|---|---|
| `panic()` in production | **0** — none |
| `%w` error wrapping | Nearly universal across 100+ call sites |
| Sentinel / typed errors | 7 typed error types, wire into 10+ call sites ✅ |
| `errors.As` / custom error types | **0 uses** — no custom error types with fields |
| Error classification | 15 reasons, 11 pattern catalogs — excellent |
| `defer` resource cleanup | 67 deferred Close() calls, universal mutex unlocks |
| Goroutine lifecycle | 47 launches, all with proper exit conditions |
| Structured logging (`slog`) | Replaced 2 `log.Printf` → `slog.Warn`/`slog.Error` ✅ |

**Typed errors added** (PR #171):
- `MaxIterationsError{MaxIter: int}` — in `internal/agent/loop.go`
- `ToolDeniedError{}` — in `internal/agent/agent_tools.go`
- `LoopDetectedError{Tool, Count, Window}` — in `internal/agent/pipeline/loopdetect.go`
- `RoleNotFoundError{Role: string}` — in `internal/agent/subagent/role.go`
- `ToolTimeoutError{Tool, Timeout}` — in `internal/tools/tools.go`
- `ToolNotFoundError{Name: string}` — in `internal/tools/tools.go`
- (pre-existing) `ErrStuckChild` — in `internal/tools/subagent_ctx.go`

Each implements `Is(error) bool` for zero-value `errors.Is` matching: `errors.Is(err, MaxIterationsError{})`.

**Minor**: No custom error types with additional fields (`ProviderError`, `StatusCode`, `RetryAfter`) — `errors.As` remains unused.

---

## 7. Testing — 🟡 Uneven Coverage

| Area | Coverage | Notes |
|---|---|---|
| `internal/agent/` | Heavy (~4,000 test lines) | agent_test.go is 1,759 lines alone |
| `internal/tui/` | Heavy | tui_test.go 1,852 lines, component_test.go 555 lines |
| `internal/tui2/` | **Near zero** | Only 53 lines in lolcat_test.go |
| `internal/tools/` | Moderate | Path validator tests fixed for macOS/Windows ✅ |
| `internal/process/` | Moderate | Use POSIX `echo`/`exit` instead of PowerShell ✅ |
| `internal/agent/pipeline/` | Moderate (~800 lines) | — |
| `internal/mcp/` | Unknown | — |
| Remaining packages | Sparse | Many internal packages have no or thin test coverage |

---

## 8. Security — ✅ Clean

- No hardcoded API keys, tokens, or passwords
- No `InsecureSkipVerify` in TLS
- File operations (`write`, `edit`, `delete`) pass through `PathValidator`
- MCP HTTP server binds to `127.0.0.1` only
- Binary self-update verifies checksums
- Dockerfile runs as root (acceptable for CLI, hardening opportunity)
- `docker-compose.yml` has hardcoded dev credentials (local-only, low risk)

---

## 9. Dead Code & Duplication

**Duplicated role colors**: `internal/tui/role_colors.go` and `internal/tui2/colors/rolecolors.go` — identical hex values, acknowledged in comment.

**Deprecated type aliases**: `ServerInfo`, `QuestionModal`, `QuestionOption` in `internal/tui/model.go` — still consumed by callers.

**Possible dead code**: `internal/tui2/components/messages/messages.go` Append* helpers may be unused in production (only `Build()` is called).

---

## 10. Un-implemented Plans

Two large feature plans remain:

### Custom Agent Profiles
`~/.agents/agents/<name>.md` with YAML frontmatter (`permission` rules) replacing `identity.md` at session start. Selectable via `--agent`, `:agent`, `/agent`. Requires: new `internal/agents/` package, 4 built-in agents (architect, reviewer, simplifier, tester), permission merging, and TUI/REPL integration.

### Sub-Agent Lifecycle
Role-based tool profiles (developer, analyst, reviewer, tester), timeout enforcement per sub-agent, parallel batch dispatch with concurrency cap, interrupt propagation. Requires: `internal/agent/subagent.go` (roles + profiles), timeout wrapping in `TaskTool`, parallel dispatch in `executeAndCollect`, depth-aware middleware. Note: roles already exist as `developer`, `analyst`, `reviewer`, `tester` (not worker/planner).

---

## Priority Recommendations

### ✅ Completed (PR #171)

1. **Add `log/slog`** — replaced 2 `log.Printf` calls with `slog.Warn`/`slog.Error`
2. **Define sentinel errors** — 6 typed error types with `Is` method, wired into 10+ call sites
3. **Fix `prompts.go` init** — moved `os.Exit` to `sync.Once` lazy validation
4. **Clean up ContextManager docs** — removed stale Phase 1/Phase 2 markers, removed redundant `Messages` sync
5. **Fix macOS/Windows path validator tests** — resolved `EvalSymlinks` on `t.TempDir()` paths, canonical path comparison
6. **Fix process tests** — replaced PowerShell commands with POSIX `echo`/`exit`, added poll loop for status
7. **Fix staticcheck SA5011** — moved nil guards before `defer m.Stop()`

### ✅ Completed (PR #173)

9. **Extract `subagent_runner.go`** → `internal/agent/runner/runner.go` — 563 lines of sub-agent composition logic moved out of `cmd/yaah` into a new `internal/agent/runner` package (placed in its own sibling package rather than `internal/agent/subagent` to avoid an import cycle: `subagent → tools → subagent`). `resolveProviderByName` injected as a parameter.
10. **Extract `doctor.go`** → `internal/doctor/checks.go` — 429 lines of diagnostic logic separated from cobra command. `DirectiveOverrides` global replaced with `Options` struct parameter.

### ✅ Completed (PR #174)

11. **Split `wiring.go:newAgentSession`** — extracted focused builders: `wiring_otel.go` (initOtel + wrapProviderWithOtel), `wiring_mcp.go` (initMCP), `wiring_prompt.go` (buildSystemPrompt + buildMainPrompt). wiring.go slimmed from 396 to ~210 lines; removed dead `layers.Skills` assignment.
12. **Complete `ContextManager` extraction** — eliminated state sync dance. Added `State *LoopState` pointer to `ContextManager`; removed 9 duplicate state fields. `Loop.compactContext` and `Loop.trimContext` now delegate directly (19-line and 3-line sync dances removed). Removed 5 redundant `CtxMgr.Messages` assignments across `loop.go`, `tools.go`, `turn.go`.

### 🔴 Remaining High Impact / Low Effort

8. **Delete or deprecate `tui2/`** — pick one TUI framework and commit to it; stop maintaining both

### 🟡 Medium Impact

13. **Implement sub-agent lifecycle** (from plan) — this is the most impactful missing feature

### 🟢 Low Priority

14. **Deduplicate role colors** — create `internal/colors/` shared between TUIs
15. **Define custom error types** — `ProviderError{StatusCode, RetryAfter, RawBody}` for `errors.As`
16. **Wire context into `process.Manager`** — subprocess cancellation via context rather than `Process.Kill()`
17. **Implement custom agent profiles** (from plan)
18. **Add tests to `tui2/`** if kept; otherwise delete
