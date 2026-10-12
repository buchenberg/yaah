# Terminal-Bench mini-suite results

All yaah-vs-baseline numbers recorded to date, one document per suite
run. The adapter, host setup, and run instructions live in
[README.md](README.md); ledger rows live in the repo root
`BENCHMARKS.md`. Records here are append-only — new runs get a new
section, existing sections are never rewritten.

## Full-suite vs opencode (2026-10-12, deepseek-flash)

Run `2026-10-12__16-03-48` (yaah, one-shot, commit dd038a7 —
carries the PR #291 improvements: one-shot non-interactivity,
incremental usage flush, cache accounting) vs run
`2026-10-12__16-35-01` (opencode, harness installed-agent).
Same 15 frozen tasks, same judge, same model, one trial each.

| Task | yaah | opencode |
|---|---|---|
| hello-world | resolved | resolved |
| broken-python | resolved | unresolved (parse_error) |
| csv-to-parquet | resolved | unresolved |
| cpp-compatibility | resolved | resolved |
| jsonl-aggregator | resolved | resolved |
| assign-seats | resolved | resolved |
| bank-trans-filter | resolved | resolved |
| fix-pandas-version | resolved | resolved |
| git-workflow-hack | resolved | resolved |
| gomoku-planner | resolved | unresolved |
| npm-conflict-resolution | resolved | unresolved |
| create-bucket | resolved | unresolved |
| png-generation | unresolved (OCR-join brittle test; both fail) | unresolved |
| setup-custom-dev-env | unresolved (kind-cluster timeout) | unresolved |
| fix-git | — (upstream-unbuildable) | — (same) |

**yaah 12/14 vs opencode 7/14** of the runnable tasks. Both CLIs
share the same two hard failures (the brittle png OCR check; the
kind-cluster timeout); opencode additionally failed create-bucket,
csv-to-parquet, gomoku-planner, npm-conflict-resolution, and lost
broken-python to a parse error.

Caveats for the record: single trial per task per CLI (a smoke-signal
comparison, not a statistic); yaah's improved run also benefited from
the run-over-run delta of PR #291 (11/15 -> 12/15: create-bucket
flipped after the one-shot non-interactivity fix); opencode reports no
token accounting through the installed-agent path, so the cost
comparison is yaah-only: 3.59M tokens with 84% of prompt tokens served
from deepseek's cache (2.32M of 2.76M prompt tokens; see the run's
state.dbs), down from 6.3M the week before.

## First full-suite run (2026-10-12, deepseek-flash, yaah 12e9334)

Run before the PR #291 improvements. Task-level outcome and token
accounting:

| Task | Resolved | Tokens |
|---|---|---|
| assign-seats | yes | 206,057 |
| bank-trans-filter | yes | 268,661 |
| broken-python | yes | 134,718 |
| cpp-compatibility | yes | 169,021 |
| create-bucket | no (agent timeout vs localstack) | 104,825 |
| csv-to-parquet | yes | 164,575 |
| fix-git | — upstream-broken (deleted fixture repo), excluded | — |
| fix-pandas-version | yes | 295,618 |
| git-workflow-hack | yes | 3,055,210 |
| gomoku-planner | yes | 715,288 |
| hello-world | yes | 30,588 |
| jsonl-aggregator | yes | 180,558 |
| npm-conflict-resolution | yes | 930,590 |
| png-generation | no (tests failed; real agent attempt) | n/a (see below) |
| setup-custom-dev-env | no (agent timeout; spins a kind cluster) | — |

**Score: 11/15, with fix-git excluded as upstream-unbuildable (11/14
of runnable tasks, 78.6%)**. Three non-resolved attempts: two agent
timeouts on infrastructure-heavy tasks (localstack, kind cluster)
inside their 900s budget, and one genuine test failure
(png-generation — the agent ran and produced images; the tests
rejected them). ~6.3M tokens across the rows with accounting (~$2 at
deepseek-flash prices; png-generation's accounting is lost to the
upstream missing volume, and setup-custom-dev-env's session never
flushed at the timeout kill). "Easy" difficulty means easy for a
human: git-workflow-hack alone cost 3.05M tokens. hello-world's
accounting is identical to the smoke run (30,588) because it is a
deterministic single-turn session — the same input shape yields the
same counts, which incidentally validates the accounting's
reproducibility.

### Task defects found at the harness pin (d28711d)

fix-git is unbuildable at the pin: its setup.sh clones
TheMikeMerrill/personal-site, deleted from GitHub — it fails on
every runtime, Docker included. The frozen task list is left
untouched (frozen means frozen); the runner still passes it and the
harness records its build failure, so cite the suite as "11/15,
fix-git excluded (upstream-unbuildable)". png-generation's
docker-compose.yaml omits the agent-logs volume, so its token
accounting is lost (the agent run itself is fine).

## Smoke calibration (2026-10-12, deepseek-flash, commit 16927ff)

| Task | tokens_in | tokens_out | total | messages | resolved |
|---|---|---|---|---|---|
| hello-world | 30,499 | 89 | 30,588 | 5 | yes |
| broken-python | 143,384 | 3,497 | 146,881 | 24 | yes |

The fixed per-session cost dominates: ~30K input tokens before any work
(hello-world did one write). Multi-step debug tasks land 4-5x that.
The full mini-suite then measured ~6.3M tokens — under $2 at
deepseek-flash prices, but 3.5-7x this early projection: the suite's
long-horizon tasks (git-workflow-hack, npm-conflict-resolution)
dwarfed the smoke pair.
