# Compaction-as-Sleep — Expanded Design Note

> **Bead:** `yaah-u4r` · **Type:** spike · **Priority:** P3 · **Label:** agent
> **Title:** *spike: compaction-as-sleep — policy-driven, boundary-aware, memory-consolidating compaction middleware*
> **Status:** implemented (P0–P3, guardrails, hygiene, and introspection shipped; see §8)
> **Last expanded:** 2026-10-22 (hermes learning-loop analysis folded in — see §3–§6)

---

## 1. Premise

yaah already compacts context on overflow (`internal/agent/loop.go` `Loop.Compact`, `internal/agent/context_manager.go` `compactContext`/`applyCompactedSummary`/`PreviousSummary`). But compaction is **reactive** — it fires only when the context window overflows, and its only product is a *summary string* that replaces history. Nothing is *consolidated*; nothing is *learned*.

This spike asks: what if compaction were reimagined as **sleep** — a policy-driven, boundary-aware stage that not only compresses context but **consolidates durable value** (memory, skills, plans) out of the session, on a schedule, at safe boundaries, rather than waiting for overflow?

The framing unifies five original directions. A deep read of **NousResearch/hermes-agent** (folded in below) adds a sixth — the **proactive consolidation trigger** — plus a concrete reference implementation and a guardrail catalog to crib.

---

## 2. Original directions (condensed)

1. **Adaptive compaction threshold.** Drive the trigger from the `IneffectiveCompactions` telemetry signal rather than a hardcoded ratio, so compaction fires when it would actually help.
2. **Natural-boundary scheduling.** Compact at semantic boundaries (tool-call completion, sub-agent settle, turn end) instead of mid-stream — including a currently **missing sub-agent `onDrain` compaction hook**.
3. **Compaction-as-consolidation into long-term memory.** Make the compaction pass *also* extract durable facts/skills into yaah's memory store, instead of discarding them into a summary.
4. **Multi-level memory.** Short/medium/long tiers so a session's working context, recent summary, and durable memory don't share one structure.
5. **Staged light-vs-deep sleep.** A cheap "light sleep" (compress recent turns) vs. an expensive "deep sleep" (full consolidation), chosen by policy.

**Code anchors:** `internal/agent/pipeline/compaction.go` (`CompactionMiddleware` `PrepareStep`/`PostTool`), `internal/agent/pipeline/config.go` (middleware composition; sub-agents currently use `CtxMgr` pruning, *not* pipeline compaction), `internal/agent/context_manager.go`, `internal/agent/loop.go` (`Loop.Compact` + overflow recovery via `result.CompactedMessages`).

**Cross-refs:** `yaah-8uz`, `yaah-e4b`, `yaah-cgq`, `yaah-car`.

---

## 3. New input: hermes's learning loop (de-mystified)

The hermes "built-in learning loop" is not a knowledge graph and not vector memory. Read at code level, it is **three durable artifacts + one scheduled consolidation trigger**.

### 3.1 The artifacts

| Artifact | What it is | Storage |
|---|---|---|
| **`SKILL.md` directories** | "procedural memory" — narrow "how to do X" | `~/.hermes/skills/<skill>/SKILL.md` + optional `references/ templates/ scripts/ assets/` |
| **`MEMORY.md` + `USER.md`** | "declarative memory" — env/tool quirks + who the user is | flat text, entries joined by `§`, char-budgeted (2200 / 1375) |
| **ledger + usage sidecar** | mutation undo-log + per-skill counters | `.curator_ledger.jsonl` + `.usage.json` |

Key fact: `MEMORY.md`/`USER.md` have **no IDs, no tags, no timestamps, no embeddings**. `agent/learning_graph.py` is a *read-only render* for the desktop UI, assembled from those files at render time — it is **not** a persisted store.

### 3.2 The trigger — a counter-gated background fork ("the nudge")

This is the actual "loop," and it is the single most transferable idea:

1. Two counters advance as a side effect of normal work: `_turns_since_memory` (per user turn) and `_iters_since_skill` (per tool iteration) — `agent/turn_context.py:752`, `agent/turn_iteration_prep.py:167`.
2. At turn end, `agent/turn_finalizer.py:762-791` checks thresholds (`memory.nudge_interval = 10` user turns; `skill_nudge_interval = 10` tool iterations).
3. If crossed, `_spawn_background_review()` forks a **second agent in a background thread** with:
   - a **restricted toolset** — `memory` + `skills` + `read_file` + `search_files`, but `write_file`/`patch`/`terminal` **denied** (`_review_tool_whitelist`, `background_review.py:1079`);
   - `max_iterations = 16`;
   - prompt-cache parity with the parent (`build_cache_parity_fork`);
   - a **recursion guard** — the reviewer's own nudge intervals are forced to 0 (`background_review.py:986`), so it cannot spawn its own review.
4. The fork reviews the conversation and persists learnings via `skill_manage(...)` / `memory(...)`.

`/learn` is explicitly **not** an engine — `agent/learn_prompt.py:136` is a prompt rewrite ("no distillation engine, no model-tool footprint"), turned into an ordinary agent turn.

### 3.3 The curation lifecycle + guardrails

What makes unattended self-modification safe enough to run:

- **Provenance stamping** — `created_by ∈ {agent, learn, installed}`; only `agent`-created skills are "curator-managed" (a user's `/learn` skill is never silently rewritten by autonomous curation).
- **Usage + quality signal** — `.usage.json` tracks `use_count`/`view_count` and a `reuse_after_patch` flag ("was this skill used again *after* it was patched?"), the closest thing in the codebase to a skill-quality metric.
- **Archive-not-delete** — skills move to `skills/.archive`, never hard-deleted; restorable.
- **Append-only ledger with rollback** — `.curator_ledger.jsonl` stores content-addressed `before`/`after` hashes per mutation, delta-compressed, fail-open ("NEVER raises").
- **Structural validation** — frontmatter must parse, `name`+`description` required, `description ≤ 60` for new skills (because the skill *index* truncates at 57 chars — "a long description destroys the routing signal"), body non-empty.
- **Security scan** on every write (`_security_scan_skill`), plus install gating (`tools/skills_guard.py`).

### 3.4 Empirical lessons worth carrying forward

- Denying `read_file`/`search_files` to the review fork caused "~142 denials + ~204 read-before-write refusals over 2 days … starved the self-improvement loop" — the consolidator needs read access to the material it is consolidating.
- A skill-nudge review never gets the `memory` tool at all, because an unattended fork "cannot act on the memory tool's 'consolidate now' hint and delete entries no one reviewed."

---

## 4. What yaah can learn from hermes

Ranked by value:

1. **The trigger, not the storage.** yaah's memory is already *strictly better* — SQLite + FTS5 + vectors + cosine similarity (`internal/memory/vector.go`), vs. two flat char-budgeted files with zero embeddings. And yaah already has `skill` (`internal/tools/skill.go`) and `memory_search_sessions` (cross-session recall). What yaah lacks is the **scheduled, counter-gated capture step**. The loop is the trigger.
2. **Procedural memory as a curated lifecycle.** yaah's `skill` tool can create/edit but has no provenance, no usage tracking, no `reuse_after_patch` quality signal, no archive-not-delete, no undo ledger.
3. **Guardrails for autonomous self-modification.** If yaah's agents write to shared memory/skills unattended, it needs: a restricted write path (consolidator touches only memory/skills, not arbitrary files), a recursion guard, schema validation, a security scan, and an append-only rollback log.
4. **A `MEMORY` vs `USER` split.** yaah approximates this with tags (`user_info`, `preferences`); the conceptual separation is worth making explicit.

---

## 5. Direction 6 — the proactive consolidation trigger ("the nudge")

Synthesizing §3 into a sixth direction: add a **counter-gated consolidation stage** to the pipeline, so durable capture is *scheduled* rather than *accidental*.

- Two counters: user turns and tool iterations.
- Config-driven thresholds (yaah's existing `agents.middleware` config is the natural home).
- On threshold, run a **bounded consolidation pass** with a restricted toolset and a recursion guard.
- Product of the pass: proposed memory entries + proposed skill create/patch — **not** a prose summary (that's direction 3/4's job).

This direction is what makes directions 3 (compaction-as-consolidation) and 4 (multi-level memory) actually *fire*. Those directions describe *what* to write; the nudge describes *when*.

---

## 6. Feasibility — yaah is the better substrate

Hermes implements the loop with a raw `threading.Thread` fork and direct file writes. yaah's architecture upgrades each mechanism:

| Hermes mechanism | yaah realization | Why yaah's is better |
|---|---|---|
| `turn_finalizer` nudge hook | new **pipeline middleware stage** (`consolidate`) | same seam — `CompactionMiddleware` already exposes `PrepareStep`/`PostModel`/`PostTool` |
| `_spawn_background_review` (thread fork) | **sub-agent dispatch** ("kid" with a consolidation role + restricted toolset) | real isolation (worktree), a role's own tool budget, supervision (`inject`/`halt`) for free; recursion guard = the role simply has no consolidation trigger |
| direct file writes + security scan + ledger | **sealed proposal → `apply_out`** | hermes *writes files directly*; yaah can produce a *proposed* `MEMORY`/`SKILL` delta the orchestrator/user reviews **before it lands** — a diff on shared state hermes can't do |
| `.curator_ledger.jsonl` + `.usage.json` | `skill_usage`/`skill_provenance` table + mutation ledger in the existing SQLite store | same data, already-backed-up store |

**Conclusion:** the hermes loop is a *trigger* + a *lifecycle* + a *rail*. yaah already has superior *storage* (SQLite+vectors), superior *orchestration* (sub-agents), and a superior *write boundary* (sealed proposals). Adopting the loop is mostly adding the trigger and the lifecycle — not rebuilding storage.

**The one friction point:** yaah's isolated mode sets the `skill` tool **`ReadOnly`** — skills/plans are host harness config, and an isolated container must not write host files around the `apply_out` boundary. Autonomous consolidation writes *to shared host state*, so its output must route through the **same `apply_out`/host-write seam** as any other change (the consolidator emits a proposed file delta, applied only on accept). That is consistent with yaah's sealed-proposal philosophy and reinforces "consolidation = a sub-agent producing a proposal."

---

## 7. Recommended architecture (sketch)

Two orthogonal triggers, one action. The middleware contract (`PrepareStep` /
`PostModel` / `PostTool` over a mutable `*Step`) is *per-iteration and synchronous*
— it transforms the in-flight turn. So each concern is placed by its *shape*:

```
pipeline: steer → followup → compaction → soft_prune → approval → inline_limit
          → tool_concurrency → loop_detection → evidence_guard → conflict_detect → consolidate

compaction  (enriched — "sleep check", PRESSURE-driven)
  PrepareStep / PostTool:
    if adaptive_threshold(ineffective_compactions) and at_safe_boundary:
        step.Messages = compactor.Compact(...)      # pure predicate + Compactor

consolidate (NEW — "nudge", CADENCE-driven)
  PostTool:
    turnsSince++; itersSince += len(results)
    if turnsSince >= turnsInterval or itersSince >= itersInterval:
        reset counters
        ConsolidateDispatch(ctx, "memory"|"skills") # injected callback, fire-and-forget
  return step, nil   # loop continues; the sealed proposal arrives later
```

**The three concerns, by shape:**

| Concern | Lives | Shape |
|---|---|---|
| **Sleep check** (pressure → compact now) | enriched `CompactionMiddleware` | pure predicate + `Compactor` (`Compact(ctx, msgs, thresh) []Message`) |
| **Nudge trigger** (cadence → consolidate every N) | new `ConsolidateMiddleware` | struct counters + hook check + injected `ConsolidateDispatch` callback |
| **Consolidation action** (write memory/skills) | `consolidator` sub-agent, sealed proposal → `apply_out` | *not* middleware — scheduled by the pipeline, run out-of-band |

The injected-callback pattern is already idiomatic here: `ConsolidateDispatch`
sits in `PipelineConfig` exactly like `ConflictPersist` and `SteerDrain`, wired by
the composition site (`buildPipeline`). The middleware only *enqueues*; it never
runs the consolidation inline (which would block the loop and break the
`*Step`-transform contract) and never writes durable state from a hook.

```
consolidator sub-agent (role: "consolidator", restricted tools, recursion guard):
  dispatched by ConsolidateDispatch → runs in isolated worktree
  → emits SEALED PROPOSAL:
      · memory_add/update entries (into memory store)
      · skill create/patch (SKILL.md delta)
  → orchestrator/user reviews the delta → apply_out (host write)
```

**New/repurposed pieces:**
- `consolidate` middleware (sibling of `compaction.go`, same `Step` interface; holds session-scoped counters).
- `ConsolidateDispatch func(ctx context.Context, kind string)` callback in `PipelineConfig`.
- A `consolidator` role file (`.agents/roles/consolidator.md`) with tool restriction + recursion guard.
- `skill_usage` / `skill_provenance` + a mutation ledger in `internal/memory` (SQLite).
- Guardrails ported from hermes: frontmatter validation, description-length limit, security scan, archive-not-delete, provenance stamp.

**Two caveats this split encodes:**
1. Middleware counters are **session-scoped** (struct fields), not global. Cross-session cadence requires persisting the counter in the memory store (hermes rehydrates its nudge counter from history).
2. Consolidation output is never written synchronously in a hook — it is a *proposal* crossing `apply_out`, which is what keeps isolated-mode's `ReadOnly` skills boundary intact.

---

## 8. Phased plan

1. **P0 — Trigger.** ✅ Counter-gated `consolidate` middleware that fires a bounded pass and *writes memory entries* (no skills yet). Proves the "nudge" in yaah's pipeline.
2. **P1 — Lifecycle.** ✅ Add provenance + usage + archive-not-delete + ledger to `skill`, and let the consolidator propose skill create/patch through the sealed-proposal path.
3. **P2 — Guardrails.** ✅ Schema validation, description limits, security scan, recursion guard — ported from hermes's safety stack.
4. **P3 — Multi-level memory + light/deep sleep.** ✅ Tie the consolidation product into directions 4 and 5 (short/medium/long tiers; cheap light sleep vs. expensive deep sleep).
5. **P4 — Introspection.** ✅ A `memory stats` / journey-style view (`yaah memory stats`; hermes's "learning graph" is a render, not a store).
6. **Hygiene.** ✅ A conservative staleness sweep reclaims compaction atoms that were never recalled; accessed atoms are never pruned.

---

## 9. Risks & open questions

- **Autonomous self-modification is the risk hermes is engineered around.** yaah must adopt the guardrails *before* enabling unattended writes — not after.
- **ReadOnly-skills friction in isolated mode** needs a clear answer: consolidation output must be a *proposal* crossing the `apply_out` boundary, never a direct in-container write.
- **Cadence default.** 10 turns / 10 iterations is hermes's heuristic; yaah should surface it as policy (`agents.middleware.consolidate`) and let the `IneffectiveCompactions` signal tune it (ties to direction 1).
- **Consolidator cost.** A full background fork per N turns is non-trivial token spend; the light-vs-deep staging (direction 5) is the mitigation.
- **Idempotency.** Duplicate memory/skill writes across overlapping consolidations must be deduped (hermes's ledger + `reuse_after_patch` are the pattern to follow).

---

## 10. Cross-references

- `yaah-8uz`, `yaah-e4b`, `yaah-cgq`, `yaah-car` (original spike cross-refs)
- `yaah-4il` (output-budget → fallback rotation), `yaah-9em` (surface provider/model to sub-agents)
- Hermes source anchors: `agent/turn_finalizer.py`, `agent/background_review.py`, `agent/learn_prompt.py`, `tools/skill_manager_tool.py`, `tools/memory_tool_store.py`, `tools/skill_usage.py`, `tools/skill_ledger.py`
- yaah source anchors: `internal/agent/pipeline/compaction.go`, `internal/agent/pipeline/config.go`, `internal/agent/context_manager.go`, `internal/agent/loop.go`, `internal/memory/vector.go`, `internal/tools/skill.go`, `internal/tools/memory.go`
