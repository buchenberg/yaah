# orch-* bench tasks → workflow IR mapping

Design check for the P0 exit criterion of the typed workflow DSL (bead
`yaah-0u0v.10`): **the IR can express every `orch-*` bench task's
orchestration shape** — *express*, not *execute*. Each row states the shape the
scenario forces, the v1 constructs that carry it, and whether v1 closes.

Evidence read for this table: each task's `task.yaml` (behavior forced) and
`bench-orchestration.yaml` (engine config pinned by the scenario);
`internal/workflow/spec/types.go`, `internal/workflow/spec/enums.go`,
`internal/workflow/spec/decode.go`; `internal/workflow/ir/resolve.go`,
`internal/workflow/ir/selector.go`, `internal/workflow/ir/graph.go`,
`internal/workflow/ir/template.go`. Construct names are cited as `spec.*`
(authoring surface) and `ir.*` (resolved surface) so every row is checkable
against code.

## Legend — the v1 vocabulary being mapped onto

| Construct | Citation |
|---|---|
| Node kinds `delegate`, `fanout`, `branch`, `loop`, `gate`, `settle`, `select`, `aggregate`, `call`, `emit`, `human` | `spec.KindDelegate`…`spec.KindHuman` (`types.go` "Node kinds"), mirrored in `ir.Kind` |
| Lanes `report` / `proposal` | `spec.LaneReport`, `spec.LaneProposal` |
| Join `all` / `any` / `quorum` | `spec.JoinAll`, `JoinAny`, `JoinQuorum` (`enums.go`: "P1 executes `all` only") |
| Settle verbs `select` / `apply` / `release` / `discard` | `spec.SettleSelect`…`SettleDiscard` |
| Settle policy `path_disjoint` / `three_way` / `judge` / `first_success` / `smallest_diff` | `spec.Policy*` (single shared `policy` key; per-kind membership is a validator concern, `types.go` `Node` doc) |
| Conflict / empty dispositions | `spec.OnConflictStop|Skip|Escalate`, `spec.OnEmptyPass|Fail|Skip` |
| Failure dispositions | `spec.OnFailureAbort|Continue|Skip`, `spec.OnItemAbort|Continue|CollectFailures` |
| Fan-out source: literal list or selector | `spec.Over{Selector, Values}`, `ir.Over{Literals, Selector}` |
| Loop bound, exit, per-iteration results | `spec.Node.MaxIterations`, `.Until`, `.Collect`; `ir.resolveLoop` |
| Proposal-lifecycle threading | `spec.Node.SeedFrom`, `.ContinueFrom`, `.ForkFrom` (delegate *and* loop: `ir.resolveDelegate`, `ir.resolveLoop`) |
| Template bindings | `{{ params.* }}`, `{{ steps.<id>.output.* }}`, `{{ steps.<id>.collect }}`, `{{ <as>.field }}`, `{{ index }}`, `{{ run.id }}`, `{{ run.attempt }}` — `ir.compileRef` |
| Selector predicates (at most one) | `spec.Selector{Equals,NotEquals,In,NotEmpty,Exists,AsList}` → `ir.PredOp` |
| Dispatch ceiling | `spec.Budgets{MaxDispatches,MaxWallClockSeconds,MaxTokens,MaxConcurrentDispatches}` |
| Shared dispatch defaults | `spec.Defaults{Lane,Network,Grants}`, folded in `ir.resolveDelegate` |
| Artifact write | `spec.Node.Value` + `spec.Node.Path` → `ir.Node.ArtifactPath` |

## Mapping — one row per task

| Task | Orchestration shape the scenario forces | IR expression in v1 | Verdict |
|---|---|---|---|
| `orch-fanout` | Six independent per-module audits, ≥3 concurrent `developer` dispatches, orchestrator writes the six result files itself | `spec.KindFanout` with `spec.Over.Values` literal list (the module set is static) or `spec.Over.Selector` over a planner `output.workstreams`; `spec.As: ws` item binding; `spec.MaxParallel: 3` (+ `spec.Budgets.MaxConcurrentDispatches` as the ceiling); `spec.JoinAll`; `spec.OnItemContinue`; body `spec.KindDelegate` `lane: report`; `spec.Collect`; six writes as `spec.KindEmit` `value` + `path` (`ir.Node.ArtifactPath`); item fields via `{{ ws.name }}` | **Full** |
| `orch-report-fanout` | Four read-only audits through ≥2 concurrent `reviewer` dispatches on the report lane; **no** proposal sealed, no settlement, host tree byte-identical | Same fan-out spine with `spec.LaneReport` on the body delegate (`spec.Defaults.Lane: report` makes it structural); `spec.JoinAll`; results surface as outputs, not proposals. The "must not seal" property is lane identity, already in the graph | **Full** |
| `orch-settle-apply` | Delegate on the retained lane, then the orchestrator edits the *parent* tree **after** the seal so the basis moves, then settles with `apply` (three-way merge) | `spec.KindDelegate` `lane: proposal` → `spec.KindSettle` `action: apply` (`spec.SettleApply`), `on_conflict`, `on_empty`, `order` (`spec.OrderSmallestDiffFirst`), `proposals` selector, `where` selector; `spec.Needs` orders seal→settle. **Two things do not land:** the host-side edit has no node kind (see G1) and `policy: three_way` (`spec.PolicyThreeWay`) is decoded but never reaches the graph (see G2) | **Partial** (G1, G2) |
| `orch-settle-discard` | Delegate, settle `discard`, write `answer.txt: discarded: <proposal_id>`, tree unchanged | `spec.KindDelegate` `lane: proposal` → `spec.KindSettle` `action: discard` (`spec.SettleDiscard`), `proposals` selector, `on_empty: pass`; then `spec.KindEmit` `value: "discarded: {{ steps.<id>.output.proposal_id }}"` + `path` | **Full** |
| `orch-settle-discard-mock` | Identical graph to `orch-settle-discard`, replayed against a deterministic mock provider; the mock sidecar injects traps mid-run | Shape as above; per-node provider pinning via `spec.Node.Provider` / `spec.Node.Model` (mock provider is a config concern, not a graph one). Trap injection is fault injection **under** the graph: the graph-facing response is `spec.Retry{MaxAttempts,BackoffSeconds}` + `spec.OnFailure*`, which is the right vocabulary | **Full** (shape); fault injection is deliberately not an IR construct |
| `orch-guard` | One delegate; the child retries a flaky command one tool call at a time until success; a supervisor rule (`threshold: 0.5, window: 2`) injects a warning into the child's conversation; orchestrator reports attempt count + warning text | Workflow-granularity retry maps to `spec.KindLoop` `max_iterations` + `until` + `collect` (`ir.resolveLoop`). **Not expressible:** the supervision rule itself — `spec.Budgets` has no error-rate/window field, `spec.Node` has no advisory/supervisor field, and there is no output surface for an injected warning (G3). The per-*tool-call* retry inside the child is the sub-agent's own turn loop, which the IR models only at node granularity | **Partial** (G3, and G6 for `{{ index }}`) |
| `orch-quality-gate` | Delegate, an implicit role-keyed gate dispatches a tester against the result; on `[quality-gate:FAIL]` fix and re-verify until pass | Exactly the bounded-repair shape, now golden-covered: `spec.KindDelegate` (proposal) → `spec.KindLoop` `max_iterations: 3` + `until {from: steps.<loop>.collect, field: verdict, equals: pass}` + `collect` + `continue_from`, body = [`fix` delegate `lane: proposal`, `check` delegate `lane: report` `output: Verdict`] → `spec.KindSettle` `action: apply` → `spec.KindEmit`. `spec.KindGate` `assert`/`judge` (`spec.Judge{Role,Prompt}`) is the alternative single-check form. The bench's `quality_gates: {developer: [tester]}` auto-dispatch is role-keyed runtime config with no spec field (G4) | **Full** (as `bounded_repair`; see `internal/workflow/spec/testdata/valid/bounded_repair.yaml`) |
| `orch-compact` | Twenty-four sequential reads by the orchestrator alone under a 6 000-token window; compaction must roll the context and preserve tool-call linkage; one output line per file in filename order | The readable spine is `spec.KindFanout` `over: [24 literal items]` + `max_parallel: 1` + `join: all`, then `spec.KindAggregate` `inputs` + `strategy: concat` (`spec.AggregateConcat`), then `spec.KindEmit` `path`. **Not expressible:** every load-bearing part of the task. Context-plane knobs (`context_window`, `compaction_threshold`, `raw_compaction_threshold`, `compact_max_messages`, `prune_min_turns`) have no home in `spec.Budgets`, which covers cost ceilings only (G5). Aggregate has no ordering key, so "filename order" is unstated (G5b) | **Partial** (G5, G5b) |
| `orch-recovery` | A previous run crashed with a live worktree; `yaah shepherd` scans the durable trace and adopts the orphaned scope; report the adopted path | Nothing in the node vocabulary. Adoption/resume is a **pre-run lifecycle step**, not a graph edge: `spec.Node.SeedFrom`/`ContinueFrom`/`ForkFrom` thread proposals *inside* a run, and `ir.resolveLoop` even accepts `continue_from`, but there is no resume/adopt node kind and no workflow-level placement fields (the bench's `shepherd_trace_path` / `retained_repo_path` have no counterpart; `spec.Node.World` is a free-form string on delegates only) (G7) | **Not expressible** (by design — belongs to a run-lifecycle section, not to P0 nodes) |
| `orch-escalation` | Read a task whose dataset does not exist; the child must refuse to fabricate and return the structured escalation block (`severity ≥ blocker`) verbatim | `spec.KindDelegate` + `spec.OutputSpec{Type, Schema}` can pin the *shape* of the block by JSON Schema ref, and `spec.KindHuman` `on_timeout` (`spec.OnTimeout`) is the only escalation-flavored node. **Missing:** no `Escalation` member of `spec.OutputType` and no severity field, so "escalate and stop without this being a failure" has no disposition — `spec.OnFailure*` conflates escalation with abort/failure (G8) | **Partial** (G8) |
| `orch-confinement` | Delegate in an isolated worktree under a syscall jail (`containment: paddock`); an out-of-workspace write must be **denied**, then settle `apply` | `spec.LaneProposal` + `spec.Node.Grants` (`spec.Grant{Path, Access}`) + `spec.Node.Network` (`spec.NetworkDenyAll` etc.) + `spec.Node.World` give placement and network posture. **Missing:** `containment` has no field, `spec.GrantAccess` is only `readonly`/`readwrite` (no enforced-vs-advisory, no fail-closed), so "the jail denied the escape and the verifier required a non-advisory placement" is not statable in the graph (G9) | **Partial** (G9) |

## Verdict tally

| Verdict | Count | Tasks |
|---|---|---|
| Full | 5 | `orch-fanout`, `orch-report-fanout`, `orch-settle-discard`, `orch-settle-discard-mock`, `orch-quality-gate` |
| Partial | 5 | `orch-settle-apply`, `orch-guard`, `orch-compact`, `orch-escalation`, `orch-confinement` |
| Not expressible | 1 | `orch-recovery` |

**Reading of the criterion.** The *dispatch-graph* portion of all 11 shapes is
expressible today: every task's delegate/fan-out/settle/loop skeleton resolves
against the v1 vocabulary. What is not expressible is the **run-environment**
portion — supervision policy, context-plane budgets, confinement mode,
escalation as a disposition, host-side mutation ordering, and run lifecycle.
That split is worth naming explicitly in the P0 criterion, because four of the
five "Partial" rows are blocked by the environment surface, not by the node
vocabulary. G2 is the exception, and it is a defect, not a gap.

## Gaps

Ordered by how much they cost this criterion.

- **G2 — `settle.policy` decodes but never reaches the IR (defect).**
  `spec.Node.Policy` is populated for both `select` and `settle` (they share the
  `policy` key by design, `types.go`), but `ir.resolveSettle` never assigns it
  and `ir.Node` has no settle-policy field — the `Policy` field on `ir.Node` is
  typed `ir.SelectPolicy` under the "gate and select" block. Checkable:
  `spec/testdata/golden/fanout_settle.canonical.json` carries
  `"policy":"path_disjoint"` while `ir/testdata/golden/fanout_settle.graph.json`
  has no `policy` key on the `land` step; same for
  `"policy":"three_way"` in the `bounded_repair` pair. `convertSelectPolicy`
  also *rejects* `path_disjoint`/`three_way` as "unconvertible select policy",
  so the values have no IR converter at all. Effect: the merge policy that
  distinguishes `orch-settle-apply` from a fast-forward `select` is invisible to
  any executor reading the graph. Needs an `ir.SettlePolicy` enum + field +
  converter (or an explicit decision that policy is resolved pre-IR).
- **G1 — no host/operator node.** The parent-side `CHANGELOG.md` edit that moves
  the basis between seal and settle (`orch-settle-apply`) has no kind. `spec.KindEmit`
  `path` writes an artifact; "mutate an existing tracked file in the parent
  tree" and "append one line" are not defined for it. Until that exists, a
  three-way-apply scenario cannot be authored, only hand-run by the model.
  Belongs with the executor phase.
- **G3 — supervision/advisory policy has no spec surface.**
  `agents.default.supervisor.rules` (`high_error_rate` threshold+window,
  `destructive_guard` enable/disable) drives `orch-guard` end to end;
  `spec.Budgets` and `spec.Node` carry nothing equivalent, and there is no
  output binding for an injected advisory. Candidate: a `supervision:` block
  parallel to `budgets:` plus a verdict/output type for advisories.
- **G5 — context-plane budgets are absent.** `spec.Budgets` bounds cost
  (dispatches, wall clock, tokens, concurrency); `orch-compact` needs window
  size, compaction thresholds, message-count rollover, and a prune deferral.
  Also `orch-guard`'s `max_agent_timeout_sec` and the bench `agents.subagent.max_concurrency`
  only partly land (`TimeoutSeconds`, `MaxConcurrentDispatches`). Candidate: a
  `context:` block beside `budgets:` in a later phase; not P0-blocking for the
  graph itself.
  - **G5b — no ordering key on `aggregate`.** `spec.AggregateStrategy` is
    concat/summarize/json_merge; "one line per file in filename order" is
    implicit in item order and uncheckable.
- **G4 — role-keyed implicit gate dispatch is not a graph construct.**
  `quality_gates: {developer: [tester]}` auto-spawns a checker after every
  developer dispatch. The IR makes this explicit (a `check` body node or a
  `spec.KindGate` with `assert`/`judge`), which is *better* for authoring, but
  the implicit engine behavior has no spec field, so a workflow cannot declare
  "this role always gets a gate". Decide: keep explicit (recommended) or add a
  `gates:` map to `spec.Defaults`.
- **G6 — `{{ index }}` is unavailable in a loop body (defect).**
  `ir.compileRef` admits `index` when `len(lex.items) != 0`, and its error text
  claims "index is only in scope inside a fan-out or loop body"; but
  `ir.resolveBody` receives the item name only from `resolveFanout`
  (`n.As`), while `resolveLoop` and `resolveAggregate` pass `""`. So a top-level
  `loop` body cannot number its iterations — exactly what a bounded repair loop
  wants (`"attempt {{ index }} of {{ max_iterations }}"`). `bounded_repair`
  avoids `{{ index }}` for this reason. One-line fix in `resolveLoop`; pin it
  with a golden update.
- **G7 — no run-lifecycle surface (resume / adoption / world placement).**
  `orch-recovery`'s shape is a pre-graph step. `spec.Node.World` is a free-form
  string with no enum and no workflow-level default, so trace and retained-repo
  placement are unrepresentable. A `call` node (`spec.KindCall`, `Workflow`,
  `Params`, `MaxDepth`) is the intended later home for composite reuse (P2);
  adoption needs either a lifecycle section or `yaah shepherd` staying
  CLI-only. If it stays CLI-only, say so in the P0 criterion rather than
  counting `orch-recovery` as a graph shape.
- **G8 — escalation is not a first-class disposition.** No `Escalation` in
  `spec.OutputType` (Report/Proposal/Settled/Verdict/Plan/Artifact), no
  `severity`, and `spec.FailurePolicy` conflates "child refused and told me why"
  with abort/continue/skip. `OutputSpec.Schema` is the only current handle on
  the shape.
- **G9 — confinement mode is unrepresentable.** `containment: paddock`, the
  jail backend, and the enforced-vs-advisory distinction have no fields;
  `spec.GrantAccess` is readonly/readwrite only. A workflow cannot state
  "deny writes outside the workspace, fail closed if the jail is advisory".
- **Join variants (`spec.JoinAny`, `spec.JoinQuorum`) decode but are not
  exercised by any orch task** — no scenario needs an early join, so P2
  `any`/`quorum` work has no bench forcing function. Recorded here so the phase
  plan does not assume bench demand for it.

## Not covered by this table

The bead's third acceptance item — the `explain` CLI that renders a resolved
graph — is **deferred to `yaah-0u0v.9`**; it is not attempted here. This table
is the design check only. Goldens are regenerated with each suite's own
`-update` flag (`go test ./internal/workflow/spec -run TestValidFixtures
-update`; `go test ./internal/workflow/ir -run TestResolveGoldenSpecs -update`).
