# yaah Best-of-Breed Gap Backlog

*Derived from the cross-framework comparison (`../agentic-frameworks-comparison.md`,
2026-08-26). Each item names the source framework where the superior pattern
lives, the concrete yaah gap verified in code, and the done-criteria. Priority
ordering: P0 = correctness/efficiency wins that compound daily; P1 = strong
differentiators; P2 = breadth/parity.*

---

## P0 — Correctness & token efficiency

**G1. Graceful `finish_reason=length` degradation** *(from pi)*
- yaah: `llm/stream.go:221` discards the whole message and errors the turn →
  errorclassify → possible checkpoint rewind → re-send, which may re-truncate.
- pi: `failToolCallsFromTruncatedMessage` synthesizes per-tool-call error
  results ("re-issue with complete arguments") and **continues the loop** —
  zero wasted turns.
- Done: a length-truncated message yields tool-result errors in the same run;
  regression test via faux harness; no full-turn failure for this case.

**G2. Tool-schema-aware token estimation** *(from hermes)*
- yaah: `context/tokens.go` estimates messages+system only in some paths;
  `EstimatePayloadBytes` exists but the preflight path yaah's compaction
  triggers on don't include tool schemas, which hermes measured at "20–30K+
  tokens the old sys+msg estimate missed entirely" (75+ tool sessions).
- Done: `estimate_request_tokens_rough`-equivalent includes tool definitions
  in every compaction/prune trigger decision; faux bench `ctx-pressure-20k`
  row shows earlier (correct) trigger with a tool-heavy registry.

**G3. Prune at cache-invalidating boundaries only** *(from kilocode)*
- yaah: pruner fires on token thresholds; a prune that lands mid-cache-block
  invalidates the Anthropic prefix cache and can cost more than it saves.
- kilocode: `PruneReason: normal | post-compaction | payload-limit` —
  prunes only at boundaries where the cache is already invalidated.
- Done: pruner consults the prompt-caching middleware's breakpoint map;
  off-boundary prunes deferred; faux bench shows equal or lower effective
  cost on a cached long session.

**G4. Cross-provider token accounting parity**
- yaah: usage tracking exists (`addUsage`) but cache-read/cache-write
  discounting in efficiency metrics is Anthropic-only (`promptcaching.go`).
- Done: usage records cache reads/writes per provider where reported; bench
  rows report effective tokens, not raw.

## P1 — Loop & durability

**G5. Durable prompt admission** *(from opencode V2)*
- yaah: a crash between REPL accept and loop `Run` loses the admitted prompt.
- opencode: `SessionV2.prompt()` writes a durable `session_input` inbox row
  before scheduling execution; the runner promotes at safe boundaries.
- Done: one-shot and REPL paths journal the user prompt (WAL or SQLite) before
  dispatch; on restart `yaah` offers to resume un-admitted prompts. Sized
  carefully — this is the door to full event-sourcing later; keep it minimal.

**G6. Per-call semantic tool-output filtering (swe-pruner)** *(from kilocode)*
- kilocode: model passes `context_focus_question` on read/grep/bash; a small
  model skims output keeping relevant lines (arXiv 2601.16746), falling back
  to full output. MIN_LINES=50 / MIN_CHARS=2000 gates.
- Done: opt-in provider transform middleware; configurable pruner model;
  faux scenario asserts the filtered shape and fallback path.

**G7. Re-verify file state before reuse** *(from hermes)*
- hermes: tools re-check on-disk state before trusting cached/summarized file
  content post-compaction ("over-claim success while the file is actually
  unchanged" repair logic).
- yaah: staleness middleware exists — verify it covers the post-compaction
  reuse case; extend if not.
- Done: post-compaction, stale-content tools (`read` cache, go_outline) get
  mtime/size invalidation; regression test.

**G8. Terminal event guarantee for every admitted run** *(from crush)*
- crush: every admitted run — canceled before start, dropped from queue, or
  completed — publishes exactly one terminal `RunComplete` event so callers
  never hang.
- yaah: `publishDone` + broker `PublishMustDeliver` exist for the loop; verify
  equivalent guarantees hold for one-shot dispatch, background jobs
  (`wireBackgroundHooks`/`unwireBackgroundHooks` wiring), and ACP/web views.
- Done: audit + tests proving exactly-one terminal event per admitted prompt
  across all four frontends (REPL/TUI/web/ACP).

## P1 — Provider breadth

**G9. Provider adapters beyond OpenAI-compat + Anthropic-native**
- yaah: `providers.go` is OpenAI-compat-first (covers OpenAI, OpenRouter,
  Ollama, LM Studio, vLLM, Google-compat, Copilot) + native Anthropic stream.
- Crush (via fantasy): Bedrock, Vertex/Gemini native, Azure, Copilot,
  OpenRouter, Vercel, Hyper + OAuth device flows per provider.
- Done: at minimum Bedrock + Vertex native routes (SigV4/IAM signing); OAuth
  device flow generalized beyond current providers; model-info catalog keyed
  by provider (context windows, pricing) so G2/G4 work cross-provider.

## P1 — Tooling

**G10. LSP tool suite** *(from crush)*
- crush: definition, references, symbols, rename, replace-symbol,
  call-hierarchy, diagnostics as first-class tools backed by an LSP client
  manager with on-demand server startup.
- yaah: has deep Go-native tools (go_outline/go_refactor/go_test) but nothing
  for TS/Python/Rust. An LSP suite generalizes precision editing beyond Go.
- Done: `internal/lsp` client manager + tools for definition/references/
  rename/symbols/diagnostics; gopls first, then typescript-language-server,
  pyright; go tests green; faux scenario uses LSP rename in a multi-file edit.

**G11. Repository-map / code-graph tool** *(from kilocode LanceDB recall / AI-Assistant repo_map)*
- yaah: grep/glob/outline are text-level; no persistent code index.
- kilocode: indexing worker + LanceDB `recall` tool for semantic recall.
- Done: opt-in embedding index (SQLite + existing memory infra) + `recall`
  tool; incremental indexing on file change; respects .gitignore.

## P2 — Platform

**G12. Session export/portability** *(from kilocode `session-export`)*
- Done: `yaah session export/import` producing self-contained JSON (messages,
  tool calls, usage, traces refs); round-trip verified by golden test.

**G12b. Terminal UI parity sweep** *(self / TUI components doc)*
- Done: gap list vs crush/kilocode TUI capability inventory (image support,
  diff rendering quality, worktree switcher, session switcher); land top 3.

**G13. Multi-root / workspace awareness**
- yaah: single cwd assumption across tools (path_validator, instructions
    walker). VS Code/Agent-Manager-style multi-root and worktree isolation
    (kilocode Agent Manager) increasingly standard.
- Done: `--workspace` flag + workspace-scoped tools/instructions; worktree
    isolation mode for sub-agents (ties into G14).

**G13b. Loop detection hardening** *(from goose stop-hook cap)*
- goose: bounded override of infinitely-blocking lifecycle hooks.
- yaah: loopdetect middleware exists; add bounded-override for repeated
  identical *tool results* (not just calls) and repeated provider errors of
  the same class — hermes' empty-response loop is the cautionary tale.
- Done: synthetic-loop regression suite via faux harness (same-call/same-
  result/same-error × window); bounded override synthesizes guidance message.

**G14. Sub-agent worktree isolation** *(from kilocode Agent Manager)*
- yaah: sub-agents share the orchestrator's working tree; parallel write
  conflicts are handled reactively (conflictdetect middleware).
- kilocode: per-session git worktree isolation in Agent Manager.
- Done: `worktree: true` role option; `tools.SharedScopeManager` integrates
  worktree path per sub-agent; conflictdetect still backstops; bench scenario
  `b5-parallel` shows zero conflict events with isolation on.

## P2 — Ecosystem

**G15. Harbor adapter** *(enables the Tier-2 benchmark from the comparison)*
- Done: `evals/harbor/agent.py`-style adapter (modeled on goose's
  `GooseBinaryAgent`) that installs a pre-built yaah binary into the task
  container; one clean `terminal-bench-2` subset run documented in
  BENCHMARKS.md with cost/compute columns, matching goose's table format.

**G16. Skills standard alignment**
- yaah already ships `.agents/` skills; verify SKILL.md discovery matches the
  cross-tool convention (yaah/crush/opencode/kilo/hermes/deepagents all read
  it in 2026) — specifically frontmatter fields, progressive disclosure
  (load only name+description until invoked), and `~/.agents/` global dir.
- Done: compat test matrix vs 2 external harnesses' skill dirs.

## Housekeeping (non-graded)

- **H1.** `docs/architecture.md` sync with post-port reality (faux harness,
  bench runner) — keep the AGENTS.md tree accurate.
- **H2.** BENCHMARKS.md: add faux-row section per the port plan Phase 5.
- **H3.** Delete or graduate `coverage.out` from repo root (build artifact).

---

## Suggested sequencing

1. **Faux harness port** (plan: `faux-harness-port`) — unlocks cheap tests
   for everything below.
2. G1, G2, G3 (correctness/efficiency triad) — each lands with faux
   regression + bench delta proof.
3. G8, G13b (event/loop hardening) — cheap, high insurance value.
4. G5 (durable admission) — design-heavy; do after the loop is stable.
5. G10 (LSP) — biggest UX differentiator for non-Go users.
6. G6, G7, G14, G15 as capacity allows; G9 when a concrete need lands
   (Bedrock/Vertex users).
7. G11, G12, G13, G16 opportunistically.
