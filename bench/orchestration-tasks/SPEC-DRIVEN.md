# Spec-driven bench runs (`yaah-0u0v.18`)

The DSL is not real until an existing orchestration bench task runs from a
document. This directory now carries one: `orch-fanout` is expressed as a
checked-in workflow spec, and the spec — not Go code — decides what dispatches,
how many run at once, and what each node writes.

## The task and why this one

`orch-fanout` (`task.yaml`): audit six modules in `inventory/`, one file per
module under `audits/`, with "at least three distinct sub-agents running
concurrently". It is the marquee parallel-delegation scenario, it is already
mapped as **Full** in `IR-MAPPING.md`, and — decisively for a first spec-driven
run — it is **report lane**: no proposal is sealed, so a run needs only a
sub-agent runner, not a retained-proposal runtime.

`orch-settle-apply` / `orch-quality-gate` would need the retained lane and the
settlement verbs (a `RetainedRuntime`), and `orch-settle-apply`'s host-side
edit between seal and settle has no node kind (`IR-MAPPING.md` G1). Later.

## The spec

`orch-fanout/spec/workflow.yaml` — a `yaah.workflow/v1` document naming the
bench task's shape:

| Bench fact | Document |
|---|---|
| six independent module audits | six `delegate` steps, no `needs` between them |
| role `developer` | `role: developer` |
| report lane, nothing to settle | `lane: report` |
| `max_concurrency: 3` (`bench-orchestration.yaml`) | `budgets.max_concurrent_dispatches: 3` |
| six files in `audits/` | six `emit` steps, `path: audits/<module>.md`, each `needs` its delegate |
| ≤ 6 dispatches (+ retry headroom) | `budgets.max_dispatches: 8`, `max_wall_clock_seconds: 1800` |

Two deliberate choices, both worth knowing:

- **Six delegate+emit pairs, not one `fanout` node.** The task's end state is
  one file *per module*, and an emit's `path:` is a literal
  (`ir.Node.ArtifactPath`), not a template — a fan-out body could not name six
  different artifact paths. Independent top-level nodes still overlap: the
  scheduler makes all six ready at once and the concurrency cap decides how
  many are in flight. Per-item artifact paths are the gap this surfaces (a
  candidate for the executor phase, next to `IR-MAPPING.md` G1).
- **`max_dispatches: 8`, not 6.** The budget is a ceiling, not a target: the
  scheduler crosses it on the attempt that would exceed it, so a budget equal
  to the plan's own dispatch count trips on the last delegate and cancels the
  rest. The two spare dispatches are the retry headroom a live model needs.

## Reproducing it

```bash
cd <yaah checkout>
go build -o /tmp/yaah .          # or use an installed `yaah`

YAAH=/tmp/yaah ORCH_APP_DIR=/app \
  bench/orchestration-tasks/spec-run.sh orch-fanout
```

The driver runs exactly four commands, and prints each:

```bash
yaah workflow validate bench/orchestration-tasks/orch-fanout/spec/workflow.yaml
yaah workflow run      bench/orchestration-tasks/orch-fanout/spec/workflow.yaml \
                       --run-id bench-orch-fanout \
                       --state bench/orchestration-tasks/orch-fanout/.spec-run/state.db \
                       --artifact-root /app --json
yaah workflow explain  --from-trace bench-orch-fanout \
                       --state bench/orchestration-tasks/orch-fanout/.spec-run/state.db \
                       --json bench/orchestration-tasks/orch-fanout/spec/workflow.yaml \
  > bench/orchestration-tasks/orch-fanout/.spec-run/trace.json
python3 bench/orchestration-tasks/_lib/orch_trace.py \
        --trace bench/orchestration-tasks/orch-fanout/.spec-run/trace.json \
        --task orch-fanout --artifact-root /app
```

`run --json` is the `yaah.workflow.run.v1` report (status, dispatch tally,
outputs, artifacts, warnings). `explain --from-trace --json` is the
`yaah.workflow.trace.v1` record (`internal/workflow/trace`), which is what
`_lib/orch_trace.py` asserts over — the same three layers `orch_assert.py`
checks, with the trace layer read from the run's own record instead of the
Shepherd store.

**`run` needs a runner.** `yaah workflow run` takes its sub-agent runner from
the host composition (`workflowRunRunner` in `cmd/yaah/workflow_run.go`); the
standalone binary refuses with "no sub-agent runner configured" rather than
inventing a session, a workspace, and a scope the user never chose
(`TestWorkflowRunRefusesWithoutRunner` pins that refusal). So the live driver
above runs inside a composed host, or with the runner seam wired
(`swapRunRunner` in tests). Wiring the standalone composition is the one thing
this bead did **not** add, deliberately: it is composition-root work, not a
bench driver, and it would dispatch under facts the user never chose.

## What CI proves, with no model

`cmd/yaah/workflow_bench_spec_test.go`, which `go test ./...` runs:

1. `TestBenchSpecsValidate` — every `*/spec/workflow.yaml` under this directory
   goes through `analyzeWorkflow`, the same gate `yaah workflow validate` and
   `yaah workflow run` apply (decode → resolve → structural/values/linearity/
   authority/cost). Any error-severity finding fails CI, so a spec that drifts
   out of the vocabulary never reaches a bench run that costs model time.
2. `TestBenchSpecOrchFanoutRunsFromTheDocument` — the spec runs end to end
   against a deterministic runner (no provider, no network, no sleep) through
   `workflow run` with `--state` and `--artifact-root`, and the trace is
   exported with `explain --from-trace --json`. It asserts the facts the bench
   cares about: six dispatches, the six `audits/*.md` artifacts on disk, twelve
   succeeded nodes, zero divergences from the document, no proposals or
   settlements, and **peak concurrency exactly 3** replayed from the trace's
   `dispatch` records — the model-free form of
   `orch_assert.assert_parallel_dispatch(3)`.

The Python verifier is not part of CI: it needs a real run's trace. Its
assertions are the ones the Go test pins over the same record shape, so a
record-shape change fails CI first.

## Deviations from the bead's Design section

- **No live end-to-end run in this change.** The spec runs end to end against a
  deterministic runner in CI; a *model-backed* run needs the standalone runner
  wiring described above, which the bead's constraints exclude (no new
  dispatcher, and the DSL spec is the dispatcher).
- **`_lib/orch_trace.py` is a sibling of `orch_assert.py`, not an edit to it.**
  `orch_assert.py` reads the Shepherd trace *store*; a spec-driven run's trace
  is a JSON document. The workspace layers (`APP`, `seed`,
  `assert_no_unexpected_changes`, `assert_byte_identical`) are imported from
  `orch_assert`, so nothing is forked — only the trace reader is new.
