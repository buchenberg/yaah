---
name: memory-search-sessions-overhaul
description: Fix memory_search_sessions: structured results, filters, relevance floor, tool-call exclusion, doc/behavior parity
status: draft
---

## Problem

`memory_search_sessions` (`internal/tools/memory.go:237`, backed by
`internal/memory/message_repo.go:99` + `126`) has three classes of defects
uncovered by an 8-probe smoke test:

**A. Return shape does not match the documented "session search" framing.**
Semantic hits are returned as a plain concatenated string of message `Text`
values with a score. `VectorResult` carries `Entry.ID` but the tool discards
it — there is no `session_id`, `role`, `timestamp`, or `message_id` in the
output. The promise "sub-agent sessions marked `[SUB]`" only applies to the
empty-query listing branch (`listRecentMessages`), never to semantic hits.

**B. Result quality is dominated by past tool-call arguments.** Tool calls
are stored as messages and get embedded alongside real user/assistant text,
so queries like `"platform-router PMA rules composition"` return old
`memory_add` argument payloads at scores 0.72–0.79 instead of actual
conversation content. Every high-scoring hit in probes 3, 4, 6, 7 was a
tool-call arg blob.

**C. No relevance floor, silent path selection, and inconsistent parameter
surface.** Query `"asdkfjhqwoeirujzxcv nonsense token"` returned 3 hits at
0.44–0.46 with no indication they are noise. Semantic vs. FTS fallback is
chosen silently. Whitespace-only queries (`" "`) bypass the listing branch
because the check is literal string equality. `memory_search` (the non-
session tool) has an undocumented `tag` filter; `memory_search_sessions`
has no filters at all.

## Evidence (smoke test, 2026-08-31)

| # | Query | top_k | Path | Issue |
|---|---|---|---|---|
| 1 | "dolt setup on windows" | 3 | semantic | all 3 hits from current session; no session_id in output |
| 2 | (empty) | 1 | listRecentMessages | works as documented |
| 3 | "platform-router PMA rules composition" | 5 | semantic | top hits are `tool:memory_add` arg payloads, not discussion |
| 4 | "supervised_task rollback demo checkpoint" | 5 | semantic | same — tool-call payload pollution |
| 5 | "asdkfjhqwoeirujzxcv nonsense" | 3 | semantic | returns 3 hits at 0.44–0.46, no threshold, no signal |
| 6 | "supervised_task developer sub-agent" | 2 | semantic | both hits `[tool:memory_add]` args |
| 7 | "yaah" | 20 | semantic | 20 tool-call blobs; **zero** `[SUB]` markers |
| 8 | " " (whitespace) | 3 | semantic | bypasses listing branch — should be empty-query |

## Bugs itemized

1. **No structured metadata in semantic output.** `internal/tools/memory.go:281`
   formats `"[semantic] %s  (score: %.2f)"` — drops session_id, role, timestamp.
2. **`[SUB]` marker only in empty-query branch** — docs imply it's global.
3. **Silent path selection.** `if err == nil && len(vecResults) > 0`
   (`memory.go:279`) — never distinguishes "semantic returned nothing" from
   "FTS returned nothing." Caller can't tell which path ran.
4. **No relevance threshold.** Gibberish hits are returned uniformly.
5. **Tool-call payload pollution.** Message table stores tool_calls arg JSON
   as content; embedder embeds it; search surfaces it. Search should default
   to conversation-only.
6. **Whitespace bypasses listing.** `params.Query == ""` should be
   `strings.TrimSpace(params.Query) == ""`.
7. **Potential panic at `memory.go:299`**: `m.SessionID[:12]` assumes ≥ 12 chars.
8. **O(N²) bubble sort** at `message_repo.go:174-181` and `memory.go:585-591`.
   Replace with `sort.Slice`. Also full-table scan — no time or session
   filter pushed to SQL.
9. **Doc/schema mismatch.** Description says "search past conversation
   sessions" (session granularity); behavior returns message hits.
10. **Inconsistent surface with `memory_search`** — sibling tool exposes
    `tag`, this one exposes nothing.

## Design

### New tool contract

```go
// Args
type SessionSearchArgs struct {
    Query           string   `json:"query"`
    TopK            int      `json:"top_k"`             // default 10
    Mode            string   `json:"mode"`              // "messages" (default) | "sessions"
    ExcludeCurrent  bool     `json:"exclude_current"`   // default false
    SessionID       string   `json:"session_id"`        // pin to one session
    Since           string   `json:"since"`             // RFC3339 or YYYY-MM-DD
    Until           string   `json:"until"`
    Roles           []string `json:"roles"`             // ["user","assistant"] default
    IncludeToolCalls bool    `json:"include_tool_calls"` // default false — hides tool_call arg payloads
    MinScore        float32  `json:"min_score"`         // default 0 (no floor)
}

// Hit (JSON, not stringified)
type SessionHit struct {
    SessionID  string  `json:"session_id"`
    MessageID  string  `json:"message_id"`
    Role       string  `json:"role"`
    Timestamp  string  `json:"timestamp"`   // RFC3339
    IsSubAgent bool    `json:"is_sub_agent"`
    Score      float32 `json:"score"`       // 0 for FTS
    Path       string  `json:"path"`        // "semantic" | "fts"
    Snippet    string  `json:"snippet"`
}

// Envelope
type SessionSearchResult struct {
    Path    string       `json:"path"`             // which branch ran
    Hits    []SessionHit `json:"hits"`
    Count   int          `json:"count"`
    Skipped struct {
        BelowMinScore int `json:"below_min_score"`
        ToolCalls     int `json:"tool_calls"`
    } `json:"skipped"`
}
```

`mode: "sessions"` groups hits by `session_id`, returns each session's top hit
+ score + hit_count, honoring the framing in the docstring.

### Schema changes

Add `message_type` column to `messages` (or reuse existing role — proposal:
add `is_tool_call BOOLEAN NOT NULL DEFAULT 0`, index it). Backfill during
Phase 0 migration. `IncludeToolCalls=false` (default) filters at SQL level.

Push `session_id`, `since`, `until`, `role IN (…)`, `is_tool_call = 0` into
the `WHERE` clause of `SearchMessagesVector` so cosine is only computed on
the candidate set.

### Score floor

If `MinScore > 0`, drop hits below it and report count in `Skipped`. Also
add package-level `DefaultMinScore` constant so future tuning doesn't
require API changes.

### Path selection

Always run both branches when both are viable; prefer semantic but ALWAYS
report `path` in the envelope so callers can see which ran. On semantic
empty *and* FTS empty, return `{path:"none", hits:[], count:0}` instead of
the "No matching …" prose string.

## Phases

### Phase 0 — Characterization test (baseline)
- Add `internal/tools/memory_test.go` cases mirroring the 8 probes above.
- Assert current (broken) behavior so the refactor is measurable.
- Deliverable: red tests demonstrating each bug.

### Phase 1 — Structured JSON output (no schema change)
- Change `MemorySessionSearchTool.Execute` to marshal `SessionSearchResult`
  as JSON.
- Populate `SessionID`, `MessageID`, `Role`, `Timestamp`, `Path`, `Score`,
  `Snippet` from `VectorResult` — requires broadening `VectorResult` to
  carry those fields (currently only `Entry{ID,Text} + Score`).
- Update `SearchMessagesVector` to `SELECT id, session_id, role, created_at,
  content, embedding` and populate all fields.
- Detect `is_sub_agent` via `strings.Contains(session_id, "-sub-")`.
- Fix `SessionID[:12]` panic in FTS branch (`safeShort` helper).
- Replace bubble sort with `sort.Slice`.

### Phase 2 — Filters & floors
- Add `min_score`, `session_id`, `since`, `until`, `roles`, `exclude_current`
  args + schema.
- Push filters into SQL `WHERE`.
- `TrimSpace` on query before empty check.
- Populate `Skipped.BelowMinScore`.
- Report `path` always; return `{path:"none", ...}` for total misses.

### Phase 3 — Tool-call exclusion (migration)
- Migration: `ALTER TABLE messages ADD COLUMN is_tool_call INTEGER NOT NULL
  DEFAULT 0`; backfill via heuristic (JSON-parseable + contains
  `"tool_calls"` OR role == "tool").
- Default `include_tool_calls = false`; add to SQL `WHERE`.
- Populate `Skipped.ToolCalls`.
- Verify probes 3/4/6/7 no longer surface old `memory_add` payloads.

### Phase 4 — Sessions mode
- Implement `mode: "sessions"`: group by `session_id`, keep best hit,
  attach `hit_count`, sort by best score.
- Empty-query + `mode: "sessions"` = current `listRecentMessages` behavior
  in structured form.
- Unify `[SUB]` marking across both branches (`is_sub_agent` field).

### Phase 5 — Docs, prompt, MCP schema
- Update `internal/prompts/toolinfo/memory_session_search.md` (create if
  absent) with new contract.
- Update MCP schema in `Schema()` to reflect all new fields with
  descriptions.
- Add examples to `docs/features.md` (memory section).
- Update `internal/tui/components/toolblock/toolblock.go` rendering for
  the JSON envelope.

### Phase 6 — Optional: ANN index
- Investigate `sqlite-vss`/`sqlite-vec` under `modernc.org/sqlite`. If
  supported without CGO, migrate cosine to an ANN query. Otherwise document
  the O(N) tradeoff and add a `max_scan` safety cap.

## Non-goals

- Not touching `memory_search` (facts store) except where a bug is shared
  (bubble sort in `memory.go:585-591`).
- Not renaming the tool.
- Not adding a cross-encoder rerank pass.

## Rollout & compatibility

Output shape change is breaking for any consumer that string-matched the
old `[semantic] ...` prefix. Grep the repo for those literals (TUI + REPL
render paths) and update before shipping Phase 1. MCP callers get a JSON
object instead of prose — considered an improvement, called out in
CHANGELOG.

## Related follow-ups (file separately)

- Yaah issue: `memory_search` should gain `min_score` and structured JSON
  output for parity.
- Yaah issue: embedder should skip tool_call arg messages at insert time
  (defense in depth on top of the query-time filter).
- Doc issue: AGENTS.md tool description of `memory_search_sessions` claims
  session-granularity — update once Phase 4 lands.

