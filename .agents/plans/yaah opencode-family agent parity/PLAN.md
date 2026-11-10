---
name: yaah opencode-family agent parity
description: Adopt opencode-family agent semantics (file-authority routing, prompt-replace, mode default all, permission rulesets, watcher reload) on top of the decoupled agent/mode/approval session axes from ../decouple-persona-mode-approval/PLAN.md; greenfield replacement, no compat paths. Complementary pair: decouple plan owns the W0 axis-restructure scope, this plan owns sequencing + parity workstreams W1-W7.
status: approved
---

# Goal

Adopt opencode-family agent semantics in yaah, built on the three orthogonal session axes (agent / mode / approval) from `../decouple-persona-mode-approval/PLAN.md`. Greenfield: replace behavior outright — no compat flags, no migration paths, no deprecation shims. Pinned tests flip from pinning old behavior to pinning new behavior.

# Source plans

- **W0** absorbs `../decouple-persona-mode-approval/PLAN.md` — sibling in this plans dir (all 5 phases, decisions D1–D3 as recommended there: "Read-only"/"Full" naming, `read_only` removed from personas entirely, approval axis included). The two plans are complementary: the decouple plan owns the axis-restructure scope and its Phase 1–5 *are* W0; this plan owns sequencing, milestones, and the parity workstreams W1–W7. The decouple plan says the same thing in the other direction.
- W1–W7 are the opencode-family parity work; each notes where it interlocks with W0.

# Non-goals (deliberate divergence, keep)

- Lanes/contracts/settlement (retained-proposal, report, shared) — yaah is ahead of the family here.
- Marketplace / org-managed cloud agents / Kilo Swarm / Agent Manager panel — SaaS product surface, not semantics.
- Shrink-only belt (`tools:` ∩ `agents.default.tools`) stays as the *ceiling*; permission rulesets refine within it, never widen.
- Session keeps its persona for life (no watcher-triggered persona re-resolution mid-session); reload refreshes the registry only.
- Per-origin `MCPApproval` policy stays config-only (W0 non-goal, unchanged).
- Mode-axis floor stays unbypassable — yaah's version of Kilo's "guarded ceilings"; in read-only mode, rulesets refine within the inspection set only.

# Terminology guard (naming collision, watch in review)

Two different "modes": the **role-file `mode:` key** (primary/subagent/all — W3) and the **session mode axis** (read-only/full, wire `build`/`plan` — W0). They are unrelated. Docs must never use one to mean the other.

# Workstreams

## W0 — Axis decoupling (absorbs decouple-persona-mode-approval) — M, ~1 wk

Lands FIRST: it restructures the seams everything else builds on. Scope and phase detail live in the decouple plan; summary:

1. **Phase 1 — session axes**: add `readOnly bool` + `approvalMode` session fields (defaults: new `agents.default.mode` config, existing approval chain); `Mode()` derives from `readOnly` not `PersonaName() == "plan"`; `personaLevel` reads the session field; loop builder takes `ApprovalMode` from the session (`build_loop.go:37`, `serve.go:289`).
2. **Phase 2 — persona schema**: remove `ReadOnly` from `rolefile.Frontmatter` and `subagent.Persona`; drop `read_only: true` from shipped `plan` persona and `architect.md` (architect stays structurally read-only via `tools:`); update `role_def.go` warnings.
3. **Phase 3 — ACP configOptions**: three builders (agent / mode / approval) composed with model in `buildSessionConfigOptions`; new `ConfigOptionAgentID`/`ConfigOptionApprovalID` extensions; `handleSetConfigOption` gains `agent` and `approval` cases; keep `set_agent`/`set_mode` shortcuts. This seam is also what W4 rides — do not invent a second one.
4. **Phase 4 — directive + docs**: `regimeSentence` decoupled from the word "plan" ("read-only mode — inspect and report, do not modify" vs "full access"); docs/agents.md, acp.md, configuration.md, prompts.md. **This phase OWNS the features.md mode-axis rewrite and the `internal/parity/doc_inventory_test.go` lockstep — it happens here, at M0, once.** The pinned section describes exactly what Phases 1–2 change; leaving it stale after M0 would keep CI green while docs lie. W7 must not re-touch the pinned section (see W7).
5. **Phase 5 — tests + gates**: mode_test, persona_test, persona_belt_test, rolefile, internals_test, server_test, session_lifecycle_test; gofmt/vet/staticcheck/go test ./...

## W1 — File-authority routing (un-park bead yaah-21uk.15) — M, ~1–1.5 wk

1. `role_def.go roleDefFrom`: consume `fm.Provider`/`fm.Model` for primary-capable roles instead of warning; persona carries a routing selection. **Interlock W0**: the warning path being touched is the same one Phase 2 edits (it drops `read_only` references) — land W1 after W0 Phase 2.
2. `wiring_stages.go resolveProvider(cfg)` → `resolveProvider(cfg, persona)`: persona routing overrides `agents.default` when set; chain = persona file > config default.
3. Delegate per-call override: `model`/`provider` params on the delegate schema + runner; dispatch chain = call > role file > **parent session model** (inheritance) > config default.
4. `temperature`/`top_p` rolefile keys → provider request options passthrough (internal API break fine).
5. Flip `TestPersonaProviderModelWarnedAndIgnored` → `TestPersonaProviderModelHonored` (persona_budget_test.go).
6. Resume: stored persona re-derives routing; the model pick itself persists via W4.

## W2 — Prompt-replace semantics — M, ~1–1.5 wk

1. New frontmatter key `system: replace`: persona body replaces the *identity layer* only; Environment / project-instructions / Memory / skills / session directives / mode directive still layer after (same shape as opencode appending session instructions after a replaced system prompt). **Interlock W0**: the mode directive's regime sentence is W0 Phase 4's new wording — W2 tests must not pin the old sentence.
2. `prompts.Build` surgery: identity layer becomes persona-supplied under replace. Known trap: `InjectAfterIdentity` anchors on the exact embedded identity substring — directives + mode directive must still land after a replaced identity (ordered-layer or synthetic anchor; never silently demote to prompt end).
3. `SetPersona`/`rebuildPromptsLocked`: replace-mode body must be a retained construction Layer so byte-stable rebuild and resume keep working.
4. Shipped `build`/`plan` personas unchanged (frontmatter-only, embedded identity).
5. docs/agents.md persona-effects table gains the replace row (fold into W7's single docs pass).

## W3 — Role-file `mode:` default `all` — S, ~1–2 days

1. `normalizeRoleMode`: missing `mode:` → `all`; unknown value → warn + `all` (family symmetry). **Interlock W0**: Phase 2 touches the same function's neighborhood — small, land right after W0.
2. Sweep `.agents/roles/*` + docs fixtures: delegate-only files add explicit `mode: subagent`.
3. `DelegatableRoles` filter stays `mode != primary` — primary-only personas remain never-delegatable.
4. docs/agents.md mode-key table update (W7 pass).

## W4 — Per-session model persistence — S/M, ~2–4 days

1. Persist the session's resolved model alongside `sessions.persona`; resume restores it.
2. Surface via the configOptions composition from W0 Phase 3 (`buildSessionConfigOptions` composes agent + mode + approval + **model**, `currentValue` from session fields) — the seam W0 builds is exactly what W4 needs.
3. Web `ctrl.agents` frame carries current model; palette/`/agent` display and change it. Mirrors opencode's per-(session,agent) model picks.

## W5 — Permission rulesets (full parity) — L, ~2–4 wk, the long pole — **rebuilt on the W0 axis model**

1. Frontmatter `permissions:` — ordered per-tool rules: `effect: allow|deny|ask` + glob `resource`; last-match-wins; `*` resource matches anything; empty resource = tool-name-only match.
   ```yaml
   permissions:
     edit: { "*.md": allow, "*": deny }
     bash: { "git push*": ask, "git status*": allow }
     webfetch: deny
   ```
2. Matching domains: `bash` → command string (shell-operator deny-guards stay), mutating file tools → workspace-relative path globs, everything else → tool name.
3. Engine: extend `allowsTool` into a resolver ordered **mode-axis level (session `readOnly` from W0) → persona belt narrowing → permission ruleset (last-match-wins) → per-call dangerousGate**. In read-only mode, an `allow` rule can never re-enable a withheld tool (floor, per non-goals). W0's decoupling is what makes this chain clean — the level comes from the session axis, not from persona identity.
4. Global config `permissions:` applies to every agent; persona rules append after (persona wins on overlap) — opencode semantics.
5. **`ask` composition rule**: ruleset verdict `ask` routes through the approval pipeline the W0 approval axis governs; the approval axis (`allow`/`ask`/`deny`) is the default posture for calls with no matching rule; ruleset verdict wins over the axis default; `dangerousGate` floors everything. W0 Phase 3's `approval` configOption becomes the mid-session knob for the default posture.
6. `ask` round-trips in REPL, TUI, web, ACP — the long pole; prototype the approval-middleware hook first.
7. Delegate inheritance: deny rules on mutating tools propagate as ceilings to sub-agent dispatches (multi-hop; inherit edit denies, not broad bash denies — Kilo #11523 lesson).
8. Interplay: `tools:` stays the advertised belt; `permissions:` refines within. Document both; no folding yet.

## W6 — Watcher hot reload — S/M, ~2–3 days

1. fsnotify (or cheap polling) on role search paths; debounce ~100 ms; rebuild via existing `ReloadDefaultRoles` atomic swap.
2. Role-tool mutations keep their reload path; watcher closes the known "raw file edits don't refresh the registry" gap.
3. Registry refresh only — session persona/axes still fixed per session (non-goal above).
4. Check dependency policy for fsnotify; polling fallback acceptable.

## W7 — Scaffolding + coordinated docs pass — S, ~2–3 days

1. `role` tool create: schema-complete defaults (contract, sane budgets matching golang-developer.md shape) instead of zeroed budgets.
2. Optional stretch: LLM-generated role scaffolding (Kilo `agent create` parity) via a dispatch.
3. **Single additive docs pass**: agents.md authority table (W1, file wins) + replace row (W2) + mode-key default (W3) + ruleset reference (W5) + acp.md/config docs for W4's model option. **Does NOT touch the features.md pinned section or the parity test — those were rewritten once in W0 Phase 4 and stay owned by it.** New sections only, so the parity lockstep never reopens.

# Sequencing & milestones

- **M0 — axes** (~1 wk): W0 complete (all phases incl. its test/gate pass and the features.md/parity lockstep). Everything else depends on its seams.
- **M1 — semantics parity** (~2.5–3.5 wk after M0): W3 (tiny, immediately after W0) → W1 → W2 → W4.
- **M2 — full parity** (+3–5 wk): W5 (long pole, self-contained behind the allowsTool resolver — delegable) in parallel with W6 + W7. W7's docs are additive-only by then (see W7.3).
- Critical path: W0 → W1 → W5. W2/W3/W4/W6/W7 float.

# Risks & pinned constraints

- **Parity test fragility**: features.md "## Plan mode" section, the "narrows the toolset to the **inspection set**" marker, the inspection-set inventory, and the forbidden phrase "read-only tools". Owned by W0 Phase 4, rewritten once at M0 in doc+test lockstep; W7 is additive-only and must not reopen it.
- InjectAfterIdentity anchor trap (W2) — known failure mode is silent demotion of directives to prompt end; add a construction test that fails if the anchor misses under replace.
- Resume byte-stability: replace-mode bodies must reproduce byte-for-byte from retained layers.
- `ask` round-trip across 4 surfaces is W5's schedule risk — prototype the approval hook before building matching engines.
- W0's `read_only` removal changes `rolefile.Marshal` output (bools emit unconditionally) — pin tests on parsed values, not bytes.
- Provider options passthrough (W1.4) touches the provider interface — internal break, fine on greenfield.
- Two-"mode" naming collision (role-file key vs session axis) — terminology guard above; reviewers watch for it.
