---
name: todowrite-bd-adapter
description: Wire todowrite to bd (beads) transparently when available; keep in-memory fallback. Honors AGENTS.md directive without prompt changes.
status: draft
---

## Problem

`AGENTS.md` loudly asserts "Use `bd` for ALL task tracking — do NOT use TodoWrite," but the `todowrite` tool (`internal/tools/todo.go`) unconditionally writes to an in-memory `todo.Store`. Result: the model calls `todowrite` (its trained default), the AGENTS directive is quietly violated, and the "durable" todo state evaporates at session end.

## Goal

When `bd` is available in the current workspace, `todowrite` transparently persists to bd. When bd is absent (missing binary, uninitialized repo, unreachable server), `todowrite` falls back to the existing in-memory store with no behavior change. The model's prompt need not change — the tool honors the AGENTS.md directive by construction.

## Non-goals

- No new `bd` tool. Direct bd access stays in `powershell` for now (revisit if this adapter is a hit).
- No changes to the `todowrite` schema. The tool's input contract is stable.
- No two-way sync. bd remains authoritative once used; todowrite is a write-through cache.

## Design

### Detection (once per session, cached)

New package `internal/bd/` with:

```go
type Availability struct {
    Available bool
    BinaryPath string
    Reason string // when not available: "not-on-path" | "no-workspace" | "server-unreachable"
    CheckedAt time.Time
}

func Detect(ctx context.Context) Availability
```

Detection steps:
1. `exec.LookPath("bd")` — if missing → `not-on-path`.
2. Run `bd where --json` with 2 s timeout — if non-zero → `no-workspace`.
3. Run `bd status --json` with 2 s timeout — if non-zero → `server-unreachable`.
4. Cache the result on the tool struct for the lifetime of the session; re-check every 5 minutes lazily.

### Adapter behavior in `TodoWriteTool.Execute`

For each todo in the incoming list:

| Case | Action |
|---|---|
| bd unavailable | current behavior (in-memory `Store.Set`) — no change |
| bd available, todo has no `bd_id` tag, status = `pending`/`in_progress` | `bd create` with `--type=task --priority=2 --title=<content>` and capture the returned id; annotate the todo with `bd:yaah-xxx` prefix in-memory for future turns |
| bd available, todo has `bd_id`, status changed | `bd update <id> --status=<mapped>` |
| bd available, todo status = `completed` | `bd close <id> --reason="Completed via todowrite"` |
| bd available, todo status = `cancelled` | `bd close <id> --reason="Cancelled via todowrite"` (status_reason if bd supports it) |
| bd call fails mid-batch | log warning to observability, fall back to in-memory for the rest of the batch, return partial-success in the tool output |

Status mapping: `pending → open`, `in_progress → in_progress`, `completed → closed`, `cancelled → closed`.

### Tool output shape

`todowrite` currently returns a formatted table string. Extend to include a header line when bd was used:

```
[bd] persisted 3 todos to beads (yaah-a1b, yaah-a1c, yaah-a1d)
    ┌─ Todo list ────────────────
    │ ...
```

When bd is unavailable, prepend an info line ONCE per session (not per call):

```
[bd] unavailable (reason: not-on-path) — todos are session-local
```

### Config

New `~/.yaah/config.yaml` block:

```yaml
todo:
  bd_adapter: auto   # auto | on | off
```

`off` is escape hatch for users who dislike the adapter; `on` errors if bd is unavailable (strict mode for CI); `auto` is the default described above.

## Phases

### Phase 0 — Characterization
- Snapshot current `todowrite` behavior in tests. Assert bd-absent path is unchanged post-refactor.

### Phase 1 — Detection package
- `internal/bd/detect.go` + tests. No consumers yet.
- `bd where --json` and `bd status --json` invocation with timeouts.
- 5-minute cache with `sync.Once`-style lazy re-check.

### Phase 2 — Adapter package
- `internal/bd/adapter.go`: `type Adapter interface { Sync(ctx, []Todo) ([]TodoWithID, error) }`.
- Wire to `bd` CLI via `exec.CommandContext` — JSON in/out.
- Unit-test with a `fakeBD` process (small Go binary in `internal/bd/testbd/`).

### Phase 3 — Wire into TodoWriteTool
- `TodoWriteTool` gets an optional `Adapter` field.
- `Execute` calls adapter first; on error or unavailable, falls back to `Store`.
- Composition-root wiring in `cmd/yaah/wiring.go`.
- Output prefix lines per design.

### Phase 4 — Config
- Load `todo.bd_adapter` from `~/.yaah/config.yaml`.
- Respect `off` / `on` / `auto`.
- Update `docs/configuration.md`.

### Phase 5 — Doctor integration
- `yaah doctor` adds a "bd/dolt" section: dolt on PATH, dolt version, `.beads/` present, `bd status` reachable, adapter mode setting.
- Prints one-liner fixes when a check fails (e.g. "Server unreachable — see skill dolt-server-lifecycle").

### Phase 6 — Docs
- Update AGENTS.md tool description to reflect the adapter.
- `docs/features.md`: new "Todo persistence via bd" subsection.
- Remove the todowrite/beads contradiction language.

## Risks

- **Latency**: 3–5 bd calls per todowrite invocation on a slow disk (Dolt server hop is ~10 ms local). Batch into a single `bd batch` call if throughput matters.
- **Schema drift**: bd upstream may change flag names. Pin behavior with an integration test that runs against a real `bd` when available, skipped otherwise.
- **Model confusion**: the model may see `bd:yaah-xxx` prefixes on todo content and rewrite them. Mitigate by carrying the id in a side channel (map keyed by content hash) rather than in the visible text.

## Related follow-ups

- Skill `beads` needs a short section on "todowrite adapter": what to expect when it's on.
- Skill `dolt-server-lifecycle` (just created) covers the failure mode when the sidecar server is down.
- Possible future: `bd` tool (narrow: create/show/list/ready/close) if the adapter proves the value.

