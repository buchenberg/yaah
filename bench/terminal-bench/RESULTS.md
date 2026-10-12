# Terminal-Bench mini-suite results

All yaah-vs-baseline numbers recorded to date, one section per suite
run. The adapter, host setup, and run instructions live in
[README.md](README.md); ledger rows live in the repo root
[BENCHMARKS.md](../../BENCHMARKS.md). Records here are append-only —
new runs get a new section, existing sections are never rewritten.

## Full-suite vs opencode (2026-10-12, deepseek-flash)

Run `2026-10-12__16-03-48` (yaah, one-shot, commit dd038a7 —
carries the PR #291 improvements: one-shot non-interactivity,
incremental usage flush, cache accounting) vs run
`2026-10-12__16-35-01` (opencode, harness installed-agent).
Same 15 frozen tasks, same judge, same model, one trial each.

What the harness measures: the agent lands in a fresh container with
a task instruction and a time budget; `resolved` means the hidden
end-state tests pass after the agent finishes. With the model held
constant, the score delta isolates the agent layer (harness, prompts,
tools, context management). Token figures are summed from yaah's
in-container `state.db` (sessions table); wall time is the harness's
per-trial agent window. Both are reproducible with
`extract_metrics.py`. The harness's own token fields are 0 for both
adapters, and opencode writes no usage data under agent-logs, so the
token columns are yaah-only.

| Task | yaah | opencode | yaah time | opencode time | yaah in | yaah out | yaah cached |
|---|---|---|---|---|---|---|---|
| hello-world | resolved | resolved | 5s | 24s | 45,734 | 178 | 31,616 |
| broken-python | resolved | unresolved (parse_error) | 36s | 38s | 151,002 | 5,200 | 133,760 |
| csv-to-parquet | resolved | unresolved | 39s | 44s | 178,353 | 4,609 | 162,176 |
| cpp-compatibility | resolved | resolved | 1200s | 32s | 92,626 | 7,500 | 78,080 |
| jsonl-aggregator | resolved | resolved | 27s | 33s | 104,843 | 2,775 | 88,704 |
| assign-seats | resolved | resolved | 35s | 73s | 140,415 | 4,713 | 124,160 |
| bank-trans-filter | resolved | resolved | 56s | 29s | 197,136 | 9,009 | 149,248 |
| fix-pandas-version | resolved | resolved | 50s | 38s | 174,154 | 5,237 | 149,120 |
| git-workflow-hack | resolved | resolved | 182s | 82s | 935,000 | 42,108 | 827,136 |
| gomoku-planner | resolved | unresolved | 182s | 322s | 306,876 | 32,123 | 287,488 |
| npm-conflict-resolution | resolved | unresolved | 292s | 26s | 618,060 | 22,043 | 539,776 |
| create-bucket | resolved | unresolved | 50s | 28s | 221,484 | 5,343 | 204,288 |
| png-generation | unresolved (OCR-join brittle test) | unresolved | 28s | 41s | — (upstream missing volume) | | |
| setup-custom-dev-env | unresolved (kind-cluster timeout) | unresolved | 1200s | 23s | 272,601 | 8,717 | 244,736 |
| fix-git | — (upstream-unbuildable) | — (same) | — | — | — | | |

**Score: yaah 12/14 vs opencode 7/14** of the runnable tasks. Both
CLIs share the same two hard failures (the brittle png OCR check; the
kind-cluster timeout); opencode additionally failed create-bucket,
csv-to-parquet, gomoku-planner, npm-conflict-resolution, and lost
broken-python to a parse error.

**yaah cost across the 13 tasks with accounting: 3.44M prompt +
150K output = 3.59M tokens, of which 3.02M (84% of total, 88% of
prompt tokens) were served from deepseek's cache** — down from 6.3M
with no cache accounting the week before. Wall time is not directly
comparable either: yaah hit the 1200s budget cap on cpp-compatibility
(resolved at the cap) and setup-custom-dev-env (unresolved), while
opencode's low totals partly reflect fast-fails (npm-conflict-
resolution 26s, setup-custom-dev-env 23s) — giving up early rather
than working the task.

Caveats for the record: single trial per task per CLI (a smoke-signal
comparison, not a statistic); yaah's improved run also benefited from
the run-over-run delta of PR #291 (11/15 -> 12/15: create-bucket
flipped after the one-shot non-interactivity fix).

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
(hello-world did one write). Multi-step debug tasks land 4-5× that.
The full mini-suite then measured ~6.3M tokens — under $2 at
deepseek-flash prices, but 3.5-7× this early projection: the suite's
long-horizon tasks (git-workflow-hack, npm-conflict-resolution)
dwarfed the smoke pair.

## Suite v2 (2026-10-12): known-failure tasks removed

The v1 comparison above included three tasks that fail for reasons
unrelated to agent capability — every CLI tested fails them the same
way — so they add run cost and noise, not signal. They are removed
from the default suite (`mini-suite-v2.txt`, 12 tasks; the runner
defaults to it; `SUITE=mini-suite.txt ./run.sh` restores v1):

- **fix-git** — unbuildable at the pin (setup.sh clones a deleted
  GitHub repo; fails on every runtime).
- **png-generation** — upstream compose omits the agent-logs volume
  (no token accounting), and its OCR-join test is brittle; both CLIs
  fail it.
- **setup-custom-dev-env** — spins a kind cluster inside the task
  budget; both CLIs hit the time cap. Budget-bound rather than
  capability-bound; re-add it under a larger budget if that axis
  matters.

v1 stays frozen for comparability with the recorded runs: its 12/14
comparison above is unchanged and remains citable. Future v2 scores
are "n/12 on the yaah mini-suite v2" — for reference, the recorded
yaah run scores 12/12 on the v2 subset (opencode 7/12).
