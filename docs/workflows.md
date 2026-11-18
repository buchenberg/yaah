# Workflows

A workflow is an orchestration written down: a YAML or JSON document
(`yaah.workflow/v1`) that names what should be dispatched, in what order,
with what prompts, budgets, and settlement verbs. The document is the
artifact a team reviews; `yaah workflow validate` checks it, `explain`
diagrams and prices it, and `run` executes it — deterministically, with
every dispatch, proposal, and settlement recorded.

This file is the user guide and reference; the design source is
[workflow-dsl-plan.md](workflow-dsl-plan.md), whose §9 the
[security model](#security-model-and-threat-surface) below states
operationally.

## The idea in one example

```yaml
apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: audit-one-module
spec:
  budgets:
    max_dispatches: 4
    max_concurrent_dispatches: 2
  steps:
    - id: audit
      kind: delegate
      role: developer
      lane: report
      prompt: |
        Audit inventory/a_models.py and report its public functions.
    - id: find
      kind: emit
      needs: [audit]
      value: "{{ steps.audit.output }}"
      path: audits/a_models.md
```

Four facts are the whole surface:

- **The document decides.** Which nodes exist, what each dispatch says, how
  many may run at once, and what gets written — all from the spec. The
  document decides the *orchestration*; host configuration decides the
  *capabilities* (roles, network, worlds, model).
- **`needs` is the graph.** Independent steps run concurrently up to the
  document's cap; `needs: [a, b]` runs after both.
- **Every proposal gets exactly one disposition.** A `lane: proposal`
  dispatch seals a retained proposal; a `settle` node applies, selects,
  releases, or discards it. A run that ends with an open proposal fails by
  rule, not by accident.
- **Fail closed, everywhere.** A document that does not validate does not
  run. Unknown fields, oversized prompts (64 KiB), unbudgeted loops, roles
  the host does not delegate to — all refused with a stable `WFxxx` code and
  the node path that caused it.

## Document anatomy

```yaml
apiVersion: yaah.workflow/v1   # required, only v1
kind: Workflow                 # required
metadata:
  name: my-workflow            # required; stable identity
  version: 1.0.0               # optional
spec:
  params: ...                  # declared parameters, with types
  defaults: ...                # lane/network/grants inherited by dispatches
  budgets: ...                 # ceilings the scheduler enforces at runtime
  steps: ...                   # the node list; ids required on top level
```

- **`params`**: declared up front; a spec may not reference an undeclared
  param. `run --param name=value` and `--param name=json` bind them.
- **`defaults`**: `lane`, `network`, `grants` — inherited by every
  `delegate`, and narrowing is the only direction: a step may tighten its
  host-given ceiling, never raise it.
- **`budgets`**: `max_dispatches` (the scheduler stops the run when crossed),
  `max_concurrent_dispatches`, `max_wall_clock_seconds`, `max_tokens`. These
  are runtime-enforced, not advisory: a run that lies about its budget is
  stopped at the budget, not the workload (pinned by the bench cost-bomb
  test).

## Node kinds

| Kind | What it does | Key fields |
|---|---|---|
| `delegate` | Dispatch one sub-agent on a lane | `role`, `prompt`, `lane`, `grants`, `network`, `world`, `max_turns`, `timeout_seconds`, `output` |
| `fanout` | One body per item of a list | `over` (literal list or selector), `as` (item binding), `max_parallel`, `collect`, `join`, `quorum`, `on_item_failure` |
| `loop` | Repeat a body until a terminator | `max_iterations` (required), `until`, `collect`, `continue_from`, `continue_from`-shaped proposal threading |
| `branch` | Route on a selector | `on` (selector), `cases` with `when`, `default` |
| `gate` | Deterministic check | `assert` (registered asserts), or `judge` (a dispatched verdict), `on_pass`/`on_fail` jump labels |
| `select` | Choose one candidate proposal | `candidates`, `judge` or `policy: first_success | smallest_diff`, `judge` dispatches with the fixed verdict shape |
| `settle` | Dispose proposals: one verb per handle | `proposals`, `where`, `action: select\|apply\|release\|discard`, `policy`, `on_conflict`, `on_empty` |
| `aggregate` | Combine outputs | `inputs` (selector list), `strategy: concat\|json_merge\|summarize` |
| `call` | Run a registered subworkflow | `workflow` (name under `--workflow-root`), `params`, `max_depth` |
| `emit` | Write a value to an artifact | `value`, `path` |
| `human` | Wait for a person | `on_timeout` |

Notes worth knowing per kind:

- **`delegate`** with `lane: proposal` seals the sub-agent's work as a
  retained proposal (a real worktree). `steps.<id>.output.proposal_id` is
  the handle a settle node disposes. With `lane: report` (or the default),
  the value is the report text; a report that carries a JSON object exposes
  its fields as `steps.<id>.output.<field>` (prose-tolerant: prose around the
  object is fine).
- **`fanout`/`loop`** bodies are node lists; each item/iteration mints body
  instances (`steps.<id>.items[2].body[0]`, `steps.<id>.iters[1].body[0]`).
  The `collect` accumulates one entry per succeeded item/iteration — the
  body's last node's recorded value — and is readable both by selectors
  (`from: steps.<id>.collect`) and templates (`{{ steps.<id>.collect }}`).
- **`settle`** requires its `proposals` and `where` to read the same source;
  a `where` may filter on the handle identity (`proposal_id`, `producer`) or
  on the other fields the source element carried (the gate pattern: the check
  report named the proposal *and* the verdict, so `where: {field: verdict,
  equals: pass}` selects it). Cross-step joins are refused by name.

## Values and templates

`{{ ... }}` segments bind:

- `{{ params.<name> }}` — declared parameters.
- `{{ steps.<id>.output }}` and `{{ steps.<id>.output.<field> }}` — a
  succeeded step's recorded value; structured reports decode on demand.
- `{{ steps.<id>.collect }}` — a container's accumulation.
- `{{ <as-name>.<field> }}` — a fan-out item binding (`as: dep` → `{{ dep.version }}`).
- `{{ index }}` — the item/iteration counter, inside bodies only.
- `{{ run.id }}`, `{{ run.attempt }}` — the run's identity and this attempt.

Selectors (`from`, `over`, `until`, `where`, `when`) are the structured
form: `from: steps.a.output.list` plus at most one predicate
(`equals`, `not_equals`, `in`, `not_empty`, `exists`).

## Validation and the WF codes

`yaah workflow validate` (and `run`, before anything dispatches) decodes,
resolves, and runs five families:

| Range | Family |
|---|---|
| WF001–WF005 | structure: does the document describe a graph at all |
| WF010–WF014 | values: does every value resolve, in scope, in shape |
| WF020–WF029 | linearity: is every proposal settled exactly once |
| WF030–WF037 | authority and cost: what may it touch and spend, against *this* host |

Findings print as `code path message`, ordered by code then path. Severity
is a contract:

- **Errors** (WF001–WF029 mostly) mean the document is invalid — it will
  never run.
- **Warnings** (WF030, WF031, WF032, WF033, WF035, WF036) are
  host-relative facts: roles, worlds, network ceilings, budget ceilings. A
  different host might accept them. `--strict` promotes warnings to
  refusals — CI and self-modification flows should use it.

## Commands

```text
yaah workflow validate <spec> [--param n=v]... [--strict] [--json] [--workflow-root <dir>]
yaah workflow explain   [<spec>] [--param n=v]... [--json] [--workflow-root <dir>]
                                 [--from-trace <run_id> --state <db>]
yaah workflow run       <spec>   [... validate flags ...]
                                 [--run-id <id>] [--state <db>] [--artifact-root <dir>]
                                 [--replay <run_id>]
yaah workflow resume    <run_id> [--state <db>] [--artifact-root <dir>] [--spec <path>]
```

- **`validate`**: the gate. Report (JSON) kind `yaah.workflow.validate.v1`;
  status `valid`/`invalid`/`unresolved` — a document that never became a
  graph reports one WF001 and no other findings can be trusted.
- **`explain`**: the DAG, the authority union (every dispatch with its
  grants and world), the worst-case cost estimate, and the settlement
  dispositions — before anything runs. With `--from-trace`, it replays a
  finished run's own record instead of the plan and reports divergences
  between the document and what actually happened.
- **`run`**: validate-and-execute. `--state` makes the run durable and
  gives `explain --from-trace`, `resume`, and `--replay` a record to read.
  `--artifact-root` is where `emit`'s `path:` writes. `--replay <run_id>`
  re-derives the same orchestration doors from the run's checkpoint with the
  runner never called (see [Replay](#durability-recovery-and-replay)).
- **`resume`**: continue a durable run from its committed state — every
  node that terminated stays terminated; only work the record does not show
  is dispatched. A finished run is final: resume refuses it (`already
  finished`), because its disposition is a fact, not a todo.

Reports: `run --json` emits `yaah.workflow.run.v1` (status, dispatch tally,
per-node states, outputs, artifacts, warnings); `explain --json` emits the
match shape; `explain --from-trace --json` emits `yaah.workflow.trace.v1`
(the typed record set, proposals, settlements, divergences).

## Durability, recovery, and replay

- **Durable runs.** `run --state <db>` commits node state per event, so a
  killed process leaves a record that `resume` continues: no completed node
  is re-dispatched, and no proposal is ever settled twice.
- **Adoption** of a crashed session's orphaned worktree is a host command,
  not a graph edge: `yaah shepherd` scans the durable trace and reports the
  adopted worktrees. The workflow surface's part is reporting — the
  `orch-recovery` bench spec delegates the scan and emits `recovery.txt`.
- **Replay** (`run --replay <run_id>`): re-run a completed run's orchestration
  from its checkpoint with the same doors but a live runner — the original
  run's own state is untouched. Replay answers "what would this run do now
  with this model", with zero double-settlement by construction.

Crash/resume determinism is proven at the scheduler core with seeded kill
schedules (`internal/workflow/exec/resume_property_test.go`): a converged
resume equals the baseline run, every proposal settles at most once across
lives, attempt ordinals never repeat.

## Security model and threat surface

A workflow file is untrusted input. This is the threat model the adversarial
security suite (`yaah-0u0v.26`) enforces; §9 of
[workflow-dsl-plan.md](workflow-dsl-plan.md) is the design source. The
inventory below stands alone — each row names the threat, the rule that
refuses it, and the test that pins it.

### Positions

- **The spec proposes; the runtime disposes.** A document is a reviewed
  artifact. It cannot read files, run shell, or call arbitrary tools.
  Its only verbs are dispatch declared roles, emit values, settle
  proposals, and call registry subworkflows.
- **Fail closed.** Every attack in the inventory below either never
  decodes, never resolves to a graph, or produces a diagnostic that names
  the rule (stable `WFxxx` code) and the node path it anchored to. There is
  no code path from a hostile artifact into the checklist.
- **Warnings are host-dependent facts, not verdicts.** Authority and cost
  findings (WF030–WF033, WF035) are printed as warnings by default because
  their truth depends on the host's declared ceilings; `--strict` promotes
  them to refusals. The runtime still enforces the document's *own* declared
  budgets at execution — a spec that runs despite a warning cannot exceed
  the budget it authored.

### Threat inventory

| # | Threat | Defense | Pinned by |
|---|--------|---------|-----------|
| 1 | Oversized spec, node flood, prompt-size bomb, template-depth bomb | decode limits: 1 MiB spec, 256 nodes, 64 KiB prompt, depth 8 | `spec.TestAdvSpec*` |
| 2 | Integer overflow in control fields (`max_iterations`…) | `intValue` range check: out-of-range integers are refused, never truncated | `spec.TestAdvIntegerOverflowIsRefused`, `spec.FuzzParse` |
| 3 | YAML alias-expansion bombs, structural nesting cliffs | yaml.v3's excessive-aliasing and depth limits, pinned by tests | `spec.TestAdvSpecAliasExpansionBomb`, `TestAdvSpecStructuralNestingBomb` |
| 4 | Unknown fields, duplicate keys, trailing data | strict decode: unknown field refusal at the exact key | existing decoder tests; fuzz |
| 5 | Role escalation, imposture | WF030 against the host's role registry | `validate.TestAdvRoleEscalation` |
| 6 | World and grant escapes: climbing grants, absolute paths, overlapping grants, emit artifact escapes | WF031; world-relative holds without host config | `validate.TestAdvWorldEscape`, `TestAdvGrantEscape`, `TestAdvEmitArtifactEscape` |
| 7 | Network widening past the host ceiling | WF032 | `validate.TestAdvNetworkWidening` |
| 8 | Cost bombs: budget widened, loops × loops over budget, unbounded loop/fanout, turns/tokens widened | WF033–WF035; the declared budget is enforced by the scheduler at runtime | `validate.TestAdvCostBombs`, `cmd.TestWorkflowRunRefusesCostBombAndEscapes` |
| 9 | Call traversal (`../outside`, file extensions), unregistered names | call names are a registry charset resolved under `--workflow-root` only; WF002 | `validate.TestAdvCallTraversal` |
| 10 | Invented assert vocabulary | WF037: asserts are a fixed registry | `validate.TestAdvGateInventedAssert` |
| 11 | Inline secrets in spec text | WF036 markers: private keys, bearer lines, `AKIA`, GitHub/GitLab/Slack/npm/Google tokens, `sk-` keys, JWTs | `validate.TestAdvInlineSecretsBattery` |
| 12 | Prompt injection in spec text and interpolated report values | Spec text is data; undeclared template references never resolve, so injection cannot exfiltrate out-of-scope values | `validate.TestAdvInjectionPayloadIsData` |
| 13 | Decoder/validator panics or hangs on hostile bytes | standing fuzz harnesses | `spec.FuzzParse`, `validate.FuzzValidate` |

### Self-modification gate

When an orchestrator emits a spec, it must travel the same road a human
author's does: `validate` → `explain` → dry-run → approval (or policy) →
`run`. The commands enforce the gate mechanically — `run` runs the same
analysis as `validate` and refuses before any dispatch when it fails (under
`--strict`, on the first warning). There is no API surface that executes a
document the analysis refused.

### Accepted risks

- Warning-class findings default to proceed: a host that runs a warningful
  document without `--strict` is trusting its own configuration to make the
  warning false. Declared-budget runtime enforcement is the backstop.
- Secret markers are shape heuristics: a credential outside every marker is
  not caught by WF036; the emission gate and role confidentiality are the
  remaining lines.
- yaml.v3 library limits are external: pinned by tests today, but a future
  parser swap must re-earn both pins.

## Examples

Checked in and validated by CI (`TestBenchSpecsValidate`,
`TestExampleSpecsValidate`):

- **Bench tasks** — each `bench/orchestration-tasks/orch-*/spec/workflow.yaml`
  is a task from the orchestration bench expressed as the document:
  `orch-fanout` (parallel audits), `orch-settle-discard` (discard path),
  `orch-guard` (supervised retry loop), `orch-quality-gate` (bounded repair
  through a gate), `orch-escalation` (honest refusal to fabricate),
  `orch-recovery` (report the adopted worktree). Each has a model-free CI
  run (`cmd/yaah/workflow_bench_spec_test.go`,
  `workflow_bench_run_test.go`) pinning the dispatch tallies, artifacts,
  concurrency, loop folds, and settlement dispositions from the document
  itself.
- **A real chore** — `examples/workflows/dependency-upgrade.yaml`: the
  dependency-upgrade sweep this repo would actually run (inventory →
  per-dependency retained upgrade → verify → apply what passed, discard what
  did not, report). See its header for the exact commands.

To run one:

```console
$ yaah workflow validate examples/workflows/dependency-upgrade.yaml
$ yaah workflow explain  examples/workflows/dependency-upgrade.yaml
$ yaah workflow run      examples/workflows/dependency-upgrade.yaml \
      --param deps='["gopkg.in/yaml.v3"]' --artifact-root /tmp/upgrades
```

Live, model-backed bench runs stay manual (`bench/orchestration-tasks/spec-run.sh`,
[SPEC-DRIVEN.md](../bench/orchestration-tasks/SPEC-DRIVEN.md)); CI proves
the same runs model-free with deterministic runners, so every artifact and
count above is reproducible without a provider.
