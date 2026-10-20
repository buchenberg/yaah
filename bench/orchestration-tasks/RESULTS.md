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
