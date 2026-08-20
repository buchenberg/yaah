---
name: persistence-hardening
description: Adopt the useful parts of shepherd-kernel-go's append-only store (SQLite hardening, exact content dedup, idempotent writes) in yaah's session/memory storage
status: draft
---

# Plan: Harden yaah's SQLite persistence (adopt shepherd-store techniques)

## Goal

Bring three properties from `shepherd-kernel-go`'s `SQLiteTraceStore` into yaah's
`internal/memory/` (`~/.yaah/state.db`) without importing the parts that are
specific to supervised execution:

1. **SQLite hardening** — `busy_timeout`, `foreign_keys=ON`, applied per-connection.
2. **Exact memory dedup** — content-addressed dedup (SHA-256 digest) instead of fuzzy FTS matching.
3. **Idempotent message persistence** — a re-persist of the same message is a no-op, not a duplicate or a UNIQUE error.

These map to the durable wins identified in the shepherd-store analysis; the
provenance/witness/authorization machinery and the fact-DAG/scope/checkpoint
layers are explicitly out of scope.

## Background

`../shepherd-kernel-go/store.go` demonstrates three techniques that yaah's
`state.db` currently lacks:

| Technique | Shepherd | yaah today |
|-----------|----------|------------|
| Busy-timeout / FK hardening | `store.go:62-76` (`SetMaxOpenConns(1)`, WAL, `busy_timeout=5000`, `foreign_keys=ON`) | WAL only (`internal/memory/memory.go:98`); FK on `messages.session_id` is declared (`memory.go:143`) but never enforced |
| Exact dedup | `record_id` = canonical SHA-256 (`canonical.go:170`); `insertRecordIfMissing` short-circuits on identical content (`store.go:1395-1408`) | `AddMemoryDedup` uses FTS `MATCH` (`memory.go:323-335`), which is fuzzy, not exact |
| Idempotent writes | `append_intents(append_intent_id → batch_digest → receipt)` returns the prior receipt on retry (`store.go:499-516`) | `AddMessage` is an unconditional `INSERT` keyed by `(session_id, idx)` (`internal/memory/message_repo.go:8-17`); a retry of the same position errors |

## Non-Goals

- Do NOT import the witness/authorization chain (`ensureAppendAuthorized`/`ensureReadAuthorized`, root witness, `PresentedAuthorityRefs`).
- Do NOT import `RetainedContext` / schema-environment refs, or the declaration/capture taxonomy.
- Do NOT add a `record_edges` causal-DAG to `messages` — `tool_call_id`/`tool_name`/`tool_calls` (`memory.go:58-61`) already carry the tool→result linkage.
- Do NOT make `memory` immutable/append-only — `UpdateMemory`/`DeleteMemory` are legitimate for a user-editable knowledge base.
- Do NOT merge the two SQLite files (`state.db` vs `trace.sqlite`) — that is blocked by the shepherd schema (see `consolidate-persistence` plan, Finding F1).

---

## Item 1 — SQLite hardening (per-connection pragmas)

**Problem**: `memory.Open` sets only `PRAGMA journal_mode=WAL`. yaah has
concurrent writers (the debounced writer, FTS triggers, and the background
embedding goroutines `EmbedMemoryAsync`/`embedMessageAsync`), so a concurrent
write can return `SQLITE_BUSY`; and the `messages.session_id → sessions(id)`
FK is dead because `foreign_keys` is never enabled.

**Key subtlety**: `PRAGMA` set via `db.Exec(...)` only affects *one* pooled
connection. shepherd works around this with `SetMaxOpenConns(1)`, so its single
connection always has the pragmas. yaah benefits from WAL read concurrency
(search during a turn + background embedding UPDATEs), so a single connection
is too restrictive. Instead use modernc's `_pragma` DSN query parameters,
which are applied to **every** new connection (verified in
`modernc.org/sqlite@v1.53.0/sqlite.go:applyQueryParams`; `busy_timeout` is
applied first by the driver).

**Change** — `internal/memory/memory.go` `Open()` (currently `sql.Open("sqlite", path)`):

```go
sep := "?"
if strings.Contains(path, "?") {
    sep = "&"
}
dsn := path + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
db, err := sql.Open("sqlite", dsn)
```

Notes:
- `busy_timeout(5000)` mirrors shepherd (`store.go:72`).
- `foreign_keys(1)` enables enforcement of the already-declared FK.
- Existing query parameters are preserved (separator is `&` when a `?` is already present).
- `_txlock=immediate` makes `Begin()` take the write lock up front, so the `AddMemoryDedup` transaction (Item 2) is atomic.
- Leave the connection pool at its default; do **not** copy `SetMaxOpenConns(1)` (see Open Questions).

**Files**: `internal/memory/memory.go`.

**Tests to update**: any test that inserts a `messages` row without first
`CreateSession`-ing the parent will now fail on the FK — add the parent session
(or use a helper). Audit `memory_test.go`, `session_repo`/`message_repo` tests.

---

## Item 2 — Exact memory dedup via content digest

**Problem**: `AddMemoryDedup` (`memory.go:323-335`) detects duplicates with FTS
`MATCH`, which tokenizes and can match near-duplicates — broader and fuzzier
than the documented "identical text" intent. A content digest gives exact,
idempotent dedup like shepherd's content-addressed records.

**Changes**:

1. **Schema**: add a nullable `digest` column (no unique index — soft dedup via
   `AddMemoryDedup` keeps `AddMemory`/`UpdateMemory` semantics unchanged). The
   migration is guarded so repeated `Open()` is idempotent, and returns errors:

   ```go
   // in migrate(), alongside the existing embedding migration
   row := d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('memory') WHERE name = 'digest'")
   var hasDigest bool
   if err := row.Scan(&hasDigest); err != nil { return err }
   if !hasDigest {
       if _, err := d.sql.Exec("ALTER TABLE memory ADD COLUMN digest TEXT"); err != nil { return err }
   }
   ```

2. **Digest helper** in `internal/memory/memory.go`:

   ```go
   func memoryDigest(text string) string {
       sum := sha256.Sum256([]byte(text))
       return hex.EncodeToString(sum[:])
   }
   ```

   (yaah's memory text is a plain string, so a plain SHA-256 suffices — no need
   for shepherd's canonical-JSON key-sorting, which exists only for `map[string]any` payloads.)

3. **`AddMemory`** stores the digest; **`AddMemoryDedup`** does an atomic
   lookup+insert inside one write transaction (the DSN's `_txlock=immediate`
   makes `Begin()` take the write lock, so concurrent dedup calls serialize):

   ```go
   func (d *DB) AddMemoryDedup(e Entry) (string, error) {
       digest := memoryDigest(e.Text)
       tx, err := d.sql.Begin()
       if err != nil { return "", err }
       defer tx.Rollback()
       var dupID string
       err = tx.QueryRow(`SELECT id FROM memory WHERE digest = ? LIMIT 1`, digest).Scan(&dupID)
       if err == nil { return dupID, nil }
       if err != sql.ErrNoRows { return "", err }
       if _, err := tx.Exec(`INSERT INTO memory (...) VALUES (...)`, ...); err != nil { return "", err }
       if err := tx.Commit(); err != nil { return "", err }
       return "", nil
   }
   ```

4. **`UpdateMemory`** recomputes the digest when text changes.

5. **Backfill** existing rows (mirrors `ReconcileEmbeddings`): a
   `ReconcileMemoryDigests` pass that sets `digest` for rows where it is `NULL`.

**Normalization**: `memoryDigest` hashes the raw text exactly as stored (no
`TrimSpace`). Every digest producer (`AddMemory`, `AddMemoryDedup`,
`UpdateMemory`, `ReconcileMemoryDigests`) calls the same `memoryDigest` helper,
so the rule is applied consistently.

**Files**: `internal/memory/memory.go`.

---

## Item 3 — Idempotent message persistence

**Problem**: `SessionPersister.Persist` (`internal/agent/persist.go:36-75`) mints
a random `newMessageID()` and a counter `idx` per call, then writes through
`DebouncedWriter` (`internal/memory/debounce.go`) which coalesces by `m.ID`.
Two consequences:

- A retry of the same logical message mints a **new random ID and a new idx**,
  so the debouncer can't coalesce it and the DB gets a duplicate/error.
- `AddMessage` does a bare `INSERT` against `PRIMARY KEY (session_id, idx)`, so
  re-persisting the same position returns a UNIQUE error rather than a no-op.

**Changes**:

1. **DB layer — upsert on `(session_id, idx)`** in `message_repo.go` `AddMessage`:

   ```sql
   INSERT INTO messages (...) VALUES (...)
   ON CONFLICT(session_id, idx) DO NOTHING
   ```

   Semantics: a retry with the same deterministic ID is a no-op; a different
   message at the same position returns a conflict error (never silently
   dropped). Only a newly inserted row starts background embedding.

2. **ID layer — stable message ID** in `persist.go`: replace `newMessageID()`
   with a deterministic ID over **all** immutable persisted fields (session,
   position, role, content, reasoning, tool name, tool-call ID, tool-calls):

   ```go
   func messageID(sessionID string, idx int, role, content, reasoning, toolName, toolCallID, toolCalls string) string {
       h := sha256.Sum256([]byte(fmt.Sprintf(
           "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
           sessionID, idx, role, content, reasoning, toolName, toolCallID, toolCalls)))
       return hex.EncodeToString(h[:])
   }
   ```

   Benefits: the debouncer's `pending[m.ID]` map now coalesces a re-submitted
   message before flush, and the embedding goroutine's
   `UPDATE messages SET embedding = ? WHERE id = ?` targets a stable row. The ID
   doubles as a content fingerprint, so `AddMessage` can detect a *different*
   message occupying the same position (compare stored `id` on conflict).

   Historical rows keep their existing random IDs; no backfill required
   (ID is not part of any uniqueness constraint — `(session_id, idx)` is).

**Explicitly NOT doing**: content-dedup of messages. Identical assistant
content at different turns/positions is legitimate, so dedup is position-keyed
(idempotency), not content-keyed — unlike `memory`.

**Files**: `internal/memory/message_repo.go`, `internal/agent/persist.go`.

---

## Migration & backfill

- `memory.digest` is additive (`ALTER TABLE ADD COLUMN`). No breaking change;
  `ReconcileMemoryDigests` backfills legacy rows on open (wired into `migrate()`).
- `messages` upsert needs no schema change — `PRIMARY KEY (session_id, idx)`
  already exists.
- `foreign_keys=ON` is the only behavior change with test fallout: inserts that
  reference a missing session now fail.

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/memory/memory.go` | `_pragma` + `_txlock` DSN (preserving existing params); guarded `digest` migration with error handling; `AddMemory`/`AddMemoryDedup`/`UpdateMemory` digest support; atomic transactional dedup; `ReconcileMemoryDigests` (wired into `migrate`) |
| `internal/memory/message_repo.go` | `AddMessage` → idempotent `ON CONFLICT(session_id, idx) DO NOTHING` with conflict validation; embed only on insert |
| `internal/agent/persist.go` | deterministic `messageID(...)` over all immutable fields, replacing `newMessageID()` |

No new dependencies (`crypto/sha256`, `encoding/hex` are stdlib).

---

## Testing Strategy

### Unit
1. **Pragmas**: open a DB, assert `PRAGMA foreign_keys` is 1 and `PRAGMA busy_timeout` is 5000 from a *freshly pooled* connection (not just the `Open` caller's connection); `sqliteDSN` preserves existing query params and appends the pragmas.
2. **FK enforcement**: inserting a `messages` row for a missing session now errors.
3. **Digest dedup**: `AddMemoryDedup` returns the existing ID for identical text; adds when text differs; `UpdateMemory` re-dedups correctly; backfill sets digests.
4. **Message idempotency**: calling `AddMessage` twice with the same `(session_id, idx)` and content yields one row and no error; a different message at the same position returns a conflict error; the debouncer coalesces a re-submitted message with the same deterministic ID.
5. **Migration idempotency**: `Open()` twice on the same file succeeds (guarded digest column add).

### Integration
- Run a short agent session; confirm messages persist, `yaah session show` unchanged, and `yaah memory` dedup still works.

---

## Rollback

- Item 1: drop the `_pragma` query param to revert to WAL-only (no data change).
- Item 2: the `digest` column is additive; revert the `AddMemoryDedup` path to
  FTS if the exact-match semantics regress any workflow.
- Item 3: reverting `AddMessage` to a bare `INSERT` restores uniqueness errors
  on conflict (not position-overwrite). If overwrite-on-conflict is ever
  required, use an explicit `ON CONFLICT(session_id, idx) DO UPDATE` policy.

---

## Open Questions

1. **`SetMaxOpenConns(1)`?** shepherd uses it; yaah's background embedding
   writes + concurrent search reads suggest keeping a small pool under WAL with
   `busy_timeout`. Confirm via a quick concurrency smoke test. *Recommendation*:
   keep default pool, rely on `_pragma busy_timeout`.
2. **Digest normalization**: raw text (as stored) — resolved; no `TrimSpace`.
3. **Deterministic message ID length**: full SHA-256 hex (64 chars) vs truncated
   (e.g. 32 chars)? Purely cosmetic; *Recommendation*: keep full hex, consistent
   with `memory.digest`.
4. **Should `AddMemoryDedup` keep the FTS path as a fuzzy fallback** for legacy
   rows that predate the `digest` column? *Recommendation*: no — backfill covers
   them; keep one exact code path.
