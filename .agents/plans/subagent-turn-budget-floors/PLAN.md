---
name: subagent-turn-budget-floors
description: Add per-role minimum turn/iteration floors so an orchestrator's per-call override cannot starve a sub-agent of its tool budget
status: draft
---

# Sub-agent turn budget floors (`min_turns` / `min_iterations`)

**Status:** draft
**Owner:** TBD
**Related:** `internal/agent/runner/runner.go`, `internal/agent/budget` (new), `internal/rolefile`, `internal/config`

## 1. Problem statement

Sub-agents frequently exhaust their turn budget mid-task and are forced
into a premature text-only response. The user-visible symptom is a
sub-agent that stops calling tools and reports partial work, or a
`MaxIterationsError` that triggers a `supervised_task` rollback/retry.

The orchestrating agent's per-call `max_turns` / `max_iterations`
arguments are **unconditionally authoritative in the downward
direction**. A role author can express a ceiling but has no way to
express a floor, so the orchestrator can silently starve any role.

## 2. Root cause analysis

### 2.1 The two budget dimensions

| Dimension | Config field | Loop behaviour when reached |
|---|---|---|
| Loop cycles | `max_iterations` → `LoopConfig.MaxLoopCycles` | Hard stop, returns `MaxIterationsError` |
| Tool turns | `max_turns` → `LoopConfig.MaxToolTurns` | **Soft**: `req.Tools = nil` — tools stripped, model forced to answer |

`internal/agent/turn.go` (`buildTurnRequest`):

```go
if l.Config.MaxToolTurns > 0 {
    effective := l.Config.MaxToolTurns
    if effective >= l.Config.MaxLoopCycles {
        effective = l.Config.MaxLoopCycles - 1
    }
    if iter >= effective {
        req.Tools = nil          // <-- "ran out of turns"
    } else if ... WrapUpThreshold ... {
        l.injectWrapUpNotice(...)
    }
}
```

So `max_turns` is the binding constraint in practice, and it is the one
with the weakest defaults and no floor.

### 2.2 The defect: per-call override has no floor

`internal/agent/runner/runner.go`:

```go
func resolveSubAgentTurns(callMax int, profile, subCfg, role, maxIter) int {
    var v int
    switch {
    case callMax > 0:
        v = callMax          // <-- NO clamp of any kind
    case subCfg.Roles[string(role)].MaxToolTurns > 0:
        v = subCfg.Roles[string(role)].MaxToolTurns
    case profile.MaxToolTurns > 0:
        v = profile.MaxToolTurns
    case subCfg.DefaultMaxToolTurns > 0:
        v = subCfg.DefaultMaxToolTurns
    default:
        v = 3                // <-- hardcoded, very low
    }
    if maxIter > 0 && v >= maxIter { v = maxIter - 1 }
    if v < 1 { v = 1 }
    return v
}
```

`resolveSubAgentIterations` is *asymmetric* — it clamps the per-call
value with the profile as a **ceiling only**:

```go
case callMax > 0:
    v = callMax
    if profile.MaxLoopCycles > 0 && v > profile.MaxLoopCycles {
        v = profile.MaxLoopCycles   // ceiling enforced, floor absent
    }
```

Net effect: `max_turns: 1` from the orchestrator gives *any* role exactly
one tool-using turn, and the role definition is powerless to object.

### 2.3 Aggravating factor: built-in role budgets are very low

`internal/prompts/roles/*.md`:

| Role | `max_iterations` | `max_turns` |
|---|---|---|
| analyst | 30 | 10 |
| developer | 40 | 6 |
| tester | 30 | 6 |
| reviewer | 25 | **3** |

A `reviewer` gets **3 tool-using turns** even with no override at all.
The orchestrator system prompt, meanwhile, says *"Reviewers have limited
iteration budgets (typically 25-50)"* — describing `max_iterations`, the
dimension that is **not** binding. The guidance actively misleads.

### 2.4 Aggravating factor: `(unset)` silently means 3

Project roles in `.agents/roles/`:

| Role | `max_iterations` | `max_turns` |
|---|---|---|
| checker | (unset) | 1 |
| counter | (unset) | 1 |
| goat-joke-teller | 0 | 0 |
| golang-developer | 50 | 8 |
| golang-tester | (unset) | 8 |
| grump | (unset) | 2 |
| security_auditor | 30 | **(unset) → 3** |

`security_auditor` and `goat-joke-teller` (explicit `0`) both fall
through to the hardcoded `v = 3`. A security audit with three tool
turns cannot read a codebase.

Note also that `NewSubAgentLoop` has a *generous* fallback
(`if cfg.MaxToolTurns <= 0 { cfg.MaxToolTurns = cfg.MaxLoopCycles }`)
which is **dead code** on the dispatch path, because
`resolveSubAgentTurns` never returns 0.

### 2.5 Diagnosability gap

Nothing records *which* precedence branch won. The user had to infer
"it seems the orchestrating agent sets the max turns" by reading
behaviour. There is no span attribute, no log line, and no field in the
sub-agent result reporting the effective budget or its source.

## 3. Goals / non-goals

**Goals**

- G1. A role can declare `min_turns` / `min_iterations` that a per-call
  override cannot go below.
- G2. Operators can set the same floors in `config.yaml`, per role and
  globally.
- G3. `(unset)` / `0` stops meaning "3". Unset resolves to a sane,
  role-derived budget.
- G4. The effective budget and the reason for it are observable in
  traces and in the sub-agent result.
- G5. Budget resolution becomes a pure, unit-tested, single-responsibility
  unit rather than three ad-hoc functions inside `runner.go`.

**Non-goals**

- Dynamic//adaptive budgets that grow at runtime based on progress.
- Removing per-call overrides (they remain useful for *raising* budgets
  and for deliberately cheap probes).
- Changing the soft-strip mechanism in `turn.go`.

## 4. Design

### 4.1 Extract a `budget` leaf package (SOLID: SRP + OCP)

`runner.go` currently mixes provider resolution, registry construction,
prompt assembly, and budget arithmetic. Budget resolution is pure
arithmetic over configuration and deserves its own leaf package with no
yaah imports beyond types it defines itself.

New package `internal/agent/budget`:

```go
package budget

// Spec is the complete, explicit input to budget resolution. Every
// source of truth is a named field, so precedence is visible at the
// call site and testable without constructing a runner.
type Spec struct {
    // Per-call override from the orchestrator's tool call. 0 = unset.
    CallIterations int
    CallTurns      int

    // Role file frontmatter (internal/prompts/roles, .agents/roles).
    RoleMaxIterations int
    RoleMinIterations int
    RoleMaxTurns      int
    RoleMinTurns      int

    // config.yaml agents.subagent.roles.<role>.*
    CfgMaxIterations int
    CfgMinIterations int
    CfgMaxTurns      int
    CfgMinTurns      int

    // config.yaml agents.subagent.* global defaults.
    DefaultTurns    int
    DefaultMinTurns int

    // HardCeiling bounds the final iteration count (schema max, 50).
    HardCeiling int
}

// Source identifies which precedence branch supplied a value, for
// tracing and error messages.
type Source string

const (
    SourceCall       Source = "call"
    SourceRoleConfig Source = "role_config"
    SourceRoleFile   Source = "role_file"
    SourceDefault    Source = "config_default"
    SourceFallback   Source = "builtin_fallback"
    SourceFloor      Source = "floor"      // a min_* raised the value
    SourceCeiling    Source = "ceiling"    // a max_* lowered the value
    SourceHeadroom   Source = "headroom"   // reconciliation grew it
)

type Budget struct {
    Iterations       int
    Turns            int
    IterationsSource Source
    TurnsSource      Source
}

// Resolve is pure: no I/O, no globals, no clock.
func Resolve(s Spec) Budget
```

### 4.2 Resolution algorithm

```
1. iterations := pick(CallIterations, CfgMaxIterations, RoleMaxIterations, fallback 25)
   - if source == call and RoleMaxIterations > 0: clamp DOWN to it   (existing ceiling)
2. iterFloor := first non-zero of (CfgMinIterations, RoleMinIterations)
   - if iterations < iterFloor: iterations = iterFloor; source = SourceFloor
3. turns := pick(CallTurns, CfgMaxTurns, RoleMaxTurns, DefaultTurns, fallback)
4. turnFloor := first non-zero of (CfgMinTurns, RoleMinTurns, DefaultMinTurns)
   - if turns < turnFloor: turns = turnFloor; source = SourceFloor
5. RECONCILE (see 4.3): if turns >= iterations:
       iterations = turns + 1; IterationsSource = SourceHeadroom
6. if HardCeiling > 0 && iterations > HardCeiling:
       iterations = HardCeiling; turns = min(turns, iterations-1)
7. floor of 1 on both.
```

### 4.3 Key decision: floors grow, they never shrink the other dimension

Today, `if maxIter > 0 && v >= maxIter { v = maxIter - 1 }` lets a low
`max_iterations` silently shrink the tool budget. If a role declares
`min_turns: 8` and something sets `max_iterations: 5`, the *floor must
win* and iterations must grow to 9 rather than turns being cut to 4 —
otherwise the floor is not a floor and we have reproduced the original
bug one level down.

Reconciliation therefore grows `iterations`, bounded only by
`HardCeiling`. If `HardCeiling` itself makes the floor unsatisfiable,
that is a **configuration error** surfaced at validation time (§4.6),
not silently clamped at dispatch time.

### 4.4 Fix the `(unset) → 3` fallback

Replace the bare `v = 3` fallback. When no `max_turns` is expressed
anywhere, derive it from the iteration budget rather than a magic
constant, matching the intent already encoded (and stranded) in
`NewSubAgentLoop`:

```go
// Unset max_turns means "use essentially the whole loop budget",
// leaving one iteration of headroom for the forced-text turn.
turns = iterations - 1
```

`goat-joke-teller` (explicit `max_turns: 0`) and `security_auditor`
(omitted) both then get a usable budget. This is a **behaviour change**
and needs a changelog note; roles that genuinely want a tiny budget
(`checker: 1`, `counter: 1`) already state it explicitly and are
unaffected.

### 4.5 Schema and frontmatter surface

`internal/rolefile/rolefile.go` — `Frontmatter` (the package doc warns
that any field missing here is *silently dropped on rewrite*, so both
new fields must be added here or the `role` tool will eat them):

```go
MaxLoopCycles int `yaml:"max_iterations"`
MinLoopCycles int `yaml:"min_iterations"`   // NEW
MaxToolTurns  int `yaml:"max_turns"`
MinToolTurns  int `yaml:"min_turns"`        // NEW
```

Then mirror through the two hops to the consumer:

1. `internal/agent/subagent/role_def.go` — `RoleDef` ← frontmatter
2. `internal/agent/subagent/role.go` — `RoleProfile` ← `RoleDef`

`internal/config/load.go` — `RoleConfig`:

```go
MinLoopCycles int `yaml:"min_iterations"` // 0 = no floor
MinToolTurns  int `yaml:"min_turns"`      // 0 = no floor
```

and `SubAgentConfig`:

```go
DefaultMinToolTurns int `yaml:"default_min_turns"` // global floor; 0 = none
```

### 4.6 Validation

`internal/config/validate.go`, following the existing
`errs = append(errs, ...)` accumulation style:

- reject negative `min_turns` / `min_iterations`
- reject `min_turns > max_turns` within the same role config
- reject `min_iterations > max_iterations` within the same role config
- reject `min_turns >= 50` (hard ceiling) — unsatisfiable floor

Role *files* are validated at registry load; a bad floor should log a
warning and clamp rather than kill startup, consistent with how role
files are treated elsewhere (a broken project role must not brick the
CLI).

### 4.7 Observability (addresses G4 / §2.5)

- Sub-agent span attributes:
  `subagent.budget.turns`, `subagent.budget.turns_source`,
  `subagent.budget.iterations`, `subagent.budget.iterations_source`.
  Set next to the existing `subagent.directives` attribute in
  `makeTaskRunner`.
- When the soft cap strips tools, `turn.go` already emits
  `maxturns.stripped`; add the resolved source to that event so a trace
  answers "who set this to 3?" directly.
- Enrich `MaxIterationsError` with the effective budget and its source,
  so `supervised_task` retry guidance says *"exhausted 5 iterations
  (source: call override)"* instead of a bare count.
- `list_subagents` output: show effective `min`/`max` per role so the
  orchestrator can see real budgets instead of guessing.

### 4.8 Orchestrator-facing wording (§2.3)

- `internal/tools/task_schema.go`: change `max_turns` description from
  *"Optional soft cap on tool-using turns. Overrides the role default."*
  to note it is clamped to the role's `min_turns` and that **omitting it
  is preferred** — the role knows its own budget.
- Same for `max_iterations`, and for `BuildSupervisedTaskSchema` /
  the fallback schema literal in `supervised_task.go`.
- `internal/prompts/`: fix the misleading *"limited iteration budgets
  (typically 25-50)"* guidance and replace it with "do not set
  `max_turns`/`max_iterations` unless you intend a deliberately cheap
  probe; roles carry tuned budgets."

## 5. Alternatives considered

| Option | Verdict |
|---|---|
| **A. Ignore per-call overrides entirely** (role file always authoritative) | Simplest and fixes the reported symptom, but removes a legitimately useful escape hatch (cheap probes, deliberate exhaustion tests — the `supervised_task` rollback demo depends on being able to force exhaustion with `max_iterations: 1`). Rejected. |
| **B. Treat per-call value as a *request* the role may veto** (this plan) | Chosen. Preserves the escape hatch above the floor, restores role authority below it. |
| **C. Only raise the built-in role `max_turns` values** | Treats the symptom (§2.3) and not the cause (§2.2). Worth doing anyway as Phase 5, but insufficient alone. |
| **D. Make `max_turns` a hard stop like `max_iterations`** | Larger blast radius, loses the graceful "answer with what you have" behaviour. Out of scope. |

## 6. Implementation phases

Each phase is independently shippable and independently green.

### Phase 0 — Baseline & tracking
- File `bd` issues for each phase; one epic parent.
- Add a **characterization test** capturing today's behaviour of
  `resolveSubAgentTurns` / `resolveSubAgentIterations`, including the
  bug (`callMax: 1` beats `profile.MaxToolTurns: 40`). This test is
  edited deliberately in Phase 2 — the diff is the proof of the fix.

### Phase 1 — Extract `internal/agent/budget` (pure refactor, no behaviour change)
- Create the package with `Spec`, `Source`, `Budget`, `Resolve`.
- Port the existing precedence exactly, including current quirks.
- Rewrite `resolveSubAgentIterations` / `resolveSubAgentTurns` as thin
  adapters that build a `Spec` and call `Resolve`.
- Port the characterization test onto `budget.Resolve`.
- **Gate:** `go test ./...` green with zero behaviour change.

### Phase 2 — Add the floors
- Add `min_turns` / `min_iterations` to `rolefile.Frontmatter`,
  `RoleDef`, `RoleProfile`, `config.RoleConfig`,
  `SubAgentConfig.DefaultMinToolTurns`.
- Populate `Spec` floor fields in the adapter.
- Implement floor application + reconciliation (§4.2, §4.3).
- Update the Phase 0 characterization test to the new expectations.
- **Gate:** table-driven tests for the full precedence matrix.

### Phase 3 — Fallback fix + validation
- Replace `v = 3` with `iterations - 1` (§4.4).
- Add config validation (§4.6) + role-file load-time clamp/warn.
- Add a `rolefile` round-trip test asserting the two new fields survive
  `Parse` → `Marshal` (guards the documented silent-drop hazard).

### Phase 4 — Observability
- Span attributes + `maxturns.stripped` enrichment (§4.7).
- `MaxIterationsError` carries budget + source.
- `list_subagents` shows effective budgets.

### Phase 5 — Retune the shipped roles
- `internal/prompts/roles/*.md`: raise `reviewer` `max_turns` from 3;
  add explicit `min_turns` to all four built-ins.
- `.agents/roles/*.md`: add `min_turns`; give `security_auditor` and
  `goat-joke-teller` real budgets.
- Fix schema + prompt wording (§4.8).

### Phase 6 — Docs & gates
- `docs/configuration.md`: new config keys, precedence table.
- `docs/sub-agents.md`: budget model, floors vs ceilings, guidance on
  when the orchestrator should override.
- `AGENTS.md`: note the new `budget` leaf package in the repo layout.
  *(Separately: the layout table still lists the deleted
  `cmd/yaah/subagent_runner.go` — fix while in there.)*
- **Gates:** `go test ./...`, `go vet ./...`, `gofmt -l .` empty,
  `staticcheck ./...` clean.

## 7. Test plan

**Unit — `internal/agent/budget` (table-driven, the bulk of the value)**

| Case | Expectation |
|---|---|
| No inputs at all | fallback iterations, `turns == iterations-1` |
| Role max only | role values, source `role_file` |
| Call override *above* role max iterations | clamped to role max (ceiling preserved) |
| Call `max_turns: 1`, role `min_turns: 8` | **turns == 8**, source `floor` |
| Call `max_iterations: 3`, role `min_turns: 8` | turns 8, **iterations 9**, source `headroom` |
| Config role floor vs role-file floor | config wins |
| `DefaultMinTurns` with no role floor | applies |
| Floor > `HardCeiling` | clamped at ceiling, `turns == ceiling-1` |
| `max_turns: 0` explicit (goat-joke-teller) | treated as unset, not 3 |
| Negative inputs | treated as unset, never panics |

**Unit — other packages**
- `rolefile`: `Parse`/`Marshal` round-trip preserves both new fields.
- `config`: validation rejects `min > max`, negatives, unsatisfiable floors.

**Integration**
- Dispatch a role with `min_turns: 8` via `spawn_subagent` passing
  `max_turns: 1`; assert the loop's `MaxToolTurns == 8`.
- Assert span attributes carry the resolved value and source.
- Regression: `supervised_task` with `max_iterations: 1` must **still**
  be able to force exhaustion (Alternative A rejection depends on this;
  a role with no `min_iterations` must remain forceable).

**Manual smoke** — via the `yaah-dev-loop` skill (HTTP MCP hot-reload):
rebuild, restart on `127.0.0.1:7333`, confirm `pid` changed via
`mcp__yaah__status`, dispatch a reviewer, inspect
`mcp__yaah__traces --tree` for the budget attributes.

## 8. Acceptance criteria

1. A role file declaring `min_turns: N` always receives at least `N`
   tool-using turns regardless of per-call arguments.
2. Per-call overrides can still *raise* budgets up to the role ceiling
   and can still force exhaustion for roles without floors.
3. Omitting `max_turns` no longer silently yields 3.
4. Effective budget + source visible in traces and `list_subagents`.
5. Budget resolution lives in one pure, fully-tested function.
6. `go test ./...`, `go vet ./...`, `gofmt -l .`, `staticcheck ./...`
   all clean.

## 9. Risks

| Risk | Mitigation |
|---|---|
| §4.4 raises budgets for roles that relied on the implicit 3 → higher token spend | Phase 5 sets explicit `max_turns` on every shipped role, so the new fallback only affects roles that never expressed an opinion. Call out in changelog. |
| Reconciliation growing `iterations` could exceed a provider's practical context | `HardCeiling` (50) bounds it; context window is a separate, already-enforced guard. |
| Adding fields to `Frontmatter` and forgetting a hop silently drops them | Explicit round-trip test in Phase 3; the three hops are enumerated in §4.5. |
| Orchestrator keeps setting low `max_turns` out of habit | Floors make it harmless; §4.8 wording removes the incentive. |

## 10. Open questions

1. Should `min_turns` in `config.yaml` be allowed to *exceed* a role
   file's `max_turns`? Proposed: yes — operator config outranks role
   authorship, consistent with the existing comment that
   "config-level overrides are authoritative and bypass the ceiling".
2. Should there be a global `min_iterations` default alongside
   `default_min_turns`? Proposed: defer until asked; `min_turns` is the
   binding dimension.
3. Should exceeding a floor emit a warning to the orchestrator's
   transcript (visible feedback that its override was overruled), or
   stay trace-only? Proposed: trace-only first, revisit if the model
   keeps fighting the floor.

