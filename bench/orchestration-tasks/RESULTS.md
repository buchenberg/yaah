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
