---
name: plan-build-mode-clarity
description: Make plan/build mode switching legible to the model: in-band switch notices, symmetric mode directive, single source of truth for tools+prompt, persistence, switch_mode dead path, docs/tests. Tracker: epic yaah-t3fh (children yaah-t3fh.1–6).
status: draft
---

# Plan/Build mode clarity — implementation plan

> Tracker: epic **yaah-t3fh** (children yaah-t3fh.1–.6). All code refs are
> `origin/main`; the current working tree (`feat/oneshot-memory-recall`)
> predates PR #710's mode feature — branch this work off `main`.

## Findings — how mode switching works today

- **State**: `agentSession.mode` ∈ {"", "build", "plan"} (empty = build),
  `SetMode` at `cmd/yaah/session.go:108`. Not persisted — resume silently
  starts in build (`session.go:117`, `docs/acp.md:122`).
- **Entry points**: TUI `/plan` `/build` (`internal/tui/commands.go:93`), REPL
  `/plan` `/build` (`cmd/yaah/repl_loop.go:125`), web palette
  (`cmd/yaah/web.go:284`), ACP `session/set_mode` + config-option picker
  (`internal/acp/server.go:159`, `internal/acp/prompt.go:70`).
- **Enforcement, three layers**:
  1. Tools: `toolsLevelForMode` → `agent.ReadOnlyTools`; `readOnlyToolNames`
     = read, grep, glob, ls, file_info, go_outline, calculate, diff, question
     (`internal/agent/tools.go:108-112`).
  2. Prompt: `systemPromptForMode` (`session.go:506-513`) injects one
     plan-mode-only directive via `prompts.InjectAfterIdentity`.
  3. Execution gate: `allowsTool` refuses with
     `tool %q is not available in plan mode` (`internal/agent/agent_dispatch.go`).
- **Run capture**: mode is sampled at run start (`cmd/yaah/build_loop.go:236`)
  — a mid-run switch is invisible until the next prompt.
- **Dead code**: `KindSwitchMode` (`internal/acp/protocol.go:480`) and
  `SwitchModeRequest` (`internal/acp/view.go:167`) are never constructed.

## Clarity problems being fixed (ranked)

1. **No in-band switch signal** — `SetMode` flips state silently; the model's
   only cue is a different system prompt next turn. (Observed live: the agent
   could not say what mode it was in or that it had changed.)
2. **Directive names no mechanism** — "Ask for approval before leaving plan
   mode" gives the model no tool, no vocabulary for who switches or how.
3. **Asymmetric prompts** — build mode has no counterpart text; removal of the
   plan directive is indistinguishable from any other prompt change.
4. **Prompt/toolset can disagree** — policy wording vs withheld tools are two
   sources of truth; a directive-only plan mode with a full toolset produced
   the agent misreporting its own capabilities.
5. **Naming collisions & gaps** — plan mode vs `plan` tool vs PLAN.md; plan
   mode withholds the `plan` tool; "read-only tools" is really a curated
   inspection set (git reads, staticcheck absent).
6. **Resume silently downgrades** plan → build.

## Work items (beads)

### yaah-t3fh.1 (P1) — In-band switch announcement
Add `ModeChangedEvent` to `internal/agent/events` (struct + `allEvents()` in
`exhaustive_test.go`, per the AGENTS.md event process), emit from `SetMode`
through the broker, and inject a one-line conversation-visible notice on every
switch (both directions) in all views (TUI/REPL/web/ACP). Notice states new
mode + effective regime. Acceptance: the marker is quotable by the model.

### yaah-t3fh.2 (P1) — Symmetric directive, single source of truth
Replace `systemPromptForMode` with `modeDirective(mode)` covering both modes:
(a) regime sentence generated from actual enforcement (tools withheld vs
policy-only), (b) how leaving works (only the user switches: `/build`, web
palette, ACP `session/set_mode`; the agent may request it via the `question`
tool or in its reply), (c) a mode-change notice will appear in-band. Introduce
`PlanModeSpec{Tools, Directive}` consumed by both `toolsLevelForMode` and the
directive builder so they cannot drift.

### yaah-t3fh.3 (P2, decision) — Plan-mode toolset scope
Decide: admit `plan` (writes only `.agents/plans`), read-only `git`, and
`staticcheck` into plan mode — or keep the strict 9 and fix naming in
`/plan` help + `docs/features.md:44`. Document the exact set either way;
resolve the plan-mode / plan-tool naming collision in wording.

### yaah-t3fh.4 (P2) — Persist mode or announce downgrade
Persist `mode` in session state (preferred) or inject "Resumed in build mode"
on load. Update `docs/acp.md:120-122` accordingly.

### yaah-t3fh.5 (P2) — `switch_mode` dead path
Per acp-spec `session-modes.mdx` (AskResponse confirms/denies a requested
switch): either wire `SwitchModeRequest` so the agent can request leaving plan
mode, or delete `KindSwitchMode`/`SwitchModeRequest` and document
`session/set_mode` as the only switch.

### yaah-t3fh.6 (P2) — Docs + tests
Update `docs/features.md`, `docs/acp.md`, TUI `/plan` `/build` help,
`internal/prompts` wording for the final vocabulary. Tests: doc-parity for the
directive text (doc_parity_test.go pattern), exhaustive-event consumer test
for `ModeChangedEvent`, unit tests for `modeDirective` symmetry (both modes,
regime sentence matches enforcement level).

## Sequencing

1. yaah-t3fh.2 (vocabulary + PlanModeSpec — everything else quotes it)
2. yaah-t3fh.1 (announcement uses the same vocabulary)
3. yaah-t3fh.3 (decision feeds the allowlist in the spec)
4. yaah-t3fh.4, yaah-t3fh.5 (independent, parallelizable)
5. yaah-t3fh.6 (docs/tests land with each item; this bead owns the sweep)

## Validation

`go test ./...`, `go vet ./...`, `gofmt -l .`, staticcheck via
`go run honnef.co/go/tools/cmd/staticcheck@latest` (PATH binary is stale vs
Go 1.26). New exhaustive-event test must fail red before `ModeChangedEvent`
consumers exist, then pass.

## Open decisions (blockers for yaah-t3fh.2/3)

1. Directive-only plan mode (full tools + policy text) — supported
   configuration, or strictly tools-withheld? Determines whether the regime
   sentence is generated from enforcement or states the layer explicitly.
2. Toolset admission (yaah-t3fh.3).
3. Persist vs announce-only (yaah-t3fh.4).

