# Architectural Review 2025-11

Full findings from a 5-sub-agent parallel architectural review of the yaah
codebase. Each issue is written as a bd-ready section: title, priority, type,
description, acceptance. Import via the companion `bd-import.sh` (see bottom
of this file) or file manually with `bd create`.

---

## HIGH severity (4)

### 1. TUI `HandleEvent` lacks default branch and exhaustiveness test
- **priority**: 1
- **type**: bug
- **files**: `internal/tui/proxy.go` (`HandleEvent` type switch)
- **finding**: The TUI's `agent.Event` type switch has no `default:` branch and
  no compile-time exhaustiveness protection. The `events/exhaustive_test.go`
  runtime grep DOES cover `proxy.go`, but the code itself gives no hint when a
  new event type is silently dropped. New events → silent no-op in the TUI
  until someone reruns the test suite.
- **fix**: Add a `default:` case that logs (or panics in dev) on unknown event
  types, matching the "explicit intentionally-ignored" pattern used elsewhere.

### 2. `plans.ParseFrontmatter` regressed to line-by-line parsing
- **priority**: 1
- **type**: bug
- **files**: `internal/plans/plans.go:57-77`
- **finding**: `skills.ParseFrontmatter` was explicitly fixed to use `yaml.v3`
  (review A5). `plans.ParseFrontmatter` still uses the brittle
  `strings.Split(frontmatter, "\n")` + prefix-match approach that skills
  abandoned. Fails on quoted values, multi-line strings, or any real YAML.
- **fix**: Replace with `yaml.Unmarshal([]byte(fm), &frontmatterStruct)`.
  Consider extracting a shared `internal/frontmatter` package used by both.

### 3. `web_view.toolStartSummary` duplicates `internal/toolfmt` logic
- **priority**: 1
- **type**: tech-debt
- **files**: `cmd/yaah/web_view.go:220-268`
- **finding**: 48 lines of `toolStartSummary` + `shortPattern` + `extractURL`
  + `extractAction` + 3 `regexp.MustCompile` vars — a private duplicate of
  toolfmt logic, applied only to `ToolStartEvent`. Meanwhile the terminal
  view has no pre-execution summary at all, and ACP has yet another
  representation. Latent duplication bug.
- **fix**: Add `toolfmt.StartSummary(name, args)` mirroring the existing
  `toolfmt.Summary(name, args, result)`. Delete the web_view duplicates.
  Optionally use it from ACP and terminal for consistency.

### 4. `EscalationEvent` structure discarded by ACP and SSE views
- **priority**: 1
- **type**: bug
- **files**: `internal/acp/view.go:64`, `cmd/yaah/web_view.go:131`
- **finding**: ACP flattens the escalation into an `agent_message_chunk` text
  string. SSE emits a `ctrl.status` frame carrying only `Summary`. Both drop
  `Detail`, `Suggestion`, and `SubAgentRole`. The terminal view renders all
  four. Clients cannot route or display structured escalations.
- **fix**: Emit a dedicated `escalation` wire type in both views with
  Severity/Summary/Detail/Suggestion/SubAgentRole fields.

---

## MEDIUM severity (14)

### 5. TUI `App` is a ~65-field god object across ~20 files
- **priority**: 2
- **type**: refactor
- **files**: `internal/tui/app.go` and ~19 sibling files
- **finding**: Single `App` struct owns layout, event handling, business state,
  metrics, config mirror, 8 callback hooks, 8 atomics, 3 mutexes. Violates SRP.
  Extraction candidates: render state (`renderedItems`, `renderedWidth`,
  `userScrolled`), ephemeral timer helper, config snapshot, activity/metrics
  block.
- **fix**: Design a decomposition plan; refactor incrementally without
  breaking `HandleEvent` / control-plane contracts.

### 6. `cmd/yaah/tui.go` type-asserts into `*tools.QuestionTool`
- **priority**: 2
- **type**: refactor
- **files**: `cmd/yaah/tui.go:131-140`
- **finding**: Command layer reaches into `sess.toolReg.Get("question")` and
  mutates `qtp.Handler`. Deep coupling between wiring and tool internals.
- **fix**: Add `agentSession.SetQuestionHandler(func(...))` on the session
  API; keep the tool registry opaque.

### 7. Undocumented globals `tools.SharedTraceStore` / `SharedScopeManager`
- **priority**: 2
- **type**: tech-debt
- **files**: `cmd/yaah/wiring.go:252,254`, `internal/tools/*`
- **finding**: Two package-level mutable singletons set from wiring. Neither
  is in AGENTS.md's permitted-globals list. Race hazard for multi-session
  scenarios; kills testability.
- **fix**: Inject into `SupervisedTaskTool` constructor. Already available at
  wiring time.

### 8. `newAgentSessionWithOptions` is a ~300-line god function
- **priority**: 2
- **type**: refactor
- **files**: `cmd/yaah/wiring.go`
- **finding**: Config load, OTel init, role registry load, tool registry build,
  DB open, embedding attach, system prompt assembly, session restore,
  background jobs wiring, shepherd init, supervised task conditional
  registration, session construction — all one function.
- **fix**: Extract shepherd block first (~50 lines, clear seam). Then
  embedder setup and jobs wiring.

### 9. Dual compaction paths race in the same turn
- **priority**: 2
- **type**: bug-latent
- **files**: `internal/agent/pipeline/compaction.go`, `internal/agent/context_manager.go`
- **finding**: `CompactionMiddleware` fires on `PrepareStep`/`PostTool`.
  `ContextManager.compactContext` fires on overflow recovery from
  `llm.Client.Call`. Both share state via `compactFn` delegation. No guard
  against double-compact in a single turn.
- **fix**: Add ordering documentation and a "just compacted" guard on the
  ContextManager path.

### 10. `llm.Client.isDegenerateStream` uses error-string matching
- **priority**: 2
- **type**: tech-debt
- **files**: `internal/agent/llm/client.go`
- **finding**: `strings.Contains(msg, "streamed response produced no content")`
  — exactly the anti-pattern `errorclassify` was built to replace.
- **fix**: Promote a typed `DegenerateStreamError` and check with `errors.As`.

### 11. `llm.Client` uses mutable struct fields for per-call state
- **priority**: 2
- **type**: bug-latent
- **files**: `internal/agent/llm/client.go`
- **finding**: `replayCount` and `dsmlSeq` are struct fields but used only
  within a single `Call`. Concurrent-call race hazard.
- **fix**: Convert to locals inside `Call`.

### 12. `pubsub.PublishMustDeliver` holds RLock during timeout
- **priority**: 2
- **type**: performance
- **files**: `internal/pubsub/broker.go`
- **finding**: `time.After(timeout)` called inside the RLock scope, per
  subscriber. Slow consumers block broker teardown by up to `N × 50ms`.
- **fix**: Precompute deadlines and use `context.WithTimeout` outside the
  lock, or serialize with `chan struct{}`.

### 13. No `Provider` interface despite two concrete clients
- **priority**: 2
- **type**: refactor
- **files**: `internal/providers/*`
- **finding**: `OpenAIClient` and `AnthropicClient` share output types
  (`StreamChunk`, `APIError`) but no shared contract exists. Callers duck-type
  or reference concretes. Third provider = touching every dispatch site.
- **fix**: Extract `Provider` interface with `Send`, `SendStream`,
  `ListModels`. Mark `ListModels` as optional (return `ErrNotSupported`).

### 14. Skills and plans duplicate discovery + frontmatter loops
- **priority**: 2
- **type**: refactor
- **files**: `internal/skills/skills.go`, `internal/plans/plans.go`
- **finding**: Walk-up-cwd loop, `seen` map, skip logic — copy-pasted between
  two packages. `truncate` variants across `memory`, `instructions`,
  `providers/apierror` (three near-identical UTF-8-safe truncators).
- **fix**: Extract `internal/frontmatter` for parsing + generic `Discover[T]`
  helper (Go 1.18+ generics). Extract `internal/strutil` for truncation.

### 15. SQLite schema version stuck at 1 despite 12 additive migrations
- **priority**: 2
- **type**: tech-debt
- **files**: `internal/memory/memory.go:17-20`
- **finding**: `currentSchemaVersion = 1` never bumped, yet 12 `ensureColumn`
  steps have been added. Version guard catches "db newer than binary" but
  gives false confidence.
- **fix**: Bump version on each column addition. Add `if version >= N`
  guards around each `ensureColumn`.

### 16. `memory.EmbedMemoryAsync` mixes lock domains
- **priority**: 2
- **type**: bug-latent
- **files**: `internal/memory/memory.go:263-270`
- **finding**: Holds `d.embMu` while executing `d.sql.Exec`. SQLite has its
  own locking; lock-ordering bug waiting to happen.
- **fix**: Release `embMu` before the SQL write.

### 17. Per-role provider names not validated at config load
- **priority**: 2
- **type**: bug
- **files**: `internal/config/validate.go:85-100`
- **finding**: `agents.subagent.roles.<name>.provider` typos go undetected
  until first dispatch. Only top-level fallback/subagent providers are
  cross-checked.
- **fix**: Extend validation to walk per-role provider references.

### 18. `activeSubAgents` counter races with DoneEvent reset
- **priority**: 2
- **type**: bug
- **files**: `internal/tui/proxy.go:103`
- **finding**: `DoneEvent` handler resets counter to 0 unconditionally. A
  concurrent `SubAgentEndEvent` decrement can then make it -1 (silently
  clamped by `> 0` guard). State becomes wrong.
- **fix**: Only reset if no pending sub-agent end events are queued, or
  track by ID rather than counter.

---

## LOW severity (10)

### 19. `YAARH_THEME` env var typo — light theme unreachable
- **priority**: 3
- **type**: bug
- **files**: `internal/tui/colors/theme.go:14`
- **finding**: Checks `os.Getenv("YAARH_THEME")` — extra `H`. Should be
  `YAAH_THEME`. Silent misconfiguration since the file was written.
- **fix**: One-character rename. Add regression test.

### 20. `ReasonAuthPermanent` is dead code
- **priority**: 3
- **type**: cleanup
- **files**: `internal/agent/errorclassify/`
- **finding**: Declared in enum, has `String()` case, but `Classify` never
  returns it. Either implement the auth-after-rotation path or delete.
- **fix**: Delete the constant + string case, or implement the intended
  classifier branch.

### 21. `ViewWithWrite` split-brain in ACP view
- **priority**: 3
- **type**: refactor
- **files**: `internal/acp/view.go:90-110`
- **finding**: Two exported types (`View` + `ViewWithWrite`) where the inner
  `View` is stateless and only useful when wrapped. Collapse to one type.
- **fix**: One `ACPView` with constructor-injected send + sessionID.

### 22. `sseView.writeLocked` has no disconnect check
- **priority**: 3
- **type**: bug
- **files**: `cmd/yaah/web_view.go:171`
- **finding**: Writes to `http.ResponseWriter` with no `ctx.Err()` check.
  Disconnected clients cause silent write failures until loop ends.
- **fix**: Accept context; short-circuit on `ctx.Err()`.

### 23. Dual write pumps on SSE stream have implicit shutdown ordering
- **priority**: 3
- **type**: tech-debt
- **files**: `cmd/yaah/web_view.go`
- **finding**: `sseView.HandleEvent` (broker path) and `forwardCtrl`
  (control-plane path) both write to the same `ResponseWriter` with the same
  mutex. Shutdown ordering (DoneEvent vs control.Done) is untested and
  implicit.
- **fix**: Merge into a single write pump draining both channels via a
  select, or document ordering invariant with a test.

### 24. `sseView` accumulating session-cache state
- **priority**: 3
- **type**: refactor
- **files**: `cmd/yaah/web_view.go:97-103`
- **finding**: 4 mutable cache fields (provider, model, mcpServers, todos)
  for reconnect replay. SRP drift.
- **fix**: Extract `SessionSnapshot` struct held externally; keep view
  stateless.

### 25. `NoopView` consumers not acknowledged in exhaustive test
- **priority**: 3
- **type**: test
- **files**: `internal/agent/events/exhaustive_test.go:67-74`
- **finding**: Test hardcodes 4 consumer paths. `NoopView` (used by
  `serve.go` and `runner/runner.go`) is silently outside the check.
- **fix**: Add a comment naming both NoopView consumers, or add a compile-
  time exhaustiveness assertion that NoopView is intentional.

### 26. `serve.go ensureSession` retries silently after warmup error
- **priority**: 3
- **type**: bug
- **files**: `cmd/yaah/serve.go:~87`
- **finding**: Failed warmup sets `st.sessErr` but `ensureSession` doesn't
  short-circuit on it. Every subsequent call re-runs the expensive failing
  init.
- **fix**: Short-circuit on `st.sessErr != nil`.

### 27. `control.Continue.AnswerCh` can block agent forever in headless mode
- **priority**: 3
- **type**: bug
- **files**: `internal/control/control.go`
- **finding**: No timeout on the answer channel. In MCP-serve/headless mode
  with no human, the agent goroutine blocks indefinitely.
- **fix**: Add per-mode default answer or context deadline.

### 28. `observability.Setup` is not idempotent
- **priority**: 3
- **type**: bug
- **files**: `internal/observability/otel.go`
- **finding**: Second call installs new TracerProvider/MeterProvider against
  the global registry; returned `shutdown` only covers the latest set.
  Leaks providers on repeat init (tests).
- **fix**: Guard with `sync.Once` or detect prior install and return the
  original shutdown.

---

## INFO (7) — file if desired, or park

- **29**: `internal/todo` domain type imported directly into TUI (`app.go:9`)
  — boundary leak. Priority 4.
- **30**: `spawn_subagent` string literal hardcoded in `proxy.go:37,49` for
  tool-block filtering. Priority 4.
- **31**: `expandHomeDir` doesn't handle `%USERPROFILE%` on Windows target.
  `internal/config/load.go`. Priority 4.
- **32**: `instructions.Load` loads both `AGENTS.md` and `CLAUDE.md` from the
  same dir if present — potential double-inject. Priority 4.
- **33**: `memory` tag filter uses `LIKE '%"tag"%'` — full table scan; no
  index. Fine at small scale. Priority 4.
- **34**: `jobs` uses 9 context keys for sub-agent plumbing; single struct
  pointer under one key would be cleaner. Priority 4.
- **35**: `providers.AnthropicClient.ListModels` hard-errors — no interface
  contract marks this optional. Falls out of #13. Priority 4.

---

## Partial-coverage warning

The wiring/tools/MCP reviewer emitted a `warning` escalation: glob patterns
for `internal/tools/*.go` and `internal/mcp/*.go` returned no files, so the
`supervised_task` ↔ `supervisor` coupling, tool-schema consistency, and MCP
client/server duplication were NOT reviewed. A follow-up review scoped
specifically at those two packages is recommended.

---

## Import script (bd batch)

Once `bd` is functional on this machine (CGO-enabled build, or `dolt
sql-server` running with `bd init --server`, or `bd init --proxied-server`),
save the following as `bd-import.sh` and run:

```bash
#!/usr/bin/env bash
# Files 28 issues from the 2025-11 architectural review.
# Uses bd batch grammar: create <type> <priority> <title...>
bd batch <<'EOF'
create bug 1 TUI HandleEvent lacks default branch and exhaustiveness test
create bug 1 plans.ParseFrontmatter regressed to line-by-line parsing
create tech-debt 1 web_view.toolStartSummary duplicates internal/toolfmt logic
create bug 1 EscalationEvent structure discarded by ACP and SSE views
create refactor 2 TUI App is a 65-field god object across 20 files
create refactor 2 cmd/yaah/tui.go type-asserts into *tools.QuestionTool
create tech-debt 2 Undocumented globals tools.SharedTraceStore and SharedScopeManager
create refactor 2 newAgentSessionWithOptions is a 300-line god function
create bug 2 Dual compaction paths race in the same turn
create tech-debt 2 llm.Client.isDegenerateStream uses error-string matching
create bug 2 llm.Client uses mutable struct fields for per-call state
create performance 2 pubsub.PublishMustDeliver holds RLock during timeout
create refactor 2 No Provider interface despite two concrete clients
create refactor 2 Skills and plans duplicate discovery and frontmatter loops
create tech-debt 2 SQLite schema version stuck at 1 despite 12 additive migrations
create bug 2 memory.EmbedMemoryAsync mixes lock domains
create bug 2 Per-role provider names not validated at config load
create bug 2 activeSubAgents counter races with DoneEvent reset
create bug 3 YAARH_THEME env var typo makes light theme unreachable
create cleanup 3 ReasonAuthPermanent is dead code in errorclassify
create refactor 3 ViewWithWrite split-brain in ACP view
create bug 3 sseView.writeLocked has no disconnect check
create tech-debt 3 Dual write pumps on SSE stream have implicit shutdown ordering
create refactor 3 sseView accumulating session-cache state
create test 3 NoopView consumers not acknowledged in exhaustive test
create bug 3 serve.go ensureSession retries silently after warmup error
create bug 3 control.Continue.AnswerCh can block agent forever in headless mode
create bug 3 observability.Setup is not idempotent
EOF
```

Note: `bd batch` accepts only the narrow grammar shown in `bd batch --help` —
it does NOT support `--description` or bodies. After the batch runs, use
`bd update <id> ...` or `bd edit <id>` to add the descriptions from this
report. Alternately, run one `bd create ... --description "..."` per issue in
a for-loop (slower but includes bodies).

---

Generated by a 5-sub-agent parallel architectural review, 2025-11-16.
See conversation transcript for methodology and per-reviewer contracts.
