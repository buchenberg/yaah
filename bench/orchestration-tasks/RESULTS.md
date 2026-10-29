# Orchestration-suite results

Append-only ledger for the orchestration task set in this directory. The
adapter and runner live in [../terminal-bench](../terminal-bench); aggregate
coding-suite results stay in
[../terminal-bench/RESULTS.md](../terminal-bench/RESULTS.md).

Token accounting comes from the in-container `state.db`
(`sessions.tokens_in/tokens_out`, `sub_tokens_in/out`). The orchestrator share
is `session total - sub-agent share`; `extract_metrics.py --subs` also prints
the per-role split from `yaah.subagent.usage.v1` trace facts. Runs before that
fact existed show only the aggregate split.

All runs are single-trial, deepseek-flash, one-shot, `--n-concurrent 1`.
Verdicts are trace-sensitive: end state alone is not enough.

## 2026-10-17 — settle-discard, umbrella runner + per-sub usage

Run `2026-10-17__04-44-19` (`orch-settle-discard`), via
`bench/run-all.sh` with the per-dispatch usage fact.

| Metric | Value |
|---|---|
| Resolved | yes |
| Agent wall time | 30s |
| Session input / output | 190,614 / 3,867 |
| Cached prompt | 164,608 (86% of input) |
| Orchestrator | 141,765 in / 1,586 out |
| Sub-agents | 48,849 in / 2,281 out (26% of total tokens) |
| Sub-role split | developer: 1 dispatch, 48,849 in / 2,281 out |

The sub-agent share (26%) is the useful number here: the orchestrator spends
most of its prompt budget on the fixed session scaffold, while the developer
carries the work.

## 2026-10-17 — settle-discard (pre-fix accounting)

Run `2026-10-17__04-36-12` (`orch-settle-discard`), from the suite runner.
Same task, before `yaah.subagent.usage.v1` existed; the aggregate sub share
was already available from state.db after the foreground-usage fix.

| Metric | Value |
|---|---|
| Resolved | yes |
| Agent wall time | 31s |
| Session input / output | 205,135 / 4,007 |
| Cached prompt | 178,688 (87% of input) |
| Orchestrator | 141,376 in / 1,776 out |
| Sub-agents | 63,759 in / 2,231 out (32% of total tokens) |

## 2026-10-17 — explicit-delegation fan-out (resolved)

Run `2026-10-17__04-26-01`, a temporary variant of `orch-fanout` whose
instruction explicitly requested parallel delegate sub-agents (the committed
task stays behavior-forcing).

| Metric | Value |
|---|---|
| Resolved | yes (end state, integrity, trace) |
| Agent wall time | 136s |
| Session input / output | 853,011 / 54,986 |
| Cached prompt | 760,192 (89% of input) |
| Orchestrator | 292,900 in / 7,848 out |
| Sub-agents | 560,111 in / 47,138 out (67% of total tokens) |
| Sub-sessions | 9 (3 analyst, 6 developer) |

This is the cost shape of a real fan-out: sub-agents dominate token spend
(67%), and the orchestrator's own turns are the smaller share. The plan's
original 20–40K-per-task estimate was off by an order of magnitude for
delegating tasks.

## 2026-10-17 — explicit-delegation fan-out (pre-fix)

Run `2026-10-17__04-22-53`, same explicit variant, before foreground delegate
usage was folded into the session. Verdict unresolved on the token-share
assertion; state.db recorded `sub_tokens_*=0` even though three sub-sessions
ran.

| Metric | Value |
|---|---|
| Resolved | no (trace: no sub-agent tokens recorded) |
| Agent wall time | 70s |
| Session input / output | 130,449 / 4,724 |
| Cached prompt | 109,696 (84% of input) |
| Sub-agents | 0 recorded (accounting bug, since fixed) |

## 2026-10-17 — behavior-forcing fan-out (inline)

Run `2026-10-17__04-21-04`, the committed `orch-fanout` instruction. The model
read all six modules and wrote all six audits inline in 16 seconds; end state
and integrity passed, the trace layer failed with 0 sub-sessions.

| Metric | Value |
|---|---|
| Resolved | no (trace: 0 delegated sub-agent sessions) |
| Agent wall time | 16s |
| Session input / output | 117,205 / 2,337 |
| Cached prompt | 100,096 (85% of input) |
| Sub-agents | none |

This is the suite doing its job: a correct-looking result that skipped
orchestration is rejected.

## 2026-10-17 — first full 8-task run

Run `2026-10-17__04-56-01`, one trial per task, `--n-concurrent 1`, via
`bench/run-all.sh`. **4/8 resolved.**

| Task | Resolved | Time | in | out | sub in | sub out |
|---|---|---|---|---|---|---|
| orch-fanout | no | 16s | 100,779 | 2,378 | 0 | 0 |
| orch-settle-apply | no | 46s | 331,767 | 5,773 | 84,566 | 2,545 |
| orch-settle-discard | yes | 33s | 172,716 | 4,586 | 48,467 | 2,746 |
| orch-confinement | no | 57s | 186,910 | 9,377 | 62,913 | 6,417 |
| orch-quality-gate | yes | 53s | 205,722 | 8,239 | 71,480 | 6,377 |
| orch-escalation | yes | 30s | 129,918 | 4,590 | 30,147 | 1,998 |
| orch-guard | no | 79s | 415,894 | 10,871 | 122,255 | 5,455 |
| orch-compact | yes* | 20s | 90,873 | 1,715 | 0 | 0 |

Totals: 1,634,579 prompt + 47,529 output = 1,682,108 tokens, 1,425,664
cached (87%). Orchestrator 1,214,751 / 21,991; sub-agents 419,828 / 25,538
(26% of total tokens).

`*` `orch-compact` passed under the original verifier, which did not require a
persisted compaction summary. Under the current strict verifier it does not
prove compaction; the task is parked in `suite.txt` (`yaah-8uz`) and this
result is not comparable to later runs.

Failure reasons, and whether they are harness defects:

- **orch-fanout** — inline again (0 sub-sessions), plus an end-state mismatch:
  the model wrote `functions: make_item,total_value` without a space after the
  comma. The instruction says "comma-separated", so the verifier was too
  strict; normalization was the fix.
- **orch-settle-apply** — the model settled with `select`, not `apply`, and the
  end state passed. The task edited CHANGELOG *before* dispatching, so select
  fast-forwarded legitimately. The scenario must move the basis after the
  proposal is sealed to force three-way apply.
- **orch-confinement** — the shared lane is not a "workdir" dispatch, so no
  jail was established and the `/tmp` escape succeeded; no confinement fact was
  recorded. The task must use the retained lane (worktree workdir) to get a
  confined dispatch.
- **orch-guard** — the sub-agent achieved success in one tool call (a loop
  inside bash), so only one failed capture reached the trace. With
  `threshold: 0.6, window: 4`, the rule never fired. The scenario needs a
  first-failure trigger (`threshold: 0.5, window: 2`) and a stricter
  one-attempt-per-call instruction.

The four passing tasks exercised retained discard, quality gates, escalation,
and (under the original verifier) compaction. The failures are informative; the three harness defects were
fixed and the affected tasks re-run (next section).

The umbrella run also recorded the coding suite at 6/6 in the same invocation;
see [../terminal-bench/RESULTS.md](../terminal-bench/RESULTS.md).

## 2026-10-17 — task fixes and focused re-runs

The first full run exposed three task-design defects (not product bugs) and one
behavioral miss. Fixes:

- **orch-fanout** — verifier now treats comma spacing in `functions:` as
  presentation. The committed instruction also changed from behavior-forcing to
  explicit delegation (at least three concurrent developers); the model
  consistently did the audits inline when only the workload shape was described.
- **orch-settle-apply** — the CHANGELOG edit now happens *after* the proposal
  is sealed, moving the basis past the fork point. `select` can no longer
  fast-forward, so `apply` is forced.
- **orch-confinement** — switched to the retained lane so the dispatch has a
  worktree and a real confinement placement. The verifier accepts `select` or
  `apply` for settlement; the evidence that matters is the confinement fact and
  the denied escape.
- **orch-guard** — rule thresholds changed to `threshold: 0.5, window: 2` so
  the first failed tool call trips the rule, and the instruction forbids
  hiding the retry loop inside one shell command. The verifier requires at
  least one failed capture.

### Re-run 1: four affected tasks (run `2026-10-17__05-10-07`)

2/4 resolved.

| Task | Resolved | Time | in | out | sub in | sub out |
|---|---|---|---|---|---|---|
| orch-fanout | no | 18s | 118,897 | 2,856 | 0 | 0 |
| orch-settle-apply | yes | 60s | 348,439 | 8,921 | 85,575 | 3,287 |
| orch-confinement | no | 43s | 202,079 | 6,808 | 74,563 | 4,374 |
| orch-guard | yes | 140s | 885,445 | 20,588 | 131,126 | 7,462 |

Totals: 1,554,860 prompt + 39,173 output, 1,400,064 cached (90%).
Orchestrator 1,263,596 / 24,050; sub-agents 291,264 / 15,123 (19%).

`orch-confinement`'s end state, integrity, and confinement fact all passed; it
failed only because the verifier still demanded the `apply` verb while the
model used `select` (a legitimate settlement here). `orch-fanout` was still the
behavior-forcing instruction and stayed inline.

### Re-run 2: fan-out + confinement (run `2026-10-17__05-17-45`)

2/2 resolved.

| Task | Resolved | Time | in | out | sub in | sub out |
|---|---|---|---|---|---|---|
| orch-fanout | yes | 80s | 296,057 | 36,701 | 171,257 | 29,770 |
| orch-confinement | yes | 51s | 235,166 | 8,061 | 79,860 | 5,333 |

Totals: 531,223 prompt + 44,762 output, 461,056 cached (87%).
Orchestrator 280,106 / 9,659; sub-agents 251,117 / 35,103 (50%).
Fan-out ran three developer dispatches; 50% of tokens were sub-agent spend.

### Net status

Seven of the eight tasks have a passing live run, but not in a single
invocation: `orch-settle-discard`, `orch-quality-gate`, and `orch-escalation`
passed in the first full run; the four fixed tasks passed in focused re-runs.
Some of those passes used earlier verifier revisions, and the strict checks
have since been re-validated against oracles rather than every live scenario.
`orch-compact` is parked (`yaah-8uz`) and its earlier result is not comparable.
A fresh full-suite run is the next record.

## 2026-10-17 — PR #382 review pass

Addressed reviewer findings; no new full-suite run.

- **False-positive guard scenario.** The blocking shell guard (enabled by
  tracing) denied every sub-agent bash call, and a prior "passing" guard run
  fabricated `ok.txt`/`answer.txt` without executing `flaky.sh`. The guard is
  now disabled for `orch-guard` and `orch-quality-gate`; `ok.txt` must contain
  the script's own `attempts=5` marker, the warning must appear in a **tool
  result**, and at least four failed bash captures are required. Live re-run:
  resolved (`2026-10-17__05-36-45`).
- **Quality gate.** Same guard disable; `[quality-gate:FAIL]` must appear in a
  tool result. Live run executed `verify.py` and passed (`05-29-24`).
- **Settle-apply fixture.** The seed already contained the `power(2, 5)`
  assertion, making the delegate step a no-op. Removed it; the oracle and live
  run both add it. Live run passed (`05-29-24`).
- **Settle-discard id.** `answer.txt` must name `proposal:<digits>:<digits>`,
  and the trace layer cross-checks it against the settled output ID.
- **Missing evidence.** A missing trace/state DB now fails unless the run
  declares itself an oracle (`ORCH_ORACLE=1`), so broken
  capture cannot skip the orchestration layers.
- **Tool-failure and window helpers.** Failures are scoped to `sub-` owners;
  unfinished sessions use their last start fact for the window.
- **Usage accounting.** Child usage folds before the tool result is
  published, no longer overwrites the parent's last-prompt/cache counters, and
  the per-dispatch usage fact records own (non-nested) usage.
- **Confinement.** Requests `containment: paddock` (fails closed) and the
  helper requires an enforced, non-advisory placement; an advisory fallback
  can no longer pass.
- **Compaction.** Parked in `suite.txt`: live runs never persisted a
  `compacted_summary` even with a 6K window, 38+ messages, pruning off, and
  `compact_max_messages: 20`. Tracked in `yaah-8uz`; the verifier keeps the
  strict summary assertion so re-enabling it cannot regress silently.

### 2026-10-17 — second review follow-up

- `flaky.sh` is idempotent after success: a redundant confirmation run no
  longer advances the counter or rewrites `ok.txt`, so `attempts=5` is stable
  across delegate re-dispatches.
- Oracle detection is the `ORCH_ORACLE=1` environment signal only. The agent
  can write files anywhere, so a filesystem marker was forgeable; the
  verifier's environment is not. For Terminal-Bench's oracle agent, each
  task's `run-tests.sh` detects the staged `/oracle/solution.sh` and exports
  the flag; manual runs set it explicitly (documented in
  `../terminal-bench/README.md`). Validated end to end with
  `tb run --agent oracle -t orch-guard`: 1/1.
- Failed-capture counting uses `COUNT(DISTINCT record_id)`, so a record linked
  to multiple `path_entries` rows counts once.
- Compaction helper docstring names `sessions.compacted_summary`.

All eight oracles re-validated in-container with the env signal.

## 2026-10-20 — orch-compact fixed and re-enabled

Root causes found while investigating `yaah-8uz` (run `2026-10-20__12-39-41`):

1. **Raw gate under-triggered on Anthropic usage.** `LastPromptTokens` is uncached
   input; the compaction raw gate used it directly, so cache-heavy prompts looked
   small. The gate now uses input + cache-creation + cache reads, while the
   effective count stays uncached.
2. **Oversized single-turn conversations produced no summary material.** When the
   whole conversation is one turn larger than the preserve budget, `SplitTail`
   returned `KeepStart == len(messages)` and compaction summarized nothing. It now
   keeps the final exchange (including the assistant call for tool results) and
   summarizes the rest.
3. **Summaries were not persisted in one-shot runs.** `applyDefaults` created the
   context manager without attaching the session DB, so `compacted_summary`
   stayed empty even when compaction succeeded. The DB is now wired from the
   persister.

The verifier dropped the message-count proxy and now requires the persisted
summary plus tool-call linkage. Live run `2026-10-20__12-41-11`:
**resolved 100%**, `compacted_summary` 1.2 KB, task re-enabled in `suite.txt`.

## 2026-10-20 — suite audit: minimal metric set + report-lane fan-out

Audit before the next benchmark capture (follow-up to the `yaah-7yf` lane
decoupling, PRs #479 and #480):

- **New `orch-report-fanout`.** Four read-only audits through report-role
  delegates (`reviewer`) in the retained lane: ≥2 parallel dispatches, no
  sealed proposals, no settlements (`assert_no_proposals`), worktrees torn
  down, host tree byte-identical, report content exact. Pins the
  isolated-report lane end to end — the first task that fails if a report
  dispatch ever seals or settles again.
- **`assert_any_settled_output`** gives the settled-output schema family a
  positive consumer (`orch-settle-apply`), so a schema-name drift fails
  loudly instead of silently zero-counting.
- **`orch-settle-discard` verifier fixed.** The shepherd v0.17 repin added a
  per-seal sequence suffix to sealed output ids (`out:<owner>:<seq>`), so the
  exact-membership check could never match again (run `15-22-31`'s predecessor
  failed on it). It now matches the prefix; ad-hoc re-run resolved.
- **`suite.txt` shrinks to the five-task metric set** — one task per
  load-bearing behavior: fanout, report-fanout, settle-apply, confinement,
  compact. `orch-settle-discard`, `orch-quality-gate`, `orch-escalation`, and
  `orch-guard` remain runnable ad hoc by name; their last recorded results
  stand.

Validation runs (one trial each): `orch-report-fanout` first failed on comma
spacing in `functions:` (the presentation rule fanout already pinned;
normalized) and then resolved (`15-01-28` no → `15-19-16` yes); the tightened
verifier also re-validated after the review fixes. `orch-settle-discard`
resolved with the prefix fix (`15-03-31`). `orch-settle-apply` resolved with
the positive settled-output assertion (`15-17-33`).

## 2026-10-20 — first full run of the minimal suite (5/5)

Run `2026-10-20__15-22-31`, one trial per task, binary `v0.88.1`
(commit `47f14c7`, includes the lane decoupling), deepseek-flash,
`N_CONCURRENT=2`. **5/5 resolved (100%)** — the suite's first 100% full run.

| Task | Resolved | Time | in | out | sub in | sub out |
|---|---|---|---|---|---|---|
| orch-fanout | yes | 97s | 594,423 | 40,531 | 442,013 | 35,576 |
| orch-settle-apply | yes | 80s | 549,502 | 12,418 | 76,808 | 2,617 |
| orch-report-fanout | yes | 78s | 298,612 | 22,426 | 177,920 | 19,551 |
| orch-confinement | yes | 52s | 228,548 | 9,199 | 67,279 | 4,717 |
| orch-compact | yes | 37s | 139,732 | 1,806 | 0 | 0 |

Totals: 1,810,817 prompt + 86,380 output = 1,897,197 tokens.
Sub-agents 764,020 in / 62,461 out (44% of total tokens).

Notes:

- **Report lane in a benchmark.** `orch-report-fanout` is the new task: three
  concurrent reviewer dispatches produced no proposal records and no orphan
  worktrees; the reports landed from the returned text. Sub-agent share 63%
  of that task's tokens — delegation is doing the reading.
- **Fan-out remains the heaviest task** (594K in, 442K sub in): three
  developer dispatches on the shared lane re-read overlapping inventory.
  `yaah-csg` (shared read cache) targets exactly this.
- **Compact** shows zero sub tokens and the shortest agent window — the
  orchestrator reads and compacts alone, and the persisted summary satisfied
  the strict verifier on the first try after the `yaah-8uz` fixes.

### Net status

The five-task minimal suite has a single-invocation 100% full run at
commit `47f14c7`. The four ad-hoc tasks' most recent records: settle-discard
resolved (`15-03-31`, prefix-fixed verifier), quality-gate and escalation
resolved on 2026-10-17 (`05-29-24`), guard resolved (`05-36-45`); those three
predate the minimal-suite re-shuffle and have not been re-run since.

## 2026-10-20 — stable sub-agent prompt prefix (yaah-o02)

Sub-agent dispatches re-paid their full cold-start context per sibling, and
the prefix was not even byte-stable: the environment header (with the
per-dispatch worktree path) led the prompt, and `Registry.List()` returned
tools in map order, so the tool array also differed between siblings.
Two changes:

1. `buildSubAgentSysPrompt` now opens with the byte-stable block (identity,
   role guidance, contract, directives, escalation, shell rules) and closes
   with the volatile parts (confinement guidance, environment header).
2. `Registry.List()` returns sorted names, so the request's tool array is
   deterministic across dispatches.

Both are preconditions for deepseek's automatic server-side prefix caching
(OpenAI-compatible path sends no cache markers; hits need a byte-identical
prefix). Regression-pinned: section order in the two prompt tests, and a new
`TestBuildSubAgentSysPrompt_StablePrefixAcrossSiblings` asserting two sibling
prompts share a byte-identical prefix that carries the whole stable block and
leaks neither worktree path.

Measurement is honest but noisy: three `orch-report-fanout` trials on the
branch (all resolved) vs the three pre-change runs of the same task —

| | prompt in | sub in |
|---|---|---|
| pre-change (3 runs) | 331K / 299K / 190K | 171K / 178K / 92K |
| branch (3 runs) | 292K / 253K / 228K | 140K / 107K / 85K |

Means: prompt in 273K → 258K (−6%), sub in 147K → 110K (−25%). All three
branch trials landed below the pre-change sub-in mean, but single-trial
variance is large (the pre-change spread alone is 92K–178K), so treat the
magnitude as directional. The structural preconditions are pinned by tests;
the tracking-table runs are the long-term confirmation.

## 2026-10-21 — trimmed sub-agent role belts (yaah-9n0)

The sub-agent request is ~86% schema bytes: the built-in roles shipped
their full tool list on every dispatch, including tools the environment
cannot run (`powershell` with no pwsh) or cannot use (the Go toolchain
machinery in a Python workspace). Two changes:

1. **Role belts trimmed** (`internal/prompts/roles/`): developer 24 → 22,
   tester 16 → 14, analyst 14 → 13, reviewer 11 → 11 (reordered only).
   The `http` tool goes (webfetch covers it) and so does `bisect` (git
   covers it).
2. **An environment gate** (`internal/tools/availability.go`): a shell
   the machine does not have and Go toolchain machinery in a workspace
   that is not a Go module are left out of the sub-agent registry, and
   the sub-agent's prompt names them so the model does not call a tool
   that is not registered.

A/B on the two fan-out tasks, same day, same model, one trial each —
`main` is the branch's base (stable prefix, quick-ref trim, sed removal,
tool belt) and the branch adds only the role trims and the gate:

| Task | main sub-in | trimmed sub-in | change |
|---|---|---|---|
| orch-fanout (developer role) | 559,070 | 295,561 | **−47%** |
| orch-report-fanout (reviewer role) | 125,133 | 152,735 | +22% (noise) |

Reading: the cut is where the belt was fat, and most of it comes from the
environment gate rather than the file edit. The developer role loses two
tools in the file (`http`, `bisect`) and six more at dispatch time — the
five Go tools (no Go module in the fixture) and `powershell` (no pwsh) —
so 24 named tools become 16 on the wire: −47% sub-agent prompt tokens and
−34% total prompt tokens on that task. The reviewer role was already lean
(two tools dropped: `go_outline`, `staticcheck`, both gated out in a
Python fixture) — ~450 tokens a call, far below the run-to-run spread this
task shows (85K–178K across trials), so it lands inside the noise. The
mechanism scales with the number of tools actually left out, which is what
the bead predicted.

## 2026-10-29 — orch-recovery (seed + live)

Adds the ninth planned scenario: a stale isolated worktree and its sealed
output are baked into the image by a multi-stage Go seeder, then recovered
from the durable trace. The seeder creates a normal root plus an isolated
worktree child, writes `recovered.txt`, seals the child, and exits without
settling — the crash shape.

- Task: `orch-recovery`
- Oracle: run `2026-10-29__13-43-13`, **1/1**, all three verifier layers pass
- Live yaah: run `2026-10-29__13-43-47`, **1/1**, all three layers pass
- Model: `deepseek-flash`; off-peak; one trial
- Live metrics: 2,755 uncached input / 1,205 output / 95,616 cache read,
  6 steps, 12s, **$0.0014**

The verifier checks the reported adopted path, the exact recovered payload,
`trap.txt` byte identity, git worktree registration, the fork record in the
trace, and the sealed output. `suite.txt` now lists nine scenarios;
`orch-settle-discard` remains runnable ad hoc because it overlaps
`orch-settle-apply`.

## 2026-10-29 — runtime schema namespace compatibility

The orchestration verifier still queried only the pre-rename
`shepherd.run_output.*` schemas. Runtime v0.21 emits
`goatherd.run_output.*` (and the kernel's frozen `shepherd2` vocabulary is
separate), so `orch-settle-discard`'s end state passed while its trace layer
reported no settlement.

The helper now unions both runtime namespaces for settlement actions, settled
output IDs, and sealed-output counts. Recorded copies are re-vendored.

- Live `orch-settle-discard` after the fix: run `2026-10-29__15-01-01`, **1/1**
- Before the fix: run `2026-10-29__14-59-03`, end state passed but trace layer
  failed with `settlement record is missing ['discarded']`

This affects every orchestration task that asserts settlement/sealed evidence,
not only the mock track.

## 2026-10-29 — deterministic mock provider (`yaah-bpu`)

Adds a mock-provider track for orchestration mechanics without sampling a
model. `_mock/server.py` is an OpenAI-compatible streaming server; each task
has a trap that pins the model allowlist, ordered request kinds, allowed tool
calls, and canned assistant turns. A request outside the trap is rejected
with 400 — there is no fallback to a real provider.

`orch-settle-discard-mock` is the first wired task. It keeps the real yaah
binary, fixture, runtime, proposal/settlement path, and verifier; only the
provider is synthetic. The trap reads the proposal id from the actual
`delegate` result and substitutes it into the discard call and `answer.txt`,
so the runtime's per-session id stays real rather than being faked.

Two fixed runs, same commit, same fake key (`DEEPSEEK_API_KEY=mock`):

- `2026-10-29__16-06-22`: **1/1**
- `2026-10-29__16-07-08`: **1/1**

`compare_runs.py` reports an exact match on verdict, message/tool sequence,
token accounting, and trace record-schema counts. The token counts are 72,716
input / 96 output / 10,842 sub-agent input / 32 sub-agent output in both
runs; both trace stores carry the same 47 records across the same schemas.
Only the runtime-generated proposal id (and timestamps) vary. The server's
usage estimate normalizes that id before measuring, so the token accounting
does not move with it.

Run with `DATASET=bench/orchestration-tasks`,
`SUITE=bench/orchestration-tasks/mock-suite.txt`, and `DEEPSEEK_API_KEY=mock`;
the mock service is a compose sidecar built from `_mock/` with a task-specific
trap.
