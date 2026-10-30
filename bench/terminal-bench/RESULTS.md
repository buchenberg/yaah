# Terminal-Bench mini-suite results

All yaah-vs-baseline numbers recorded to date, one section per suite
run. The adapter, host setup, and run instructions live in
[README.md](README.md); ledger rows live in the repo root
[BENCHMARKS.md](../../BENCHMARKS.md). Records here are append-only —
new runs get a new section, existing sections are never rewritten.


## Tracking table

One row per recorded full-suite run, five tasks each (hello-world,
broken-python, csv-to-parquet, jsonl-aggregator, assign-seats). This is the
canonical plot source; the prose sections below are per-run detail and
superseded comparisons. `gomoku-planner` left the suite on 2026-10-20
(vision-adjacent outlier: ≈40% of tokens, 2/3 of wall time); rows before
that date are recomputed from the recorded per-task tables with the same
five tasks, so every row is directly comparable.

- Resolved is out of 5. Tokens are yaah-semantics prompt tokens
  (input includes cache reads) and output tokens.
- Cached % is cache-read share of prompt tokens.
- Time is summed per-task agent wall time.

| Date | Commit | Agent | Resolved | Prompt in | Out | Cached | Time |
|---|---|---|---|---|---|---|---|
| 2026-10-17 | aaa8958 | yaah | 5/5 | 752,264 | 14,434 | 669,440 (89%) | 137s |
| 2026-10-17 | hermes-agent 0.19.0 | hermes | 5/5 | 490,235 | 7,033 | 448,896 (92%) | 149s |
| 2026-10-20 | 47f14c7 | yaah | 5/5 | 724,083 | 15,199 | 643,200 (89%) | 133s |
| 2026-10-20 | hermes-agent 0.19.0 | hermes | 4/5 | 453,528 | 7,373 | 414,080 (91%) | 147s |
| 2026-10-20 | 5d927a7+quickref-trim (trial 1) | yaah | 5/5 | 786,996 | 26,344 | 712,448 (91%) | 292s |
| 2026-10-20 | 5d927a7+quickref-trim (trial 2) | yaah | 5/5 | 536,465 | 14,714 | 523,648 (98%) | 125s |
| 2026-10-20 | 5d927a7+quickref-trim+sed-rm (trial 3) | yaah | 5/5 | 565,069 | 14,487 | 544,768 (96%) | 137s |
| 2026-10-29 | 8eb063f | yaah | 5/5 | 759,265 | 19,498 | 745,600 (98%) | 184s |
| 2026-10-29 | hermes-agent 0.19.0 | hermes | 5/5 | 423,427 | 6,977 | 384,000 (91%) | 176s |
| 2026-10-29 | opencode 2.1.3 | opencode | 5/5 | 300,316 | 6,823 | 283,648 (94%) | 134s |

OpenCode's 2026-10-29 `Out` is 6,823 output tokens plus a separate 4,213
reasoning tokens; the cost includes reasoning. The other arms do not report a
separate reasoning bucket.

Run ids: 2026-10-17__04-47-51 (yaah), hermes-coding 2026-10-17__18-34-17
lineage (see the Hermes section below), 2026-10-20__15-49-57 (yaah),
2026-10-20__15-55-38 (hermes), 2026-10-20__16-32-16, 2026-10-20__16-35-46,
and 2026-10-20__16-46-31 (yaah, quickref-trimmed; trial 3 also drops the
deprecated sed tool). The 2026-10-29 three-way rows are
2026-10-29__18-40-18 (yaah), 2026-10-29__18-46-16 (hermes), and
2026-10-29__18-52-00 (opencode); see the fresh three-way section below. The
2026-10-20 hermes run failed `assign-seats` on `has_correct_pairs` — the same
task family it failed on 2026-10-12 (broken-python then); single-trial
variance is real.

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
unrelated to agent capability, so they add run cost and noise, not
signal. They were removed from the default suite at the time
(`mini-suite-v2.txt`, 12 tasks — historical: superseded by the
Suite reset section below, and neither file is in the tree anymore):

- **fix-git** — unbuildable at the pin (setup.sh clones a deleted
  GitHub repo; fails on every runtime).
- **png-generation** — upstream compose omits the agent-logs volume
  (no token accounting), and its OCR-join test is brittle; both CLIs
  fail it.
- **setup-custom-dev-env** — spins a kind cluster inside the task
  budget. Neither CLI resolves it, but by different modes: yaah hit
  the 1200s cap; opencode fast-failed (23s). Budget-bound rather than
  capability-bound; re-add it under a larger budget if that axis
  matters.

v1 stays frozen for comparability with the recorded runs: its 12/14
comparison above is unchanged and remains citable. Future v2 scores
are "n/12 on the yaah mini-suite v2" — for reference, the recorded
yaah run scores 12/12 on the v2 subset (opencode 7/12).

## Suite reset (2026-10-12): small editable subset

The frozen-list convention is dropped: the default task list is now
`suite.txt`, 6 tasks, editable — iterate freely and cite the file's
commit alongside any score. The frozen lists (`mini-suite.txt`,
`mini-suite-v2.txt`) are removed from the tree; git history preserves
them — the v1 task list both recorded runs used is recoverable at
commit 2595c6b, the never-run v2 list at 639e2cc — so
reproducibility of past scores is unaffected.

Selection rationale: one representative per major category (files,
software engineering, data, algorithms, games/planning), all cheap at
flash-model prices, plus the tasks opencode failed that yaah resolves
(broken-python, csv-to-parquet, gomoku-planner) for discriminative
power. On the recorded 2026-10-12 runs (deepseek-flash), this subset
scored:

| Task | yaah | opencode | yaah tokens (in+out) |
|---|---|---|---|
| hello-world | resolved | resolved | 45,912 |
| broken-python | resolved | unresolved (parse_error) | 156,202 |
| csv-to-parquet | resolved | unresolved | 182,962 |
| jsonl-aggregator | resolved | resolved | 107,618 |
| assign-seats | resolved | resolved | 145,128 |
| gomoku-planner | resolved | unresolved | 338,999 |
| **score** | **6/6** | **3/6** | **976,821 (~27% of the 15-task run)** |

## Fresh suite.txt comparison (2026-10-12, deepseek-flash, yaah b2825c3)

Both CLIs on the 6-task `suite.txt` (at commit b2825c3), same harness
pin (d28711d), same judge, one trial each, runs minutes apart: yaah
run `2026-10-12__17-26-54` (binary built from b2825c3) vs opencode run
`2026-10-12__17-35-13`.

| Task | yaah | opencode | yaah time | opencode time | yaah in | yaah out | yaah cached |
|---|---|---|---|---|---|---|---|
| hello-world | resolved | resolved | 6s | 23s | 45,709 | 157 | 31,360 |
| broken-python | resolved | unresolved (parse_error) | 34s | 24s | 204,906 | 3,802 | 187,648 |
| csv-to-parquet | resolved | resolved | 21s | 73s | 162,108 | 1,613 | 146,304 |
| jsonl-aggregator | resolved | resolved | 24s | 38s | 118,083 | 2,394 | 101,888 |
| assign-seats | resolved | resolved | 30s | 49s | 121,224 | 5,293 | 105,472 |
| gomoku-planner | resolved | resolved | 112s | 116s | 292,347 | 17,656 | 275,456 |
| **score** | **6/6** | **5/6** | **227s** | **323s** | **944,377** | **30,915** | **848,128** |

yaah cost: 944,377 prompt + 30,915 output = 975,292 tokens, 90% of
prompt tokens served from deepseek's cache. opencode token accounting
remains unavailable through the installed-agent path; wall time
favors yaah on 5 of 6 tasks here but stays a secondary metric.

Run-over-run notes: opencode scored 3/6 on this subset in its earlier
`2026-10-12__16-35-01` run and 5/6 here — csv-to-parquet and
gomoku-planner flipped to resolved — so single-trial variance is real
and the earlier gap overstated opencode's capability. Its one
consistent failure is broken-python: a parse_error in both of its
runs (2/2), while yaah resolved it in every run (3/3). yaah is 6/6
across all recorded runs of this subset.

## Hermes added to the comparison (2026-10-12, deepseek-flash, hermes-agent 0.19.0)

Third CLI: Nous Research's hermes-agent 0.19.0, run through a new
installed-agent adapter (`hermes_agent.py`, this commit). Like the
static yaah/opencode binaries it is immune to broken-python's
deleted-pip fixture: the adapter ships hermes as a prebuilt,
self-contained package tree (one tarball per Python minor version —
the task images at the pin use 3.13 and ubuntu-24-04's 3.12), so the
container's own packaging is never used. Same suite, pin, model,
judge, one trial each, all same-day: yaah `2026-10-12__17-26-54`,
opencode `2026-10-12__17-35-13`, hermes `2026-10-12__18-34-17`.
hermes `-m deepseek-flash` resolves via its deepseek provider to the
wire alias `deepseek-chat` (flash tier); it ran with its out-of-box
defaults.

| Task | yaah | hermes | opencode |
|---|---|---|---|
| hello-world | resolved 6s | resolved 16s | resolved 23s |
| broken-python | resolved 34s | resolved 41s | unresolved 24s (parse_error) |
| csv-to-parquet | resolved 21s | resolved 104s | resolved 73s |
| jsonl-aggregator | resolved 24s | resolved 17s | resolved 38s |
| assign-seats | resolved 30s | resolved 29s | resolved 49s |
| gomoku-planner | resolved 112s | resolved 65s | resolved 116s |
| **score** | **6/6** | **6/6** | **5/6** |
| **agent time** | **227s** | **272s** | **323s** |

Token accounting (yaah from its in-container state.db; hermes from its
`--usage-file` report, whose input excludes cache reads and writes —
both combined here to match yaah's semantics; cache writes are zero on
deepseek in this run; opencode reports nothing):

| Metric | yaah | hermes |
|---|---|---|
| prompt tokens | 944,377 | 844,193 |
| output tokens | 30,915 | 14,536 |
| total | 975,292 | 858,729 |
| cached prompt tokens | 848,128 (90%) | 816,128 (97%) |

Notes: yaah and hermes tie at 6/6, and both resolve broken-python —
the task opencode parse-errors on in both of its runs (2/2). hermes
is the cheaper run (0.86M vs 0.98M in+out) with the higher cache-hit;
yaah is faster overall (227s vs 272s agent time; 3-3 per task). As
everywhere above: one trial per CLI, a smoke-signal comparison — hermes
at its defaults, yaah as benched, same judge.

## Aider added to the comparison (2026-10-12, deepseek-flash, aider @ 5dc9490)

Fourth CLI: aider, run through a new driver-based adapter
(`aider_agent.py` + `aider_driver.py`, this commit). Aider ships into
the container as a prebuilt package tree per Python minor version
(like the hermes tree); because its usage reporting is display-only,
the driver runs the same Coder flow as `aider --message` in
non-streaming mode (aider skips the final usage-only chunk on the
streaming path, so its streamed "Tokens:" numbers are estimates) and
hooks the accounting call to dump exact tokens. Model
`deepseek/deepseek-chat` — the name aider's own model-settings.yml
curates for this family (edit_format diff, caches_by_default, repo
map); deepseek-chat is the flash tier's wire alias, the same model
every other CLI here benches. A short autonomy preamble is prepended
(aider answers one-shot instructions with clarifying questions
without it — mirroring yaah's one-shot guidance). Run
`2026-10-12__20-18-12`.

| Task | aider | time | in | out | cached |
|---|---|---|---|---|---|
| hello-world | resolved | 9s | 2,494 | 39 | 1,536 |
| broken-python | unresolved (edit-format non-conformance) | 929s | 289,203 | 0 | 289,024 |
| csv-to-parquet | unresolved | 10s | 2,505 | 194 | 128 |
| jsonl-aggregator | unresolved | 11s | 2,703 | 503 | 1,536 |
| assign-seats | unresolved | 799s | 248,434 | 0 | 248,192 |
| gomoku-planner | unresolved | 325s | 100,941 | 0 | 100,736 |
| **score** | **1/6** | **2083s** | **646,280** | **736** | **641,152 (99%)** |

**Score: aider 1/6.** The dominant failure mode is protocol, not
capability: deepseek-flash answers with prose and bash blocks
instead of aider's diff edit format, and aider gives up after its 3
reflections without applying edits — the panes show real repair
plans in every failure. The three long tasks burned 100-290K-token
prompts (99% served from cache) whose single call returned no output
within the task budget. The behavior was stable across the three
configurations tried during integration (aider's generic defaults,
+autonomy preamble, +curated deepseek settings), so the record is
the harness-protocol match between aider and this model tier —
parallel to opencode's reproducible parse_error on broken-python.

Four-way summary on suite.txt (same day, same model tier, same
judge, one trial each):

| Metric | yaah | hermes | aider | opencode |
|---|---|---|---|---|
| Score | 6/6 | 6/6 | 1/6 | 5/6 |
| Tokens (in+out) | 975,292 | 858,729 | 647,016 | n/a |
| Prompt cache-hit | 90% | 97% | 99% | n/a |
| Agent time | 227s | 272s | 2083s | 323s |

## Aider removed from the roster (2026-10-12)

The aider adapter (`aider_agent.py`, `aider_driver.py`,
`aider-setup.sh.j2`) is removed from the tree: 1/6 with a
protocol-mismatch failure mode (deepseek-flash does not conform to
aider's diff edit format; the repairs never apply) is not a
comparison worth maintaining integration for. The sections above
and the BENCHMARKS.md rows stay — records are append-only — and
`extract_metrics.py` still reads the recorded aider usage reports,
so the 2026-10-12__20-18-12 run remains reproducible from its
package tarballs (build commands preserved in git history at the
removal commit).

## Full 6-task suite (2026-10-17, deepseek-flash, commit aaa8958)

Run `2026-10-17__04-47-51`, one trial per task, `--n-concurrent 1`,
alongside the first orchestration-suite run in the same umbrella invocation
(`bench/run-all.sh`). The coding task list and adapter are unchanged from the
earlier sections; this run is the current smoke signal for the benchmark PR.

| Task | Resolved | Time | in | out | cached |
|---|---|---|---|---|---|
| assign-seats | yes | 36s | 205,256 | 5,302 | 187,776 |
| broken-python | yes | 29s | 168,527 | 3,929 | 151,424 |
| csv-to-parquet | yes | 44s | 246,339 | 3,255 | 228,864 |
| gomoku-planner | yes | 126s | 516,365 | 21,248 | 494,336 |
| hello-world | yes | 5s | 47,345 | 150 | 32,512 |
| jsonl-aggregator | yes | 23s | 84,797 | 1,798 | 68,864 |

**Score: 6/6.** Totals: 1,268,629 prompt + 35,682 output = 1,304,311
tokens, 1,163,776 cached (92% of prompt tokens). No sub-agent usage on
this suite; every task stayed single-lane.

## Hermes comparison — same 6 coding tasks (2026-10-17)

Same task set, dataset pin, one-shot harness, model (`deepseek-flash`), and
`--n-concurrent 1`. yaah run `2026-10-17__04-47-51`; Hermes run
`hermes-coding` (hermes-agent 0.19.0, `hermes_agent:HermesAgent`). One trial
each. Hermes prompt tokens are normalized to yaah semantics: `input_tokens +
cache_read_tokens` (see `extract_metrics.py`).

| Task | yaah resolved | yaah in/out | yaah cached | yaah time | Hermes resolved | Hermes in/out | Hermes cached | Hermes time |
|---|---|---|---|---|---|---|---|---|
| hello-world | yes | 47,345 / 150 | 32,512 | 5s | yes | 22,349 / 78 | 21,888 | 14s |
| broken-python | yes | 168,527 / 3,929 | 151,424 | 29s | yes | 197,570 / 2,539 | 183,552 | 42s |
| csv-to-parquet | yes | 246,339 / 3,255 | 228,864 | 44s | yes | 81,451 / 932 | 70,272 | 31s |
| jsonl-aggregator | yes | 84,797 / 1,798 | 68,864 | 23s | yes | 87,750 / 1,730 | 85,504 | 35s |
| assign-seats | yes | 205,256 / 5,302 | 187,776 | 36s | yes | 101,115 / 1,754 | 87,680 | 27s |
| gomoku-planner | yes | 516,365 / 21,248 | 494,336 | 126s | yes | 1,008,219 / 12,855 | 982,528 | 615s |

**Totals**

| Metric | yaah | Hermes | Delta |
|---|---|---|---|
| Resolved | 6/6 | 6/6 | — |
| Prompt tokens | 1,268,629 | 1,498,454 | +18% |
| Output tokens | 35,682 | 19,888 | -44% |
| Cached prompt | 1,163,776 (92%) | 1,431,424 (96%) | — |
| Agent wall time | 263s | 764s | 2.9× |
| Mean tokens/task (in/out) | 211,438 / 5,947 | 249,742 / 3,315 | — |

**Excluding the image task (`gomoku-planner`)**

| Metric | yaah | Hermes |
|---|---|---|
| Prompt tokens | 752,264 | 490,235 (-35%) |
| Output tokens | 14,434 | 7,033 (-51%) |
| Wall time | 137s | 149s (+9%) |
| Cached prompt | 669,440 (89%) | 448,896 (92%) |

Caveats: single trial per task; `gomoku-planner` dominates both totals and is
a vision-adjacent task where Hermes spent 615s and 1.0M prompt tokens. Across
the five non-image tasks Hermes was token-cheaper but marginally slower, while
yaah was faster on the image task by 5×. Do not read these as a general
agent-quality ranking; they are one sampled run per task.

Raw Hermes usage reports live under each trial's
`agent-logs/hermes-usage.json`; run them through
`extract_metrics.py <runs-dir> hermes-coding` to reproduce the table.

## Full 6-task suite (2026-10-20, deepseek-flash, commit 47f14c7)

Same task set, dataset pin, one-shot harness, and model as the 2026-10-17
run. yaah `v0.88.1` (commit `47f14c7`): includes the orchestration-lane work
(cache-inclusive compaction gate, evidence guard, supervision fixes) but
nothing that touches the coding-task path. Run `2026-10-20__15-33-23`,
`N_CONCURRENT=2` — the first suite run since the minimal-orchestra split
kept the harness at two parallel trials instead of one. One trial each.

| Task | Resolved | in/out | cached | time |
|---|---|---|---|---|
| hello-world | yes | 45,375 / 137 | 31,232 | 4s |
| broken-python | yes | 144,980 / 4,891 | 127,616 | 34s |
| csv-to-parquet | yes | 162,615 / 2,349 | 147,072 | 48s |
| jsonl-aggregator | yes | 84,310 / 2,825 | 68,480 | 27s |
| assign-seats | yes | 155,009 / 4,522 | 139,136 | 28s |
| gomoku-planner | yes | 716,370 / 27,802 | 695,680 | 201s |

**Totals**: 6/6 resolved; 1,308,659 prompt + 42,526 output; cached
1,209,216 (92%); agent wall time 342s.

Comparison against the 2026-10-17 run (`aaa8958`): 6/6 again, prompt tokens
+3% (1,308,659 vs 1,268,629), output tokens +19% (42,526 vs 35,682), wall
time +30% (342s vs 263s) — within single-trial variance; `gomoku-planner`
alone accounts for 716K prompt and 201s of it in both runs. No regression
signal from the 0.87.x→0.88.x supervision/lane work on the coding path.

## Quick Reference card removed (2026-10-20, commit 5d927a7 + trim, bead yaah-w8x)

The system prompt carried a quick-reference card re-rendering every
registered tool's name, signature, and description — a full duplicate of the
request's `tools` array, ~3.4K tokens per call. Removed outright (zero
information loss; the schemas are the single source of tool truth on the
wire).

Two full-suite trials after the trim, both 5/5 resolved:

| Task | pre-trim in | trial 1 in | trial 2 in |
|---|---|---|---|
| hello-world | 45,375 | 33,924 | 33,920 |
| broken-python | 190,459 | 233,161 | 153,071 |
| csv-to-parquet | 165,090 | 181,131 | 138,856 |
| jsonl-aggregator | 161,890 | 80,332 | 131,039 |
| assign-seats | 161,274 | 258,448 | 93,880 |
| **Total** | **724,088** | **786,996** | **565,069** |

(The pre-trim per-task column sums to 724,088; the tracking table above
carries 724,083 from the extractor's own aggregate — a 5-token difference
in how the run's session totals were read, not a data change.)

Reading: `hello-world` is the clean instrument — two API calls, minimal
variance — and it dropped exactly the predicted −25% (45.4K → 33.9K/33.1K,
−11.4K across two calls). Trial 2 improved every task (−26% total, 98%
cached, 125s). Trial 1's aggregate regression is one task misbehaving
(`assign-seats` spent 10.8K output tokens across many more turns — a turn-
count outlier, not a floor change; its per-call floor is still trimmed).
Resolution held at 5/5 in all three trials.

Trial 3 also removes the deprecated `sed` tool (superseded by `replace`):
one fewer schema in every request, one fewer role-file entry, and the
`reviewer` role becomes genuinely read-only, which routes it to the shared
lane under the yaah-7yf derivation instead of paying for a worktree.

Verdict: floor cut confirmed; trimmed-run totals 787K / 536K / 565K vs
724K pre-trim (mean 630K, −13%), with trial 3 the first to also carry the
sed removal. Keep both trims; the tracking table carries all three trials
until more runs accumulate.

## Baseline smoke + common metrics (2026-10-29, deepseek-flash)

First run using the shared `terminal-bench.metrics.v1` extractor and
[pricing.json](pricing.json). This is a **baseline**, not a capability
comparison: two tasks, one trial, off-peak pricing.

- Harness pin: `d28711d`
- yaah: `53407d7`, run `2026-10-29__11-39-32`
- hermes-agent 0.19.0, run `2026-10-29__11-42-33`
- opencode v2.1.3: hello-world `2026-10-29__12-11-43`,
  broken-python `2026-10-29__12-12-27` (one OpenCode run per task)
- Model: `deepseek-flash`; effort: default; price tier: off-peak

| Task | yaah | hermes | opencode |
|---|---|---|---|
| hello-world | resolved | resolved | resolved |
| broken-python | resolved | resolved | resolved |
| **Resolved** | **2/2** | **2/2** | **2/2** |

| Metric | yaah | hermes | opencode |
|---|---:|---:|---:|
| Input, uncached | 25,066 | 24,268 | 7,704 |
| Output | 5,968 | 2,245 | 2,031 |
| Reasoning | n/a | 0 | 1,615 |
| Cache read | 196,352 | 141,440 | 68,608 |
| Steps | 13 | 13 | 10 |
| Agent wall time | 52s | 50s | 34s |
| Cost (off-peak) | $0.0079 | $0.0054 | $0.0035 |

The OpenCode totals sum the two per-task smoke runs; the adapter was exercised
one task per invocation. OpenCode's durable `session_v2` row is authoritative
(the CLI can exit before the final `step_finish` reaches stdout); the stream is
the fallback. The normalized columns keep cache separate from input;
`tokens_in` in the legacy totals still includes cache. Cost uses the shared price table, not an
arm's self-reported price. All runs came from run metadata with no model field,
so the extractor used its documented `TB_MODEL` fallback (`deepseek-flash`).

Raw metrics are emitted by `extract_metrics.py --json` for the run IDs above;
append future run IDs here rather than rewriting this section.

## Fresh three-way comparison — current 5-task suite (2026-10-29)

Same day, same five-task `suite.txt`, same model (`deepseek-flash`), same
Terminal-Bench pin (`d28711d`), one trial per task, `--n-concurrent 1`. All
three agents ran in fresh workspaces with their own agent-logs. This is the
first three-way run on the **current** five-task suite (the 2026-10-12
three-way used the older six-task list including `gomoku-planner`).

- yaah: commit `8eb063f` (`v0.118.2`), run `2026-10-29__18-40-18`
- Hermes: `hermes-agent 0.19.0`, run `2026-10-29__18-46-16`
- OpenCode: `opencode 2.1.3`, run `2026-10-29__18-52-00`

| Task | yaah | Hermes | OpenCode |
|---|---|---|---|
| hello-world | resolved 7s | resolved 15s | resolved 4s |
| broken-python | resolved 49s | resolved 41s | resolved 37s |
| csv-to-parquet | resolved 40s | resolved 61s | resolved 37s |
| jsonl-aggregator | resolved 51s | resolved 35s | resolved 21s |
| assign-seats | resolved 37s | resolved 24s | resolved 35s |
| **score** | **5/5** | **5/5** | **5/5** |
| **agent time** | **184s** | **176s** | **134s** |

Token accounting, normalized to inclusive prompt tokens (uncached input +
cache read). OpenCode reports reasoning separately; yaah and Hermes do not
split it out. Costs are the shared `pricing.json` off-peak rates.

| Metric | yaah | Hermes | OpenCode |
|---|---:|---:|---:|
| Resolved | 5/5 | 5/5 | 5/5 |
| Prompt tokens (incl cache) | 759,265 | 423,427 | 300,316 |
| Uncached input | 13,665 | 39,427 | 16,668 |
| Output | 19,498 | 6,977 | 6,823 |
| Reasoning | — | — | 4,213 |
| Cache read | 745,600 (98.2%) | 384,000 (90.7%) | 283,648 (94.4%) |
| Agent time | 184s | 176s | 134s |
| Estimated cost | $0.0160 | $0.0113 | $0.0100 |
| Steps (sum) | 46 | 34 | 40 |

Reading:

- All three resolve every task. OpenCode's old `broken-python` parse error
  (2/2 runs on 2026-10-12) did not reproduce on 2.1.3; it resolved in 37s.
- OpenCode is the cheapest and fastest on this trial. Its 300K prompt total
  is 40% of yaah's and 71% of Hermes's. It made 40 steps, between Hermes's 34
  and yaah's 46, so the token advantage is smaller prompts per turn, not fewer
  turns. yaah has the highest cache-hit ratio (98.2%) and absolute cache read;
  OpenCode is 94.4% and Hermes 90.7%.
- Hermes has the smallest reported output (6,977). OpenCode reports 6,823
  output plus 4,213 reasoning, so its completion + reasoning total is 11,036;
  yaah's 19,498 output is the largest. Cost includes OpenCode's reasoning.
- Single trial per CLI per task. These are smoke signals, not a statistical
  ranking; do not infer an agent-quality ordering from one run.

Reproduce with the adapters in this directory:

```bash
PYTHONPATH=bench/terminal-bench YAAH_BIN=/tmp/yaah-bin/yaah-threeway \
  ../terminal-bench/.venv/bin/tb run --agent-import-path yaah_agent:YaahAgent \
  -t hello-world -t broken-python -t csv-to-parquet -t jsonl-aggregator \
  -t assign-seats --dataset-path ../terminal-bench/original-tasks \
  --output-path /tmp/tb-threeway/yaah --n-concurrent 1
```

The same task list with `hermes_agent:HermesAgent` (`HERMES_PKG_DIR`) and
`opencode_agent:OpenCodeAgent` (`OPENCODE_BIN`) produces the other two arms.
Token/cost totals come from `extract_metrics.py --json <runs-dir> <run-id>`.
