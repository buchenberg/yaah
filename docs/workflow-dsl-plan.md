# yaah Workflow DSL — Detailed Plan

> **Status:** accepted — implemented as `yaah-0u0v` (P1 complete, P2 in progress; see the epic bead `yaah-0u0v`)
> **Date:** 2026-11-11 (design) · 2026-11-14 (tracked in-repo)
> **Scope:** a declarative orchestration layer over yaah's existing dispatch,
> proposal, and settlement primitives.
> **Related:** `yaah/docs/shepherd-parity.md`, `yaah/docs/sub-agents.md`,
> `yaah-shepherd-gap-report.md`, bead `yaah-hxk3.5`.

**Contents:** [1. Summary](#1-summary) · [2. Goals and non-goals](#2-goals-and-non-goals) ·
[3. Prior art](#3-prior-art-and-positioning) · [4. Design principles](#4-design-principles) ·
[5. The model](#5-the-model) · [6. YAML surface](#6-yaml-surface) ·
[7. Validation](#7-validation-and-static-analysis) · [8. Runtime](#8-runtime-architecture) ·
[9. Security](#9-security-model) · [10. Delivery plan](#10-delivery-plan) ·
[11. Testing](#11-testing-strategy) · [12. Bench mapping](#12-bench-mapping) ·
[13. Risks](#13-risks-and-mitigations) · [14. Decisions](#14-decisions-to-accept-wd1wd10) ·
[15. Open questions](#15-open-questions) · [16. IR sketch](#16-appendix-a--ir-sketch-json) ·
[17. Glossary](#17-appendix-b--glossary) · [18. References](#18-references)

---

## 1. Summary

yaah already has the runtime an orchestration DSL needs: `delegate` fans out
sub-agents, the retained lane seals each worktree or container into a
**proposal**, `proposal` settles it exactly once with `select | apply | release
| discard`, and the trace kernel records the whole causal chain. What it does
not have is a way to express a multi-step orchestration as a **reviewable,
portable artifact** instead of a sequence of model turns.

This plan adds that artifact: a small, fixed-vocabulary workflow IR with a YAML
surface. The IR is deliberately *not* a programming language. It is a dispatch
plan with first-class proposals, explicit fan-out/join/loop semantics, and
static analysis for authority and cost. It compiles onto the verbs yaah already
implements; it does not reimplement them.

**Verdict on viability:** high as an internal yaah feature; not worth pursuing
as a new open standard. The differentiator is settlement, not syntax. The main
risk is that the orchestrator LLM is already a runtime, so the DSL must be an
artifact the orchestrator can emit and the host can validate and execute — not
a cage around the orchestrator.

**The single sentence to keep:** *the workflow file is a permission plan and a
cost bound as much as it is a control-flow description.*

### 1.1 The human use case

Today an orchestration is a performance. The operator prompts, watches turns,
copy-pastes diffs, and the process lives in their head and in a chat thread.
The DSL turns it into an artifact the team owns.

**A recurring chore.** Every quarter a core dependency upgrades. The shape is
always the same: read the changelog, find the call sites, split them into
disjoint batches, update the batches in parallel, review each diff, land the
passing ones, summarize for the PR. Today the operator re-types that sequence
and babysits it. With the DSL they write `upgrade-dependency.yaml` once:
planner reads the changelog; fan out up to four developers over the batches;
each writes only under `src/` and returns a proposal; a reviewer checks each
diff; `settle` applies the passing, path-disjoint proposals; a repair fan-out
continues the rejected ones; `emit` writes the summary.

They run three commands:

```bash
yaah workflow validate upgrade-dependency.yaml
yaah workflow explain upgrade-dependency.yaml --param version=2.4
yaah workflow run     upgrade-dependency.yaml --param version=2.4
```

`explain` answers the questions a human actually has before delegating:
*How many agents? What can they touch? What is the worst-case cost? What
happens to a rejected attempt?* The run returns a small queue of proposals, not
a wall of tool calls. The human reviews six diffs instead of forty turns. Next
quarter they change one parameter. A teammate can read the file and see what
will happen. A reviewer can diff a change to the workflow like any other code.

The shift, in one line: **from prompting in the moment to editing a durable
recipe; from supervising turns to approving outcomes.**

| Human | What they do | What they stop doing |
| --- | --- | --- |
| Operator (solo dev) | Writes a recipe for a recurring chore; runs it with params; reviews proposals | Retyping the same multi-step prompt; babysitting turns |
| Teammate | Reads, runs, or copies a recipe and changes one role | Reverse-engineering someone else's prompt |
| Team lead / platform engineer | Reviews workflow files as policy: roles, grants, fan-out caps, budgets, egress | Trusting every dispatch to be well-behaved |
| Reviewer / approver | Gets sealed proposals with diffs, verdicts, and lineage | Reading transcripts to find out what changed |
| Auditor / on-call | Reads `explain` before and the trace after: spec hash, dispatches, proposals, settlements | Reconstructing a run from chat logs |
| Model orchestrator | Proposes a plan as a spec; gets it validated, explained, and approved; the host executes it | Silently spawning an unbounded swarm |
| Batch operator | Schedules one bounded workflow across repos or issues; dry-runs first | Hand-running the same process N times |
| Researcher | Treats the spec as the experimental variable; compares traces across variants | Retyping slightly different prompts |

**When not to use it.** Open-ended work — "figure out why this test is flaky",
"explore this codebase", "fix whatever is broken" — has no known shape yet.
That is the orchestrator's job. Write a workflow once the shape is understood
and repeated, not before.

---

## 2. Goals and non-goals

### 2.1 Goals

1. Express a multi-step orchestration (sequence, fan-out, fan-in, branch,
   bounded loop, gate, settle) in a versionable text file.
2. Make **proposals and settlement** first-class values, including parallel
   proposals, review verdicts, path-disjoint application, and explicit
   disposition of every attempt.
3. Make the plan **analyzable before it runs**: delegation graph, maximum
   fan-out, worst-case dispatch/turn/token budget, union of filesystem and
   network authority, and every proposal's possible fate.
4. Make the run **auditable**: the spec hash, resolved graph, node outcomes,
   dispatches, proposals, and settlement decisions all land in the trace.
5. Make the spec **authorable by a model**: strict schema, precise validation
   errors, dry-run, and a repair loop.
6. Keep the existing code path and the DSL path on one IR, so the DSL is an
   optional front-end rather than a parallel universe.

### 2.2 Non-goals

- A general-purpose programming language (no user-defined functions, no
  arbitrary expressions, no imports, no filesystem I/O inside the spec).
- Arbitrary cycles or graph re-planning. Bounded `loop`/`until` only; unbounded
  replanning stays in the orchestrator loop (`AgentGraph`-style), not the DSL.
- Replacing the interactive orchestrator. The DSL is for known-shape work; the
  orchestrator remains the driver for open-ended work.
- A visual editor in the first release.
- Cross-harness portability. If it happens later, it happens by publishing the
  IR, not by designing for it now.
- Retaining the root workflow run. Consistent with repo decision D8, the workflow
  executor is a driver, not a retained run; only its dispatches are retained.

---

## 3. Prior art and positioning

| System | What matches | What it lacks for yaah |
| --- | --- | --- |
| **AgentSPEX** (arXiv 2604.13346, Apr 2026) | YAML, typed steps, `if/switch`, `while`, `for_each`, `parallel`, `call` submodules, checkpointing, visual editor | Agents write directly; no sealed proposals, no settlement, no OS confinement |
| **Microsoft Agent Framework — Declarative Workflows 1.0** (Jul 2026) | YAML `kind: Workflow`; invoke agents, branch, loop, HITL, checkpoint/resume; same runtime as code-first | No proposal/consensus semantics; enterprise runtime |
| **CrewAI** | YAML agents/tasks, `Process.hierarchical` (manager LLM picks workers) | Control flow stays in Python; no isolation/settlement |
| **Prowl / OpenClaw workflow / AgentOS `workflow()`** | Fork/join, repeat, verdict-gated loops, partial resume, DAG compile with cycle check | Direct writes; no retained outputs; join/race semantics still incomplete |
| **AWS Step Functions ASL / CNCF Serverless Workflow / Argo** | Mature control-flow DSL (`Choice`, `Parallel`, `Map`, `Retry`, `Catch`) | No agent semantics |
| **LangGraph / AutoGen / Mastra / Google ADK / OpenAI Agents SDK** | Graph/team/workflow composition | Code, not markup; no settlement |
| **ADL (adl-spec.org)** | Declarative agent passport: identity, capabilities, permissions | Definition, not orchestration |
| **Shepherd (Python) / yaah** | Typed tasks, combinators, retained outputs, consume-once settlement | Combinators are code; no DSL; parity decision is open (`yaah-hxk3.5`) |

**Positioning statement.** AgentSPEX and Microsoft AF own "YAML that calls
agents." yaah should own **"YAML where every branch ends in a reviewable
proposal."** That is a different value proposition and it is the one the
existing runtime is already built for.

**Adopt/decline against Shepherd combinators** (resolves `yaah-hxk3.5` for the
DSL surface):

| Shepherd combinator | DSL decision | IR construct |
| --- | --- | --- |
| `sequence`, `sequence_all` | adopt | ordered `steps` + `needs` |
| `branch` | adopt | `branch` with structured cases |
| `loop` | adopt, bounded only | `loop` with `max_iterations` |
| `retry` | adopt as policy | node-level `retry` |
| `fallback` | adopt | `on_failure: continue` + `branch`, or a `fallback` node |
| `gate` | adopt | `gate` with deterministic assert or judge role |
| `budget`, `timeout` | adopt | `budgets` + per-node `timeout_seconds` |
| `parallel`, `parallel_all`, `race` | adopt | `fanout` + `join` (all/any/quorum) |
| `speculate` | adopt via fan-out + selection | `fanout` + `select` (`policy: judge`) + `settle` |
| `recover` | decline | explicit `branch`/`on_failure` instead |
| `filter_effects`, `map_effects`, `tap`, `scope_tap` | decline | trace already records effects |
| Context binding / effect fold | decline for v1 | revisit only with a concrete need |

---

## 4. Design principles

1. **Deterministic skeleton, agentic leaves.** The host executes control flow;
   LLM judgment happens only at explicit `delegate`, `judge`, and `select`
   nodes. No prompt is allowed to *be* the control flow.
2. **No expression language.** Values come from `params`, `steps.<id>.output`,
   and `item`/`index` inside a fan-out. Predicates are structured or named
   functions implemented in Go. This is the single biggest scope-control
   decision.
3. **Linear proposals.** A proposal is a handle with exactly one disposition.
   The validator proves exhaustion on every path; the runtime refuses to end a
   run with an open proposal unless the spec says `on_abort: release_all`.
4. **One IR, two front-ends.** YAML for humans; JSON for models. Both parse to
   the same `WorkflowSpec`, and the executor never sees YAML.
5. **Plan is data, execution is policy.** The spec describes *what*; the host
   decides *how* (concurrency, world, jail) from config plus node declarations.
6. **Everything is in the trace.** Spec hash, resolved graph, node attempts,
   dispatch scope ids, proposal ids, settle verbs. A run must be explainable
   without the model transcript.
7. **Fail closed.** Unknown fields, undeclared roles, overlapping grants,
   unbounded loops, and missing settlement are validation errors, not warnings.
8. **The spec is untrusted.** Treat it like a CI pipeline file from a fork:
   validate, bound, allowlist, least privilege.

### 4.1 What keeps this clean

The DSL is a front-end, not a second runtime. Cruft enters at the boundary, so
the boundary is the design.

**One core, two adapters.** Extract the bodies of `DelegateTool.Execute` and
`ProposalTool.Execute` into typed functions — `dispatch.Run(ctx,
DispatchRequest)` and `dispatch.Settle(ctx, id, verb)`. The model-facing tools
and the workflow executor both call those. The executor never builds JSON tool
arguments, parses envelopes, or shells out to a tool.

**Narrow runtime interface.** The executor sees only:

```go
type Runtime interface {
    Dispatch(context.Context, DispatchRequest) (DispatchResult, error)
    Settle(context.Context, string, SettleVerb) (Settlement, error)
    Record(context.Context, Event) error
}
```

No tools, no prompts, no provider clients, and no SQLite in the core.

**One state reducer.** The executor emits events; one `Apply(event)` function
owns run state. The trace is a projection of the same events. Do not keep a
`workflow_node` table and a separate in-memory graph that can disagree.

**Compile at load.** Templates become segment lists, selectors become a small
AST, defaults are merged, and the graph is resolved once. The executor never
parses a string at run time.

**A fixed switch, not a plugin registry.** Node handlers are `switch node.Kind`
in one file. A registry or interface appears only when a second implementation
exists. The same rule applies to aggregate strategies and selector functions.

**Plain validation.** `func validate(*IR) []Diagnostic` with rule functions. No
validation framework, no code generation, no i18n.

**No compatibility scaffolding.** No migrations, no deprecation aliases, no
dual schema versions, no `--legacy` flag. If a field is wrong, change it.

**No new config system.** Reuse `agents.subagent.*`, roles, grants, and egress.
The spec adds only node-level overrides that do not already have a home.

**No feature without evidence.** Every node kind and policy needs either a
passing `orch-*` bench task or a real workflow in the repo. The cruft test for
each PR: does it add a second way to do something, an abstraction with one
implementation, or a config knob nobody uses?

**Deliberately not in the MVP (P0–P1):** `call`, `human`, `render`,
`any`/`quorum`, `fork_from`, `select` policies beyond `judge`, version
migration, cross-harness export, and the visual editor. Each is added later
only with a bench task or a real workflow.

---

## 5. The model

### 5.1 Two layers in one document

```
WorkflowSpec
├── deterministic layer (host executes)
│   ├── steps + needs       sequence / DAG
│   ├── fanout / join       data-dependent parallelism
│   ├── branch              route on a typed verdict
│   ├── loop / until        bounded repetition
│   ├── gate                accept / reject / escalate
│   ├── settle              consume proposals
│   └── aggregate, emit     shape values and outputs
└── agentic layer (models execute)
    ├── delegate            one sub-agent run
    ├── judge               a verdict-producing delegate
    └── select              choose among candidates (policy + optional judge)
```

The deterministic layer must be replayable with no model calls. The agentic
layer's outputs are recorded in the trace so a replay can substitute them.

### 5.2 Value types

| Type | Produced by | Consumed by | Notes |
| --- | --- | --- | --- |
| `Report` | `delegate` (report lane) | `aggregate`, `branch`, `emit` | text + optional validated JSON |
| `Proposal` | `delegate` (proposal lane) | `settle`, `delegate.seed_from` | opaque handle; `id`, `diff`, `files` |
| `Settled` | `settle` | `emit`, `branch` | `{action, proposal_id, result}` |
| `Verdict` | `judge`, `gate` | `branch`, `settle.where` | `{pass: bool, reasons: []}` |
| `Plan` | `delegate` with `output: Plan` | `fanout.over` | JSON validated against a schema |
| `Artifact` | `emit`, `aggregate` | `emit` | named path + bytes/hash |

### 5.3 Node kinds (v1)

Common fields on every node: `id` (unique), `kind`, `needs` (list of node ids),
`when` (optional structured condition), `retry` (`max_attempts`,
`backoff_seconds`), `timeout_seconds`, `on_failure`
(`abort | continue | skip`), `output` (name + optional JSON Schema).

| Kind | Purpose | Key fields |
| --- | --- | --- |
| `delegate` | one sub-agent dispatch | `role`, `prompt`, `lane` (`report`/`proposal`), `output`, `seed_from`, `continue_from`, `fork_from`, `provider`, `model`, `max_turns`, `grants`, `network`, `world` |
| `fanout` | run a body per item, with a bound | `over`, `as`, `max_parallel`, `body`, `collect`, `join` (`all`/`any`/`quorum`), `quorum`, `on_item_failure` |
| `branch` | route on a value | `on` (selector), `cases` (literal match), `default` |
| `loop` | bounded repetition | `body`, `until`, `max_iterations`, `continue_from`, `collect` |
| `gate` | deterministic assert or judge | `assert` (named check + args) or `judge` (`role`, `prompt`), `on_pass`, `on_fail` |
| `settle` | dispose proposals | `proposals`, `where`, `action`, `policy`, `on_conflict`, `on_empty`, `order` |
| `select` | choose among candidates | `candidates`, `policy` (`judge`/`first_success`/`smallest_diff`), `judge`, `output` |
| `aggregate` | combine values | `inputs`, `strategy` (`concat`/`summarize`/`json_merge`), `body` (for `summarize`) |
| `call` | invoke a named subworkflow | `workflow`, `params`, `max_depth` |
| `emit` | write a named output | `value`, `path` (optional artifact) |
| `human` | pause for approval (P2) | `prompt`, `timeout_seconds`, `on_timeout` |

A `fanout` or `loop` body may be a single node or a list of nodes with unique
local ids. Inside the body, earlier local ids are in scope; outside, reference
the container with `steps.<container>.collect` (one value per item or
iteration) or `steps.<container>.output` (the body's last node).
`seed_from` starts a fresh dispatch from a sealed workspace; `continue_from`
maps to `proposal continue` and supersedes the proposal.

`fanout` subsumes `parallel_all`; `join` covers `parallel`/`race`; `gate` covers
`gate`/`critique` patterns; `settle` is the proposal-specific primitive no
other DSL has. A bare string is shorthand for `{ from: <path> }`, and a bare
`output: Name` is shorthand for `{ name: Name }`.

> **Naming note.** `select` the node and `settle.action: select` are different
> operations: the node chooses among candidate values; the action fast-forwards
> a proposal. If the collision causes confusion in user testing, rename the
> node to `choose` before `v1beta1`.

### 5.4 Selectors and templates

Selectors are structured, not expressions:

```yaml
over:
  from: steps.plan.output.workstreams
where:
  from: steps.review.collect
  field: verdict
  equals: pass
```

Templates support only interpolation of in-scope values:

```yaml
prompt: |
  Implement this workstream: {{ item.brief }}
  Overall goal: {{ params.task }}
```

Allowed roots: `params.*`, `steps.<id>.output`, `steps.<id>.collect`,
`item.*`, `index`, `run.*` (run id, attempt). Selector predicates are limited
to `equals`, `not_equals`, `in`, `not_empty`, and `exists`. `as_list: true`
wraps a single value into a one-element list. No function calls, no arithmetic,
no conditionals inside a template. If a transform is needed, it becomes a
named `aggregate` strategy or a Go-implemented selector function with a fixed
signature.

**Large values are references, not text.** A `Report`, `Proposal`, or
`Plan` above a configured size is bound as a handle. A template renders a
bounded preview and the node can request the full value through `value_ref`.
This keeps a fan-out of ten diffs from silently consuming the orchestrator's
context window, and it mirrors the existing `output_limit` discipline.

This is intentionally restrictive. An expression language is how orchestration
DSLs become unmaintainable programming languages.

### 5.5 Join semantics

| Join | Behavior | Cancellation |
| --- | --- | --- |
| `all` | wait for every item; fail per `on_item_failure` | none |
| `any` | first success wins; remaining items cancelled | required |
| `quorum` | `quorum` successes; remaining cancelled | required |

P1 ships `all` only. `any`/`quorum` land in P2 with explicit cancellation
tests, because "first wins, cancel the rest" is where every DSL in this space
has bled (see Prowl's V2 notes).

### 5.6 Settlement semantics

`settle` is a linear operation over a set of proposals:

```yaml
- id: land
  kind: settle
  proposals: steps.build.collect
  where:
    from: steps.review.collect
    field: verdict
    equals: pass
  action: apply
  policy: path_disjoint
  order: smallest_diff_first
  on_conflict: stop
```

Rules:

1. `action` is one of `select | apply | release | discard`.
2. `select` is fast-forward-only; `apply` is path-disjoint (or `three_way` for
   worktree proposals). These map directly to the existing `proposal` tool.
3. `on_conflict` is `stop | skip | escalate`. `stop` halts the workflow with a
   machine-readable conflict; `skip` leaves the proposal open for a later
   `settle`; `escalate` routes to a `human` or judge node.
4. Every proposal must end with exactly one disposition on every reachable
   path. A proposal selected into `proposals` but dropped by `where` must be
   handled by a subsequent `settle` with `action: release`/`discard`.
5. `on_empty` (`pass | fail | skip`, default `pass`) decides what a `settle`
   does when `where` selects nothing; a "review 5, land the 3 that pass" step
   must not fail just because zero passed.
6. `on_abort` at the workflow level is `halt | release_all | discard_all`
   (default `halt`, which is loud). Orphan worktrees are still covered by
   `yaah shepherd prune`, but the validator should make leaks rare.

#### Settlement vs. continuation

The `proposal` tool also accepts `continue` and `fork`, but they are **lifecycle
operations, not settlement verbs**. The distinction is not cosmetic:

| Verb | Class | Effect on the original | Creates |
| --- | --- | --- | --- |
| `select`, `apply`, `release`, `discard` | settlement / disposition | consumed once, terminal | nothing |
| `continue` | derivation | discarded (settled `discard` internally) | a new proposal that **supersedes** it |
| `fork` | derivation | **stays open** and still needs a disposition | a new sibling proposal derived from it |

`continue` is a compound "discard + re-run seeded from the sealed workspace and
captured conversation"; `fork` is "keep + re-run". Both are worktree-lane only
(a container proposal's world ended at seal), both require a captured
transcript, and both are serialized per parent so two re-runs cannot race.
They appear in a proposal's `next` list only when eligible
(`nextVerbsFor`), which is why `next` is an eligibility list rather than the
settlement vocabulary.

Consequently, the DSL keeps `settle` four-verb and puts the derivations on
`delegate`: `continue_from` supersedes the source, `fork_from` leaves it open.
A `fork_from` does **not** discharge the source proposal, so the validator must
still require a later `settle` for it.

### 5.7 Failure semantics

Per node:

| `on_failure` | Behavior |
| --- | --- |
| `abort` (default for proposal nodes) | stop the workflow; run `on_abort` |
| `continue` | mark the node failed and continue; downstream `when` can branch |
| `skip` | mark skipped; downstream nodes depending on it are skipped |

Per fan-out item: `abort | continue | collect_failures`. `continue` is how
"review 5, land the 3 that pass" is expressed without a special verb.

### 5.8 Budgets

Workflow-level:

```yaml
budgets:
  max_dispatches: 24
  max_wall_clock_seconds: 3600
  max_tokens: 2000000
  max_concurrent_dispatches: 4
```

The validator computes a worst-case dispatch count from the graph (`fanout`
max × enclosing `loop` max × `call` expansion) and rejects the spec if it
exceeds `max_dispatches` unless `--allow-unbounded` is passed. Token budgets
are estimates (role model × turn cap) and are enforced at runtime, not
statically.

---

## 6. YAML surface

### 6.1 Minimal document

```yaml
apiVersion: yaah.workflow/v1alpha1
kind: Workflow
metadata:
  name: feature-review
  version: 1.0.0

spec:
  params:
    task: { type: string, required: true }

  budgets:
    max_dispatches: 16
    max_wall_clock_seconds: 1800

  steps:
    - id: plan
      kind: delegate
      role: planner
      output: Plan
      prompt: |
        Break "{{ params.task }}" into independent workstreams.
        Return JSON: {"workstreams":[{"id","brief"}]}.
```

### 6.2 Full example: fan-out, review, settle, repair fan-out

```yaml
apiVersion: yaah.workflow/v1alpha1
kind: Workflow
metadata:
  name: feature-review
  version: 1.0.0

spec:
  params:
    task: { type: string, required: true }
    max_workstreams: { type: integer, default: 4 }

  defaults:
    lane: proposal
    grants:
      - { path: src, access: readwrite }
      - { path: docs, access: readonly }
    network: deny_all

  budgets:
    max_dispatches: 24
    max_wall_clock_seconds: 3600
    max_concurrent_dispatches: 4

  steps:
    - id: plan
      kind: delegate
      role: planner
      lane: report
      output: Plan
      prompt: |
        Break "{{ params.task }}" into at most {{ params.max_workstreams }}
        independent workstreams that touch disjoint paths.
        Return JSON: {"workstreams":[{"id","brief","paths"}]}.

    - id: build
      kind: fanout
      over: { from: steps.plan.output.workstreams }
      as: ws
      max_parallel: 4
      join: all
      on_item_failure: continue
      body:
        kind: delegate
        role: developer
        prompt: |
          Implement this workstream: {{ ws.brief }}
          Only touch these paths: {{ ws.paths }}.
        output: proposal
      collect: proposals

    - id: review
      kind: fanout
      over: { from: steps.build.collect }
      as: proposal
      max_parallel: 4
      join: all
      on_item_failure: continue
      body:
        kind: delegate
        role: reviewer
        lane: report
        prompt: |
          Review this proposal for correctness. Proposal id: {{ proposal.id }}
          Diff: {{ proposal.diff }}
          Return JSON: {"verdict":"pass"|"fail","reasons":[]}.
        output: Verdict
      collect: reviews

    - id: land
      kind: settle
      proposals: { from: steps.build.collect }
      where: { from: steps.reviews.collect, field: verdict, equals: pass }
      action: apply
      policy: path_disjoint
      order: smallest_diff_first
      on_conflict: stop

    - id: repair
      kind: fanout
      when: { from: steps.land.output, field: rejected, not_empty: true }
      over: { from: steps.land.output.rejected }
      as: rejected
      max_parallel: 2
      join: all
      on_item_failure: continue
      body:
        kind: delegate
        role: developer
        continue_from: "{{ rejected.proposal_id }}"
        prompt: |
          Address the review feedback and re-seal this proposal.
        output: proposal
      collect: repairs

    - id: land_repairs
      kind: settle
      proposals: { from: steps.repair.collect }
      action: apply
      policy: path_disjoint
      on_conflict: stop

    - id: report
      kind: emit
      value: |
        Landed {{ steps.land.output.settled }} proposal(s).
        Repairs landed: {{ steps.land_repairs.output.settled }}.
```

### 6.3 Speculative variants example

```yaml
- id: candidates
  kind: fanout
  over: [conservative, idiomatic, minimal]
  as: style
  max_parallel: 3
  body:
    kind: delegate
    role: developer
    prompt: |
      Implement the task in a {{ style }} style: {{ params.task }}
    output: proposal
  collect: candidates

- id: pick
  kind: select
  candidates: { from: steps.candidates.collect }
  policy: judge
  judge:
    role: reviewer
    prompt: |
      Pick the best proposal. Consider correctness first, then diff size.
      Candidates: {{ steps.candidates.collect }}
  output: winner

- id: land_one
  kind: settle
  proposals: { from: steps.pick.output.winner, as_list: true }
  action: apply
  policy: path_disjoint
```

`select` is a thin node over `delegate` + a structured verdict; it exists so
speculation is expressed once instead of in every workflow.

---

## 7. Validation and static analysis

`yaah workflow validate` must fail closed on all of the following. Each rule
gets a stable error code (`WF001`…) so the model-repair loop can target it.

### 7.1 Schema and structure

| Code | Rule |
| --- | --- |
| `WF001` | Unknown fields rejected (strict decode) |
| `WF002` | Unique node ids; `needs` refers to existing nodes |
| `WF003` | No cycles in `needs`; `call` depth bounded |
| `WF004` | Every node reachable from the entry set |
| `WF005` | `output` schemas are valid JSON Schema; names unique |

### 7.2 Values and templates

| Code | Rule |
| --- | --- |
| `WF010` | Every `{{ ... }}` reference resolves in scope |
| `WF011` | Type compatibility between producer and consumer (e.g. `fanout.over` needs a list) |
| `WF012` | `where` selectors reference fields that exist in the declared schema |
| `WF013` | No template recursion, no function calls |

### 7.3 Proposals and settlement

| Code | Rule |
| --- | --- |
| `WF020` | `settle.proposals` refers to a proposal-lane collection |
| `WF021` | Every proposal is settled exactly once on every reachable path |
| `WF022` | `settle.action: select/apply` targets are not `release`/`discard`ed earlier |
| `WF023` | Parallel `apply` branches declare disjoint path intent, or are serialized by `needs` |
| `WF024` | `on_conflict: skip` has a later `settle` that catches the skipped set |
| `WF025` | `on_abort` is explicit when any proposal may remain open |
| `WF026` | `settle` on a possibly-empty set declares `on_empty` |
| `WF027` | `continue_from`/`fork_from` target a worktree proposal from the same role and lane; `fork_from` leaves the source settle-able |
| `WF028` | `select` candidates are non-empty; the policy's required judge is declared |
| `WF029` | Dynamic `fanout.over` declares `max_parallel`, and the enclosing budget covers it |

### 7.4 Authority and cost

| Code | Rule |
| --- | --- |
| `WF030` | Role exists and is delegatable; not a primary-only persona |
| `WF031` | Grants are world-relative, disjoint, and within the declared world |
| `WF032` | `network`/`network_hosts` consistent; no broker without hosts |
| `WF033` | Worst-case dispatch count ≤ `budgets.max_dispatches` |
| `WF034` | Every `loop` has `max_iterations`; every `fanout` has `max_parallel` |
| `WF035` | Prompt size and output limits within configured caps |
| `WF036` | No inline secrets; `secrets:` references only |

### 7.5 `yaah workflow explain`

The killer command. Prints:

- the resolved DAG (Mermaid and plain text),
- per-node role, lane, world, grants, network,
- maximum fan-out and worst-case dispatch/turn bound,
- the union of filesystem and network authority across all branches,
- every proposal-producing node and its guaranteed disposition,
- validation warnings.

This is the feature no other YAML DSL ships, and it is the argument for a DSL
over code.

---

## 8. Runtime architecture

### 8.1 Packages

```
internal/workflow/
├── spec/        types, JSON schema, strict decoder, version migration
├── validate/    static rules WF001–WF036
├── ir/          resolved graph, selectors, template binding
├── exec/        scheduler, node handlers, budget accounting, resume state
├── bind/        adapters to DelegateTool, ProposalTool, WaitFor, Jobs
└── trace/       workflow trace records
cmd/yaah/
└── workflow.go  validate | explain | run | resume | propose | render
```

Keep the executor host-side. It uses the same tools the orchestrator uses; it
is not a sub-agent, and it does not get retained as a root run (repo decision D8).

### 8.2 Scheduler

- Topological order over `needs`; ready nodes run concurrently up to
  `max_concurrent_dispatches`.
- A `fanout` body runs with its own semaphore capped by `max_parallel`, nested
  under the global cap.
- A `loop` runs its body once per iteration; `until` is evaluated after each
  iteration.
- A `delegate` maps to the existing dispatch path (`DelegateTool` /
  `internal/dispatch`, whichever seam the runner already exposes), not a new
  execution engine. A proposal-lane dispatch returns the existing envelope,
  which the IR stores as a `Proposal` value.
- A `settle` maps to `ProposalTool` (`select|apply|release|discard`), one call
  per proposal, in the declared order. No new settlement code.
- Background mode is an implementation detail: the executor may use
  `background: true` + `subagent_jobs.wait` to keep long fan-outs off the
  model turn, but the spec never mentions jobs.

### 8.3 State and resume

Persist workflow state in SQLite (same store family as sessions) keyed by
`run_id`:

```
workflow_run(run_id, spec_hash, status, started_at, ended_at, params_json)
workflow_node(run_id, node_id, attempt, status, started_at, ended_at, output_ref)
workflow_dispatch(run_id, node_id, item_key, scope_id, proposal_id, status)
workflow_settle(run_id, proposal_id, action, result)
```

Resume skips completed nodes and rehydrates outputs from the trace. A `--replay`
mode substitutes recorded agent outputs instead of calling models; this makes
the deterministic skeleton testable and gives you a reproducible demo.

### 8.4 Trace records

Emit kernel records (or trace spans, matching the existing middleware style):

```
workflow.started        {run_id, spec_hash, workflow, params}
workflow.node.started   {run_id, node_id, attempt, kind}
workflow.node.finished  {run_id, node_id, status, output_digest}
workflow.dispatch       {run_id, node_id, item_key, scope_id, lane}
workflow.proposal       {run_id, node_id, proposal_id, status}
workflow.settle         {run_id, proposal_id, action, result}
workflow.ended          {run_id, status, proposals_open}
```

The spec hash is the content address of the canonical JSON IR. A run is
reproducible from `spec_hash` + params + recorded agent outputs.

### 8.5 Integration points

| Existing surface | Use |
| --- | --- |
| `delegate` | node execution; roles, lane, provider/model, budgets |
| retained lane + `proposal` | proposal creation and settlement |
| `background` + `subagent_jobs` | long fan-outs without blocking a model turn |
| `wait_for`/`watch` | optional `human` and external-signal nodes (P2) |
| role files (`.agents/roles/*.md`) | role resolution and per-role limits |
| `agents.subagent.*` config | concurrency, mode, world, grants, egress defaults |
| trace store | audit, replay, `explain --from-trace` |
| `yaah shepherd prune` | orphan cleanup safety net |
| bench harness | acceptance evidence |

---

## 9. Security model

A workflow file is untrusted input, especially once third parties can author
one ("outside programmability").

1. **Parse limits.** Max file size, max node count, max prompt bytes, max
   template depth. Reject unknown fields.
2. **Allowlists.** Roles, subworkflows (`call`), selector functions, and
   aggregate strategies are registries, not free-form names.
3. **Least privilege by default.** Proposal lane, `network: deny_all`, and the
   narrowest grants that satisfy the declared paths. A spec may narrow but
   never widen the host's configured maximum.
4. **Cost bounds.** `max_dispatches`, `max_concurrent_dispatches`,
   `max_wall_clock_seconds`, per-node timeouts, loop caps. A rejected spec
   cannot launch a cost bomb.
5. **No ambient authority.** The spec cannot read files, run shell, or call
   arbitrary tools directly; it can only dispatch declared roles.
6. **Prompt injection.** Spec text is data, but it becomes a prompt. The
   defense is the same as any sub-agent dispatch: dedicated roles, tool belts,
   jails, and egress postures. Never interpolate untrusted report text into a
   shell command.
7. **Settlement safety.** No silent application. `apply` is path-disjoint and
   fails closed on conflict; `select` fails if the basis moved. `on_conflict`
   cannot be omitted.
8. **Audit.** `explain` before run, trace after run. A workflow that cannot be
   explained should not run.
9. **Self-modification gate.** When an orchestrator emits a spec, it goes
   through validate → explain → dry-run → approval (or policy) → run. The model
   proposes; the runtime disposes.

---

## 10. Delivery plan

Effort is rough, for one engineer. Phases are ordered by risk retirement, not
by demo value.

### P0 — Freeze the IR and validator (1–2 weeks)

**Deliverables**

- `internal/workflow/spec` types + JSON Schema + strict decoder.
- `internal/workflow/validate` with WF001–WF025 (structural, values,
  proposals). WF030+ can land with P1.
- Three golden specs: sequential pipeline, fan-out + settle, bounded repair
  loop.
- Decisions WD1–WD10 accepted (see §14).
- Mapping table from the ten `bench/orchestration-tasks/orch-*` tasks to IR
  constructs.

**Exit criteria**

- `yaah workflow validate` accepts the goldens and rejects 20+ adversarial
  fixtures with stable codes.
- The IR can express every `orch-*` bench task's orchestration shape (not
  necessarily run it yet).

### P1 — MVP interpreter (3–5 weeks)

**Deliverables**

- `exec` scheduler for `delegate`, `fanout` (`join: all`), `settle`, `loop`,
  `branch`, `aggregate`, `emit`.
- Report lane and proposal lane; `proposal` settlement through the existing
  tool.
- `yaah workflow run`, `validate`, `explain` (DAG + budget + authority).
- Trace records and `workflow_run` state in SQLite.
- Budget accounting and per-node timeouts.
- One end-to-end bench task (`orch-fanout` or `orch-settle-apply`) run from a
  spec and verified by the existing three-layer `orch_assert.py` checks.

**Exit criteria**

- A spec run produces the same trace shape as the hand-driven orchestrator for
  the same task.
- A killed process resumes without duplicating dispatches or settlements.
- `explain` correctly reports max fan-out and authority union for the goldens.

### P2 — Hardening and semantics (4–6 weeks)

**Deliverables**

- `join: any | quorum` with cancellation tests; `select` policy (`judge`,
  `first_success`, `smallest_diff`).
- Workflow-level `on_abort` (`halt | release_all | discard_all`).
- `gate` (deterministic assert + judge role), `human` approval, `call`
  subworkflows with depth limits.
- Full WF026–WF036 static analysis, including linearity on all paths.
- Resume/replay (`--replay`), trace-backed `explain --from-trace`.
- Security test suite: oversized specs, role escalation, grant widening, cost
  bombs, path traversal in `call`, prompt-size bombs.
- All `orch-*` tasks expressible; at least five pass from specs, including
  `orch-settle-discard`, `orch-guard`, `orch-quality-gate`,
  `orch-escalation`, and `orch-recovery`.
- Documentation: `docs/workflows.md` user guide + reference.

**Exit criteria**

- No proposal can be left open by a completed run unless the spec explicitly
  chose that policy.
- Fuzz/property tests show the scheduler never exceeds declared concurrency or
  dispatch bounds.
- A resume after a crash mid-fan-out settles every created proposal exactly
  once.

### P3 — Model authoring (3–4 weeks)

**Deliverables**

- `yaah workflow propose "<intent>"` — an orchestrator dispatch that emits a
  spec, validated against the schema with a bounded repair loop (max 3).
- `workflow_run` tool (opt-in) so the orchestrator can execute a spec it
  authored, gated by approval policy.
- `yaah workflow render` — Mermaid/HTML graph from the IR (read-only).
- Metrics: invalid-spec rate, repair attempts, spec-vs-actual trace diff.

**Exit criteria**

- A model-authored spec for a nontrivial task validates on the first or second
  attempt more often than not, and a dry-run explains it correctly.
- Self-modification is impossible without passing validate → explain → policy.

### P4 — Optional (not scheduled)

Visual editor, spec registry/sharing, cross-harness export, richer effect
queries. Only after P3 demonstrates real use.

### Total

Roughly **3–4 months** for P0–P3 with one engineer, parallelizable across the
validator, executor, and bench tracks. The MVP (P0–P1) is about **5–7 weeks**
and is the right point to decide whether the idea has pull.

### First five PRs

Each PR should be independently reviewable and land green.

0. **Prerequisite: extract the typed core.** Move the bodies of
   `DelegateTool.Execute` and `ProposalTool.Execute` into `dispatch.Run` and
   `dispatch.Settle`; leave the tools as thin adapters. This is not DSL code,
   it can land on its own, and it is what keeps the executor from duplicating
   tool plumbing.

1. **`workflow/spec` + schema + fixtures.** Types, strict decoder, canonical
   JSON, spec hash, three valid + ten invalid fixtures. No runtime.
2. **`workflow/validate` structural and value rules.** `WF001–WF005`,
   `WF010–WF013`, with stable codes and table-driven tests.
3. **`yaah workflow validate` + `explain` skeleton.** CLI wiring, DAG
   rendering, budget/authority placeholders; explain works on the goldens.
4. **Sequential interpreter.** `delegate` (report lane) and `emit`, one golden
   spec end-to-end against the deterministic provider.
5. **Fan-out + settle + resume.** `fanout` with `join: all`, proposal lane,
   `settle`, SQLite state, trace records; `orch-fanout` or `orch-settle-apply`
   passes from a spec.

Proposal linearity (`WF020–WF029`) can land between 4 and 5 if the validator
work is parallelized; do not start `any`/`quorum` until 5 is green.

### PR ladder

A reviewable-increment estimate, not an effort estimate. Each row is one PR
that lands green with its tests.

| Phase | PRs | Representative increments | Exit artifact |
| --- | --- | --- | --- |
| **P0** | 7 | spec + schema + hash; IR + selectors/templates; validator structure; validator values; validator proposal linearity; validator authority/cost; `explain` skeleton | `validate` + `explain` on the goldens |
| **P1** | 8 | scheduler core; delegate/emit; proposal/settle; fan-out; loop/branch/aggregate; SQLite resume; trace records; bench e2e | one `orch-*` task runs from a spec |
| **P2** | 9 | any/quorum + cancellation; `select`; `gate`; `human`; `call`; `on_abort`; replay/idempotency; security suite; five bench tasks + docs | five `orch-*` tasks pass |
| **P3** | 3 | `workflow propose` + repair loop; `workflow_run` + approval; `render` + gallery/metrics | a model-authored spec runs |

**Total:** ~27 PRs for P0–P3, plus the PR 0 refactor; ~15 for the MVP
(P0+P1), ~16 with PR 0. The first five are the spine and should not be
parallelized past. In a single-author repo, merging the four validator PRs
into two and the node handlers into four brings the total to roughly **20**.

The four riskiest PRs — proposal linearity validation, the `settle` binding,
`any`/`quorum` cancellation, and resume idempotency — deserve dedicated review
time and should not be bundled with unrelated changes.

---

## 11. Testing strategy

| Layer | Tests |
| --- | --- |
| Spec/decoder | strict-decode fixtures, unknown fields, version migration, size limits |
| Validator | table-driven cases per error code; property test: every accepted spec has a settlement proof |
| IR/selectors | golden resolved graphs; template scope errors; selector type checks |
| Scheduler | deterministic fake executor; ordering, concurrency caps, failure paths, cancellation |
| Settlement | mock proposal registry; double-settle refused; conflict paths; `on_abort` dispositions |
| Resume | kill after each node; assert no duplicate dispatch, no duplicate settle |
| Trace | record presence, spec hash stability, replay equivalence |
| Security | escalation, grant widening, cost bomb, prompt bomb, path traversal |
| End-to-end | the `bench/orchestration-tasks` suite with the existing `orch_assert.py` three layers |

Use the deterministic provider for unit tests. Real-model runs are manual, like
the existing bench arms, and never CI gates.

---

## 12. Bench mapping

| Bench task | DSL constructs exercised |
| --- | --- |
| `orch-fanout` | `fanout`, `delegate` (report), `aggregate` |
| `orch-report-fanout` | `fanout` report lane, `aggregate` |
| `orch-settle-apply` | `delegate` proposal, `settle.apply` |
| `orch-settle-discard` | `settle.discard`, `on_abort: discard_all` |
| `orch-guard` | `gate` (judge), `on_failure` |
| `orch-quality-gate` | `gate` + `branch` + `retry` |
| `orch-escalation` | `branch`, `on_failure: continue`, `human` (P2) |
| `orch-recovery` | resume state, `settle` after restart |
| `orch-confinement` | per-node `grants`, `network`, `world` |
| `orch-compact` | `delegate` + compaction (not a DSL concern) |

The first acceptance milestone is **one** task passing from a spec. The second
is the whole suite being expressible. The third is five tasks passing.

---

## 13. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| DSL becomes a second source of truth | High | High | One IR for both paths; DSL optional; `workflow_run` is a tool, not a replacement |
| Expression language creep | High | High | Hard rule: no expressions; named Go functions only; reject PRs that add operators |
| Join/race semantics bugs | Medium | High | Ship `all` first; add `any`/`quorum` with cancellation tests; property tests |
| Proposal leaks / double settlement | Medium | High | Linearity validator + runtime guard + `on_abort`; assert zero open proposals at end |
| LLM-generated invalid specs | High | Medium | Strict schema, stable error codes, bounded repair loop, dry-run |
| Overfitting to bench tasks | Medium | Medium | Also land one real workflow (repo-wide refactor or dependency upgrade) |
| Versioning churn | Medium | Medium | `apiVersion`, strict decode, explicit migration, deprecation window |
| Adoption friction | Medium | High | `explain` + templates + orchestrator-authored specs; make the first workflow a one-liner |
| Security regression | Low–Medium | High | Untrusted-input test suite; no widening; fail closed; audit trail |

---

## 14. Decisions to accept (WD1–WD10)

| ID | Decision | Rationale |
| --- | --- | --- |
| WD1 | Fixed node vocabulary, no user-defined functions | Scope control; analyzability |
| WD2 | No expression language; structured selectors + named Go functions | Avoid becoming a bad programming language |
| WD3 | Bounded loops only; no arbitrary cycles | Termination and cost bounds |
| WD4 | Proposals are linear values; settlement exhaustion is a validation rule | Safety; matches consume-once semantics |
| WD5 | `settle` delegates to the existing `proposal` tool | No duplicate settlement logic across lanes |
| WD6 | Executor is host-side and not retained | Matches repo decision D8 (orchestrator is the driver) |
| WD7 | One IR, YAML + JSON front-ends, spec hashed into the trace | Reproducibility and model authorability |
| WD8 | P1 ships `join: all` only | Retire the hardest semantics last |
| WD9 | DSL is opt-in; the orchestrator path is unchanged | Avoid a second source of truth |
| WD10 | `explain` is a first-class command, not a debug flag | Static analysis is the reason to have a DSL |

---

## 15. Open questions

1. **Executor placement.** Host-side loop (recommended) vs. a dedicated
   `workflow-runner` sub-agent. Host-side keeps control flow out of a model and
   aligns with repo decision D8, but it needs a clean tool interface.
2. **Root retention.** Should a workflow run ever be retained as a proposal?
   Recommendation: no for v1; revisit if workflows start mutating the host
   directly.
3. **`select` cardinality.** Should "orchestrator chooses N sub-agents" be a
   data-driven `fanout over plan` (recommended) or a `select` node that returns
   a count? The former is analyzable; the latter is more flexible.
4. **Property intent vs. proof.** `WF023` can only check declared path intent,
   not actual diff disjointness. Do we require a `paths` declaration on every
   proposal node, or infer it from the grant roots? Recommendation: require it
   for parallel `apply`, infer otherwise.
5. **Human nodes.** Use the existing `question` tool or a dedicated `human`
   node with `wait_for`? Recommendation: dedicated node, backed by `question`,
   so the trace is uniform.
6. **Subworkflow registry.** File-path `call` (`./sub.yaml`) is convenient but
   a path-traversal risk. Recommendation: named registry + explicit
   `--workflow-root`, with path containment checks.
7. **YAML vs. a small indentation language.** YAML is fine for the IR and for
   model output; a human-facing surface can come later. Do not build both now.
8. **Versioning policy.** How long is `v1alpha1` supported after `v1beta1`?
   Recommendation: one minor release overlap with a migration command.

---

## 16. Appendix A — IR sketch (JSON)

```json
{
  "apiVersion": "yaah.workflow/v1alpha1",
  "kind": "Workflow",
  "metadata": { "name": "feature-review", "version": "1.0.0" },
  "spec": {
    "params": {
      "task": { "type": "string", "required": true }
    },
    "defaults": {
      "lane": "proposal",
      "network": "deny_all",
      "grants": [{ "path": "src", "access": "readwrite" }]
    },
    "budgets": {
      "max_dispatches": 24,
      "max_wall_clock_seconds": 3600,
      "max_concurrent_dispatches": 4
    },
    "steps": [
      {
        "id": "plan",
        "kind": "delegate",
        "role": "planner",
        "lane": "report",
        "output": { "name": "Plan", "schema": "schemas/plan.json" },
        "prompt": "Break \"{{ params.task }}\" into workstreams."
      },
      {
        "id": "build",
        "kind": "fanout",
        "over": { "from": "steps.plan.output.workstreams" },
        "as": "ws",
        "max_parallel": 4,
        "join": "all",
        "on_item_failure": "continue",
        "body": {
          "kind": "delegate",
          "role": "developer",
          "prompt": "Implement {{ ws.brief }}.",
          "output": { "name": "proposal", "type": "Proposal" }
        },
        "collect": "proposals"
      },
      {
        "id": "land",
        "kind": "settle",
        "proposals": { "from": "steps.build.collect" },
        "action": "apply",
        "policy": "path_disjoint",
        "order": "smallest_diff_first",
        "on_conflict": "stop"
      }
    ]
  }
}
```

## 17. Appendix B — Glossary

- **Proposal** — a sealed, reviewable unit of sub-agent work (worktree or
  container delta) with exactly one settlement.
- **Settlement** — `select`, `apply`, `release`, or `discard`; consume-once.
- **Lane** — `report` (text back, nothing sealed) or `proposal` (sealed delta).
- **World** — where a dispatch runs: worktree, container, or shared host.
- **Skeleton** — the deterministic part of a workflow: sequencing, fan-out,
  joins, branches, loops, settlement.
- **Leaf** — an agentic node: `delegate`, `judge`, `select`.
- **Spec hash** — canonical JSON hash of the IR, recorded in the trace.
- **Disposition** — the guaranteed fate of every proposal on every path.

## 18. References

- `yaah/docs/sub-agents.md` — dispatch lanes, retention, budgets, roles.
- `yaah/docs/shepherd-parity.md` — shipped vs. open parity rows.
- `yaah/docs/configuration.md` — `agents.subagent.*`, grants, egress, worlds.
- `yaah/docs/features.md` — orchestration tool surface.
- `yaah-shepherd-gap-report.md` — Shepherd/Go-port/yaah comparison and
  `yaah-hxk3.5`.
- Shepherd Python combinators —
  `shepherd/packages/runtime/src/shepherd_runtime/combinators/`.
- AgentSPEX — arXiv:2604.13346, `github.com/ScaleML/AgentSPEX`.
- Microsoft Agent Framework declarative workflows —
  `learn.microsoft.com/agent-framework/user-guide/workflows/declarative-workflows`.
- AWS Step Functions Amazon States Language; CNCF Serverless Workflow.
- ADL — `adl-spec.org`.
