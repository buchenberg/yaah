---
name: consolidate-persistence
description: Consolidate overlapping persistence systems - session storage, memories, OTel traces, and Shepherd traces
status: draft
---

# Plan: Consolidate yaah Persistence and Observability Systems

## Goal

Reduce redundancy across yaah's four persistence/observability systems by consolidating overlapping data and unifying storage backends where practical. The primary targets are:

1. **Session storage** (`internal/memory/` SQLite `state.db`) - conversation history
2. **Shepherd traces** (`shepherd-kernel-go` SQLite `trace.sqlite`) - tool execution traces
3. **OTel traces** (`internal/observability/` OTLP backend) - distributed tracing
4. **Memories** (`internal/memory/` SQLite `state.db`) - user knowledge base

## Problem Statement

Currently yaah maintains **two separate SQLite databases** (`~/.yaah/state.db` and a Shepherd trace store at `<shepherd_trace_dir>/trace.sqlite` — configurable, opt-in, empty by default) that store overlapping data about tool executions, token usage, and session metadata. This creates:

- **Storage fragmentation**: Two files to backup/migrate/manage
- **Query fragmentation**: Different CLIs for similar data (`yaah session` vs `yaah shepherd-trace`)
- **Write duplication**: Token counts and tool call details written to multiple places
- **Conceptual overlap**: Session messages and Shepherd facts both record tool execution history
- **No cross-links**: OTel spans carry no `session_id`, and `messages` carry no `trace_id`, so the four systems cannot be joined to correlate a single turn
- **Hidden duplication**: verbose OTel tracing re-records full message content, reasoning, and tool-call args already persisted in `messages`

## Non-Goals

- Do NOT consolidate **Memories** - user knowledge is conceptually separate from execution data. (But the FTS + embedding + vector-search *plumbing* shared by `memory` and `messages` can be unified without merging the data — see Finding F7.)
- Do NOT eliminate **OTel traces** - distributed tracing has different query semantics (span trees vs fact DAGs)
- Do NOT change or fork the **shepherd-kernel-go** library - upstream dependency. This constrains Phase 1 — see Finding F1.

---

## Current Architecture Overview

### System 1: Session Storage + Memories (SQLite `state.db`)

**Location**: `internal/memory/`

**Tables**:
- `sessions` - session metadata (id, started_at, ended_at, cwd, model, tokens_in, tokens_out, system_prompt, compacted_summary, compaction_cooldown_until, ineffective_compactions)
- `messages` - conversation messages (session_id, idx, role, content, reasoning_content, tool_name, tool_call_id, tool_calls, ts, id, embedding)
- `memory` - long-term memory notes (id, text, tags, source, created_at, accessed_at, access_count, embedding)
- `todos` - todo persistence
- FTS5 virtual tables: `messages_fts`, `memory_fts`

**CLI**: `yaah session`, `yaah memory`

### System 2: Shepherd Traces (SQLite `trace.sqlite`)

**Location**: `internal/agent/pipeline/trace.go` + `shepherd-kernel-go`

**Storage**: Separate SQLite file at `filepath.Join(shepherd_trace_dir, "trace.sqlite")` — the directory is configurable (`cfg.Agent.Default.ShepherdTraceDir`) and tracing is opt-in (empty by default).

**Data Model**:
- Facts with Declaration/Capture modes
- Causal parent-child links (Fact DAG)
- Frontiers for stable read points
- Schema: `yaah.tool.{name}.v1` for declarations, `yaah.tool.{name}.v1.applied` for captures
- Turn lifecycle: `yaah.execution.created.v1`, `yaah.execution.started.v1`, `yaah.execution.completed.v1`, `yaah.execution.failed.v1`

**CLI**: `yaah shepherd-trace list/show/profile`

### System 3: OTel Traces (External or In-Memory)

**Location**: `internal/observability/`

**Storage**: 
- External OTLP backend (Jaeger, SigNoz, etc.) when `endpoint` is configured
- In-memory `BufferingSpanProcessor` in serve mode when no endpoint

**Data Model**: OpenTelemetry span tree with attributes for tokens, tool names, etc.

**Control**: Gated by `OtelEnabled` flag in loop config

### System 4: Memories (Already in `state.db`)

**No overlap** - memories are user knowledge, not execution data. Keep separate.

---

## Overlap Analysis

### Session Storage vs Shepherd Traces

| Data | Session Storage | Shepherd Traces | Overlap |
|------|---------------|----------------|---------|
| Session ID | `sessions.id` PK | `TraceOwnerID` | ✅ Same concept |
| Tool calls | `messages.tool_name`, `messages.tool_calls` | Declaration facts with `yaah.tool.{name}.v1` | ✅ Both record tool invocations |
| Tool args | `messages.tool_calls` JSON | Declaration fact payload `args` | ✅ Same data |
| Tool results | Subsequent messages | Capture fact payload `success`, `error`, `duration` | ✅ Same data |
| Token counts | `sessions.tokens_in/out` | Turn capture facts `prompt_tokens`, `completion_tokens` | ✅ Same data |
| Timestamps | `sessions.started_at`, `messages.ts` | Fact envelope timestamps | ✅ Same data |
| Start time | `sessions.started_at` | Turn declaration fact | ✅ Same data |

**Conclusion**: High overlap. Both store the same execution history from different perspectives.

### Session Storage vs OTel Traces

| Data | Session Storage | OTel Traces | Overlap |
|------|---------------|-------------|---------|
| Token counts | ✅ | ✅ (span attributes) | ✅ Partial |
| Tool execution | ✅ | ✅ (span per tool) | ✅ Partial |
| LLM calls | ❌ | ✅ (detailed) | ❌ Different |
| Session concept | ✅ | ❌ | ❌ Different |
| Persistence | ✅ SQLite | ❌ External/In-memory | ❌ Different |

**Conclusion**: Partial overlap but different purposes (durable history vs ephemeral debugging). OTel is for **observability**, session storage is for **history/replay**.

### Shepherd Traces vs OTel Traces

| Aspect | Shepherd | OTel | Relationship |
|--------|----------|------|--------------|
| Purpose | Supervised execution (rollback) | Distributed debugging | Complementary |
| Data model | Fact DAG with causal links | Span tree with parent-child | Different |
| Granularity | Intent + Capture per operation | Span per operation | Similar |
| Supervision | ✅ ScopeManager for rollback | ❌ | Unique to Shepherd |
| Persistence | ✅ SQLite | ❌ (unless external) | Different |

**Conclusion**: **Overlapping on the turn/tool/token lifecycle; complementary on the model.** Shepherd's fact DAG (causal parents, frontiers, witnesses) and OTel's span tree both record turn boundaries, tool invocations, token counts, and success/error. What is genuinely distinct is Shepherd's supervision capability (checkpoints, tree states, rollback/fork) and OTel's cross-service distributed semantics.

---

## Findings (blocking / reshaping the plan)

### F1. Shepherd exposes no `TraceStore` interface — Phase 1 as written is not implementable

The plan's Phase 1 assumes yaah can implement a `shepherd.TraceStore` interface over the shared `*sql.DB`. It cannot. In `shepherd-kernel-go@v0.3.2` (the version currently in `go.mod`):

- `NewSQLiteTraceStore(path)` returns a **concrete** `*shepherd.SQLiteTraceStore` that **owns its own `*sql.DB` connection** and opens its own file. There is no exported interface for the store.
- The store's schema is far richer than "facts + frontiers". Real tables: `records`, `path_entries`, `record_edges`, `contexts`, `append_intents` (idempotency), `owner_ordinals`, `meta`, `frontiers`, plus witness records. Record IDs are content-addressed SHA-256 digests; append is idempotent via `append_intents`; there is a witness/authorization trust chain (`ensureAppendAuthorized` / `ensureReadAuthorized`).
- `ScopeManager`, `Scope`, checkpoints, and `CaptureTree`/`ApplyTree` all build on that concrete store plus git operations (`scope_manager.go`, `scope.go`).

So merging Shepherd into `state.db` means one of:

1. **`ATTACH DATABASE`** — keep the shepherd store on its own file but attach it to the shared connection. Still two files; cosmetic only.
2. **Fork/vendor** the store schema and logic — explicitly out of scope (Non-Goals) and a large, fragile re-implementation of digest/idempotency/witness/scope.
3. **Leave the two files separate** and link them with shared IDs (F3, Phase 0). **This is the recommendation.**

The plan's proposed `shepherd_facts` / `shepherd_frontiers` schema does not match the real schema and would break idempotency, digests, and the supervisor's checkpoint/rollback.

### F2. Version and path assumptions are stale

- `go.mod` requires `github.com/buchenberg/shepherd-kernel-go v0.3.2` (not v0.1.1).
- `shepherd_trace_dir` is empty by default (Shepherd tracing is opt-in); there is no hardcoded `~/.yaah/traces/trace.sqlite`. The trace store path is `filepath.Join(traceDir, "trace.sqlite")` (`internal/agent/pipeline/scope_init.go:26`), with `traceDir` read from `cfg.Agent.Default.ShepherdTraceDir`.

### F3. The four systems cannot be joined today (highest-leverage gap)

- OTel spans carry **no `session_id`** attribute (`internal/observability/` has no session reference; the only IDs are the W3C `trace_id`/`span_id`).
- `messages` and `sessions` carry **no `trace_id`**.
- Shepherd facts are keyed by `TraceOwnerID`, which for sub-agents is **not** the session ID — see F4.

Result: a Jaeger trace, a `messages` row, and a Shepherd fact describing the same turn cannot be correlated. One shared identifier fixes this cheaply without merging any storage. This is Phase 0.

### F4. Sub-agent trace owners differ from session IDs

`internal/agent/runner/runner.go:276` assigns sub-agents a synthetic owner `sub-{role}-{parentSession}-{unixnano}` (`subTraceID`), while messages are persisted under the parent `sessionID`. So `shepherd_facts.trace_owner_id = sessions.id` is false for all sub-agent work — any "sum tokens from Shepherd by session ID" (Phase 2 Option C) silently misses sub-agent tokens.

### F5. Token counts are written in three places, and sessions is the only unconditional one

- `sessions.tokens_in/out` — written unconditionally by `EndSession` (`cmd/yaah/session.go:132`, sums `totalUsage`).
- Shepherd `turn:completed` capture (`prompt_tokens`/`completion_tokens`) — **opt-in**, only when Shepherd tracing is enabled.
- OTel `llm.prompt_tokens`/`llm.completion_tokens` span events (`FinishLLM`/`FinishStream`) — **observational**, only when OTel is enabled.

Phase 2 as written makes Shepherd the source of truth, but Shepherd is disabled by default — that would regress the common case. Sessions should remain authoritative; Shepherd/OTel are derived.

### F6. Verbose OTel re-records full message content already in `messages`

When `OtelVerbose` is on, `RecordAssistantResponse`, `RecordConversation`, `RecordSystemPrompt`, and `RecordTUIView` (`internal/observability/trace.go`) write full content, reasoning, and tool-call args as span attributes/events — the same data already durably stored in `messages`. This is the clearest "same data, two sinks" and should be either removed (point users at `yaah session show`) or explicitly labeled a debug-only mirror.

### F7. Memory and session-message search machinery is duplicated

`memory_fts` vs `messages_fts`, and `SearchMemory`/`SearchMessages` plus `SearchMemoryVector`/`SearchMessagesVector`, are near-identical (FTS + `embedding` BLOB + cosine). The *data* should stay separate (different retention/scoping), but the *plumbing* can be one `SearchableText` abstraction. This is orthogonal to, and cheaper than, Phase 1.

---

## Consolidation Strategy

### Phase 0: Cross-link the systems with shared IDs (NEW — HIGHEST LEVERAGE, LOW RISK)

**Goal**: Make every sink queryable against the others without merging any storage (addresses F3/F4).

**Implementation**:
1. Add a `session_id` attribute to the root `prompt`/`agent.turn` span in `internal/observability/trace.go` (thread the session ID into `StartPrompt`/`StartTurn`, or set it once per run on the span context).
2. Add a nullable `trace_id` column to `messages` (set when OTel is active) so a message row can be joined to a Jaeger trace.
3. Introduce a stable `turn_id` shared by the turn span, the Shepherd `turn:*` facts (`payload["session_id"]` / `payload["turn_id"]`), and the `messages` rows of that turn.
4. Persist the `subTraceID → parentSession` mapping (already computed at `internal/agent/runner/runner.go:276`) so Shepherd owners and parent sessions are joinable (F4).

**Success**: `trace_id` → `session_id` → `messages` and `trace_owner_id` all resolve to the same conversation.

**Files**: `internal/observability/trace.go`, `internal/agent/persist.go`, `internal/memory/memory.go` (column), `internal/agent/pipeline/trace.go`.

---

### Phase 1: Co-locate Shepherd Store with Session DB — REVISED (BLOCKED by F1)

> ⚠ **Superseded.** The implementation below assumes a `shepherd.TraceStore` interface and a `facts`/`frontiers` schema that do not exist (Finding F1). Keep this section as reference only. The revised approach is:
>
> - **Option A — `ATTACH DATABASE`**: `ATTACH` is scoped to a single SQLite connection, but `NewSQLiteTraceStore` opens and owns its own `*sql.DB`, so it cannot reach an attachment on yaah's connection. This requires an upstream connection-injection API or a fork, so it is not viable as-is.
> - **Option B — fork/vendor the store**: single file, but re-implements digest/idempotency/witness/scope; violates Non-Goals.
> - **Option C — keep separate + Phase 0 linkage (recommended)**: two files, joined via `session_id`/`trace_id`; no schema risk.
>
> **Recommendation**: drop the "single file" goal; pursue Phase 0. A single file would require an upstream connection-injection API or a fork (Option B); keep Option C unless that dependency change is made.

**Goal (original)**: Move Shepherd trace store from separate `trace.sqlite` into the existing `state.db`.

**Rationale**: 
- Eliminates the two-database problem
- Atomic transactions across sessions + traces
- Simpler backup, migration, and management
- No functional changes to either system

**Implementation**:

1. **Add Shepherd tables to `memory/memory.go` migration**
   ```go
   // In migrate() function, add:
   CREATE TABLE IF NOT EXISTS shepherd_facts (
       id TEXT PRIMARY KEY,
       trace_owner_id TEXT NOT NULL,
       mode TEXT NOT NULL,  -- 'declaration' or 'capture'
       schema_ref TEXT NOT NULL,
       kind_label TEXT NOT NULL,
       payload BLOB NOT NULL,
       caused_by_ids BLOB,  -- JSON array of parent fact IDs
       created_at INTEGER NOT NULL
   );
   
   CREATE TABLE IF NOT EXISTS shepherd_frontiers (
       id TEXT PRIMARY KEY,
       target_trace_owner_id TEXT NOT NULL,
       through_fact_id TEXT NOT NULL,
       created_at INTEGER NOT NULL
   );
   
   CREATE INDEX IF NOT EXISTS idx_shepherd_trace_owner ON shepherd_facts(trace_owner_id);
   CREATE INDEX IF NOT EXISTS idx_shepherd_mode ON shepherd_facts(mode);
   ```

2. **Create SQLiteTraceStore wrapper**
   
   Create `internal/memory/shepherd_store.go`:
   ```go
   package memory
   
   import shepherd "github.com/buchenberg/shepherd-kernel-go"
   
   // NewShepherdTraceStore creates a shepherd.SQLiteTraceStore backed by the
   // shared yaah DB connection instead of opening a separate SQLite file.
   func NewShepherdTraceStore(db *DB) *shepherd.SQLiteTraceStore {
       // Use db.sql for all operations
       // Implement shepherd.TraceStore interface
   }
   ```

3. **Update `InitShepherdInfrastructure`**
   
   In `internal/agent/pipeline/scope_init.go`:
   ```go
   // Accept *memory.DB instead of traceDir path
   func InitShepherdInfrastructure(db *memory.DB, busBuffer int) (*shepherd.SQLiteTraceStore, *shepherd.EffectBus, *shepherd.ScopeManager, error)
   ```

4. **Update wiring**
   
   In `cmd/yaah/wiring.go`:
   ```go
   // Pass memory DB to shepherd init
   store, bus, mgr, err := pipeline.InitShepherdInfrastructure(db, cfg.Agent.Default.ShepherdBusBuffer)
   tools.SharedTraceStore = store
   tools.SharedScopeManager = mgr
   ```

5. **Migration for existing users**
   
   On first run with new code:
   - Detect if `~/.yaah/traces/trace.sqlite` exists
   - Migrate data to `state.db` shepherd tables
   - Rename old file to backup

**Files to modify**:
- `internal/memory/memory.go` - add Shepherd tables to migration
- `internal/memory/shepherd_store.go` - NEW, SQLiteTraceStore wrapper
- `internal/agent/pipeline/scope_init.go` - accept *memory.DB
- `cmd/yaah/wiring.go` - pass memory DB to shepherd init
- `cmd/yaah/trace.go` - update `openShepherdTraceStore()` to use `state.db`

**Test files to update**:
- `internal/agent/pipeline/config_test.go`
- `internal/agent/runner/checkpoint_integration_test.go`

**CLI impact**: None - transparent to users

---

### Phase 2: Deduplicate Token Storage — REVISED (direction corrected by F4/F5)

> ⚠ **Superseded.** The original plan makes Shepherd the source of truth, but Shepherd is opt-in (disabled by default) and sub-agent facts use `sub-*` trace owners (F4), so that would regress the default path and miss sub-agent tokens. OTel also writes per-call token events (`FinishLLM`/`FinishStream`), but only when enabled (F5).

**Revised goal**: keep `sessions.tokens_in/out` as the **authoritative, unconditional** total (already written by `EndSession` at `cmd/yaah/session.go:132`). Treat Shepherd `turn:*` token payloads and OTel `llm.*_tokens` events as **derived/observational**.

**Revised implementation**:
1. Make no change to `sessions.tokens_in/out` writes.
2. Optionally add `SessionTokenStats()` as a *read model* summing Shepherd facts when present — but never the primary store.
3. If a single source of truth is desired later, the direction is the opposite of the original: derive Shepherd/OTel from sessions, not sessions from Shepherd.

**Original rationale (for reference)**:
- Token counts stored in both `sessions.tokens_in/out` and Shepherd turn capture facts
- Single source of truth reduces write duplication and potential inconsistency

**Implementation Options**:

**Option A: Computed columns (SQLite 3.35.0+, 2021-03-12)**
```go
// In migrate(), add generated columns:
ALTER TABLE sessions ADD COLUMN tokens_in_computed INTEGER GENERATED ALWAYS AS (
    SELECT COALESCE(SUM(CASE WHEN mode = 'capture' AND schema_ref LIKE '%completed.v1' THEN json_extract(payload, '$.prompt_tokens') ELSE 0 END), 0)
    FROM shepherd_facts 
    WHERE trace_owner_id = sessions.id
) STORED;
```
*Problem*: SQLite GENERATED columns don't support subqueries. Not feasible.

**Option B: Trigger-based sync**
```sql
CREATE TRIGGER IF NOT EXISTS update_session_tokens_after_shepherd_capture
AFTER INSERT ON shepherd_facts
FOR EACH ROW
WHEN NEW.mode = 'capture' AND NEW.schema_ref LIKE '%completed.v1%'
BEGIN
    UPDATE sessions 
    SET tokens_in = tokens_in + COALESCE(json_extract(NEW.payload, '$.prompt_tokens'), 0),
        tokens_out = tokens_out + COALESCE(json_extract(NEW.payload, '$.completion_tokens'), 0)
    WHERE id = NEW.trace_owner_id;
END;
```
*Problem*: JSON extraction in triggers is complex and fragile.

**Option C: Remove from sessions, compute on read (RECOMMENDED)**
1. Add `SessionTokenStats()` method to `*DB`:
   ```go
   func (d *DB) SessionTokenStats(sessionID string) (tokensIn, tokensOut int, err error) {
       // Sum from Shepherd facts
       row := d.sql.QueryRow(`
           SELECT 
               COALESCE(SUM(CASE WHEN json_extract(payload, '$.prompt_tokens') THEN json_extract(payload, '$.prompt_tokens') ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN json_extract(payload, '$.completion_tokens') THEN json_extract(payload, '$.completion_tokens') ELSE 0 END), 0)
           FROM shepherd_facts 
           WHERE trace_owner_id = ? AND mode = 'capture' AND schema_ref LIKE '%completed.v1%'
       `, sessionID)
       err = row.Scan(&tokensIn, &tokensOut)
       return
   }
   ```

2. Deprecate `sessions.tokens_in/tokens_out` columns (keep for backward compat, don't write)
3. Update all readers to use `SessionTokenStats()` when Shepherd is enabled
4. Fallback to legacy columns when Shepherd is disabled

**Files to modify**:
- `internal/memory/session_repo.go` - add `SessionTokenStats()` method
- `internal/memory/memory.go` - add index on `shepherd_facts(trace_owner_id, mode, schema_ref)`
- `internal/agent/loop.go` - use new method for token reporting

**Backward compatibility**: Keep columns, just don't write to them. Read falls back to legacy if Shepherd disabled.

---

### Phase 3: Unified CLI Commands (LOW PRIORITY)

**Goal**: Merge `yaah session` and `yaah shepherd-trace` CLIs into a cohesive interface.

**Rationale**: Users shouldn't need to know which system stores which data.

**Proposed CLI**:

```bash
# List all sessions (combines sessions + trace owners)
yaah session list

# Show session details (messages + traces)
yaah session show <id>

# Show execution profile (aggregate stats from both)
yaah session profile <id>

# Legacy aliases (kept for compatibility)
yaah shepherd-trace list    # alias for yaah session list
yaah shepherd-trace show <id> # alias for yaah session show <id>
```

**Implementation**:

1. Rename `cmd/yaah/trace.go` → `cmd/yaah/session_trace.go`
2. Add `session show` subcommand that combines:
   - Session metadata from `memory.DB`
   - Messages from `memory.DB`
   - Shepherd facts from same DB
3. Keep `shepherd-trace` as deprecated aliases

**Files to modify**:
- `cmd/yaah/trace.go` - refactor into unified commands
- `cmd/yaah/session.go` - add show/profile subcommands

**Test files**: Minimal - CLI refactor

---

## Migration Path

> ⚠ **Superseded by F1.** This section assumes Phase 1's single-file merge, which is not feasible without forking `shepherd-kernel-go`. The real store schema is `records`/`path_entries`/`record_edges`/`contexts`/`append_intents`/`owner_ordinals`/`meta`/`frontiers` — not `facts`/`frontiers`. Keep this section as reference only if option B (fork) is later authorized.

### For Existing Users

**Before consolidation** (current state):
```
~/.yaah/
├── state.db          # sessions, messages, memories, todos
└── traces/
    └── trace.sqlite   # shepherd facts, frontiers
```

**After Phase 1**:
```
~/.yaah/
├── state.db          # sessions, messages, memories, todos, shepherd_facts, shepherd_frontiers
└── traces/
    └── trace.sqlite   # DEPRECATED - read-only for backward compat
```

**Migration on first run**: not applicable — there is no `facts`/`frontiers`
schema to migrate (the real store uses `records`/`path_entries`/`record_edges`/
`contexts`/`append_intents`/`owner_ordinals`/`meta`/`frontiers`). No
`MigrateShepherdIfNeeded` step is planned.

---

## File Change Summary

> The original Phase 1/2 file list (a `shepherd_store.go` wrapper,
> `MigrateShepherdIfNeeded`, `SessionTokenStats`, and their tests) is
> **non-actionable** — it depends on a `shepherd.TraceStore` interface and a
> `facts`/`frontiers` schema that do not exist (F1). The revised Phase 0 scope
> touches: `internal/observability/trace.go`, `internal/agent/persist.go`,
> `internal/memory/memory.go`, `internal/agent/pipeline/trace.go`.

### Deprecated (future cleanup)
| File/Feature | Status |
|--------------|--------|
| `~/.yaah/traces/trace.sqlite` | Deprecated after migration |
| `sessions.tokens_in/out` | Deprecated, read-only after Phase 2 |

---

## Testing Strategy

The original Phase 1/2 test plan (a `shepherd.TraceStore` wrapper test, a
`trace.sqlite` migration test, and `SessionTokenStats()` tests) is
**non-actionable** (F1). The revised Phase 0 scope is:

1. Root OTel span carries `session_id`; `messages` carry `trace_id`/`turn_id`.
2. `subTraceID → parentSession` mapping is joinable.
3. `yaah session` / `yaah shepherd-trace` still resolve a conversation end-to-end.

---

## Rollback Plan

If issues arise during migration:

There is no data migration to roll back — the two files stay separate
(Option C). The only reversible change is the Phase 0 linkage columns, which
are additive.

---

## Success Criteria

| Phase | Success Metric |
|-------|----------------|
| Phase 0 | `trace_id` → `session_id` → `messages` / `trace_owner_id` resolve to the same conversation; a Jaeger trace and a `messages` row are mutually navigable |
| Phase 1 (revised) | Two files retained but joined via shared IDs; `yaah shepherd-trace` CLI still works unchanged |
| Phase 2 (revised) | `sessions.tokens_in/out` remains authoritative; Shepherd/OTel treated as derived |
| Phase 3 | Unified `yaah session` CLI provides access to all session data in one place |

---

## Open Questions

1. **Should `sessions.tokens_in/out` be removed entirely or kept as computed cache?**
   - *Recommendation (revised)*: Keep as the authoritative total; do not compute from Shepherd (F4/F5).

2. **Should Shepherd frontiers be exposed via the unified session API?**
   - *Recommendation*: Yes, for supervised task rollback functionality

3. **How to handle users who disable Shepherd tracing?**
   - *Recommendation (revised)*: Irrelevant once sessions stays authoritative (Phase 2 revision); Shepherd/OTel are derived.

4. **Should the migration be automatic or require explicit user action?**
   - *Recommendation*: Moot while Phase 1 is rescoped to "keep separate + link" (F1). Revisit only if option B (fork) is authorized.

5. **What is the join key?** (`session_id` on spans, `trace_id` on messages, or a new `turn_id`?)
   - *Recommendation*: A new stable `turn_id` shared across the span attribute, Shepherd fact payload, and `messages` row; plus `session_id` on the root span as the coarse key.

---

## Related Work

- `docs/supervised-task-plan.md` - Shepherd infrastructure wiring
- `internal/agent/pipeline/scope_init.go` - Current Shepherd initialization
- `internal/tools/supervisor_shared.go` - Global SharedTraceStore/SharedScopeManager
