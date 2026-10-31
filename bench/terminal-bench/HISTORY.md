# Terminal-Bench history and detail

Narrative archive: methodology, per-task breakdowns, run-over-run
analysis, and the caveats behind each recorded run. The single
canonical results table — one row per run, by date — lives in
[RESULTS.md](RESULTS.md), and that row is the only *record* of a run's
totals. Every run gets its row there first; the sections here analyze
and may quote figures, but are never the source of truth for them, and
a table here must not be maintained as a second ledger.

The adapter, host setup, and run instructions live in
[README.md](README.md). Records here are append-only — new runs get a
new section, existing sections are never rewritten.

## Column semantics for the RESULTS.md table

- `Resolved` is out of the suite length that run used. The suite was 15
  tasks, then 6, then 5 from 2026-10-17 on, so only rows sharing a
  denominator are comparable. `gomoku-planner` left the suite on
  2026-10-20 (vision-adjacent outlier: ≈40% of tokens, 2/3 of wall
  time); the five-task rows before that date are recomputed from the
  recorded per-task tables in the sections below.
- `Prompt in` includes cache reads. `Cache %` is the cache-read share of
  it, so uncached input is `Prompt in × (1 − Cache %)`.
- `Out` excludes `Reasoning` where the arm reports reasoning separately.
  OpenCode always does; Hermes does from 0.22.5 on (2,762 on the repack
  run, 4,174 on the three-way). yaah and Hermes ≤0.19.0 do not split it
  out, so their `Reasoning` is blank, not zero. `Cost` includes
  reasoning in every case.
- `Cost` is USD at `pricing.json` off-peak rates, never an arm's
  self-reported price.
- `Steps` is assistant turns. `Time` is summed per-task agent wall time.
- Model is `deepseek-flash` and the harness pin is `d28711d` for every
  row. One trial per task unless the version says otherwise.

The 2026-10-20 hermes run failed `assign-seats` on `has_correct_pairs` —
the same task family it failed on 2026-10-12 (broken-python then);
single-trial variance is real.

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

## Hermes repacked to 0.22.5 (2026-10-31, deepseek-flash)

PyPI still serves 0.19.0 (July), but Hermes's release channel (GitHub
tags, installer, `nousresearch/hermes-agent:stable`) is at v0.22.5
(2026-10-23). Every prior Hermes row above benched 0.19.0, so the arm
was ~3 months behind what a user installing Hermes today runs. This
run repins the arm to the v0.22.5 tag (commit `bf0203a`).

The repack is not a version bump of the old recipe — 0.22.x changed
packaging fundamentally:

- **Python 3.14 is the only runtime**: every core dependency in
  `pyproject.toml` carries a `python_version >= '3.14'` marker
  (<3.14 installs resolve to zero deps — an updater bridge only).
  The task images' 3.12/3.13 cannot host it, so the tarball now
  bundles a CPython 3.14 runtime (from `python:3.14-slim-bookworm`,
  glibc 2.36 — the oldest glibc any task image uses, so the bundled
  binaries run on all of them) plus the non-glibc shared libs the
  interpreter links outside its tree (sqlite3, ssl, ffi, ...).
- **No PyPI wheel**: the wheel/sdist build is guarded ("distributed
  via the shell installer, Docker image, or Nix"). The tarball uses
  their supported source-checkout layout: the v0.22.5 tree at
  `/opt/hermes/repo` (tests/apps/website stripped, `.git` kept so
  version resolution works), an editable install, and the dependency
  set exported from their own `uv.lock` (plain pip cannot resolve it —
  e.g. browser-harness's pillow pin conflicts without uv overrides).
  An `install-stamp.json` records the pin; `hermes --version`
  self-reports `v0.22.5 (terminal-bench repack)`.
- **One tarball** (`hermes-pkg-py314.tar.gz`) replaces the per-minor
  pair; the container's own Python is never used. Build recipe lives
  in `hermes_agent.py`; the install script sets `LD_LIBRARY_PATH` and
  execs the bundled interpreter.

The bench contract survived intact: `-z`, `--yolo`, `-m`,
`--usage-file` all still exist (verified in-tree), and the usage
report keeps the extractor's semantics (`input_tokens` excludes cache
reads). New in the report: `reasoning_tokens` (now surfaced in
metrics.v1) and an `auxiliary` bucket — title generation adds a
~240-input-token call per task that the top-level counts (and thus
our extractor) exclude. Also new per the release notes: `hermes -z`
exits non-zero on failure, and unattended sessions hard-stop after 5
identical tool replays with a 600s no-progress watchdog.

Run `2026-10-31__13-49-26`: same five-task `suite.txt`, same Terminal-Bench
pin (`d28711d`), same model (`deepseek-flash`), `--n-concurrent 1`, one
trial. (Smoke first: `2026-10-31__13-48-07`, hello-world only.)

| Task | Resolved | Time | in (incl cache) | out | reasoning | cached | steps |
|---|---|---|---|---|---|---|---|
| hello-world | yes | 19s | 50,792 | 78 | 0 | 25,216 (50%) | 2 |
| broken-python | yes | 45s | 230,939 | 2,843 | 838 | 108,800 (47%) | 8 |
| csv-to-parquet | yes | 49s | 232,887 | 1,344 | 338 | 109,824 (47%) | 9 |
| jsonl-aggregator | yes | 51s | 208,975 | 2,376 | 361 | 103,040 (49%) | 7 |
| assign-seats | yes | 42s | 235,376 | 2,830 | 1,225 | 116,224 (49%) | 8 |

Against the 0.19.0 arm of the 2026-10-29 three-way (same suite, model,
pin; single trial each):

| Metric | hermes 0.19.0 | hermes 0.22.5 | Delta |
|---|---:|---:|---|
| Resolved | 5/5 | 5/5 | — |
| Prompt tokens (incl cache) | 423,427 | 958,969 | +127% |
| Uncached input | 39,427 | 495,865 | +1158% |
| Output | 6,977 | 9,471 | +36% |
| Reasoning | n/a | 2,762 | new |
| Cache read | 384,000 (90.7%) | 463,104 (48.3%) | — |
| Steps (sum) | 34 | 34 | — |
| Agent wall time | 176s | 206s | +17% |
| Cost (off-peak) | $0.0113 | $0.0831 | 7.4x |

Reading: resolution and step count are identical — the delta is cost,
not capability. The same 34 assistant turns carry 2.3x the prompt
tokens, and the cache-hit ratio halved (91% to 48%), so uncached
input grew ~12.6x. Whether that is a larger fixed prompt per turn, a
cache-unfriendly prompt/tool ordering on deepseek, or both, the run
data does not say; per-turn `in` is roughly uniform across tasks
(~30K/turn vs ~12.5K/turn on 0.19.0). Caveats: single trial;
the 2026-10-29 yaah/opencode rows remain at their recorded versions
(yaah 8eb063f, opencode 2.1.3), so no same-day three-way at equal
recency exists — this is a Hermes-arm repin, not a fresh ranking.

## Context/token audit of the 2026-10-29 three-way, and the tool-gate trim (2026-10-31)

An audit of the three-way above asked where yaah's tokens actually go,
on the theory that the headline prompt total (759,265 — 2.5× OpenCode's)
was the thing to fix. It is not. Decomposing each arm's cost at
`pricing.json` off-peak rates:

| Cost component | yaah | Hermes 0.19.0 | OpenCode |
|---|---:|---:|---:|
| Uncached input | $0.00205 (13%) | $0.00591 (53%) | $0.00250 (25%) |
| Cache read | $0.00224 (14%) | $0.00115 (10%) | $0.00085 (9%) |
| **Output** | **$0.01170 (73%)** | $0.00419 (37%) | $0.00662 (66%) |
| Total | $0.0160 | $0.0113 | $0.0100 |

**85% of yaah's cost gap with OpenCode is output tokens, not prompt
volume.** yaah's prompt side costs $0.0043 against OpenCode's $0.0034
despite sending 2.5× the tokens, because 98.2% of them are cache reads
at $0.003/M.

yaah's cache discipline is the best of the three and should not be
"fixed": it injects only **~297 uncached tokens per step** (13,665 over
46 steps). Hermes 0.19.0 injects ~1,160/step — it mutates its prefix.
Hermes 0.22.5 is worse still (48.3% cache-hit, above).

Where the prompt volume *does* matter is latency and window headroom, so
the fixed per-call prefix was measured directly. Using `hello-world` as
the clean instrument (3 calls, negligible history) and solving for the
floor:

| Per-call floor | yaah | Hermes 0.19.0 | OpenCode |
|---|---:|---:|---:|
| tokens/call | ~13,400 | ~11,200 | ~5,500 |

Instrumenting the registry (a temporary test, since removed) attributed
that floor: the system prompt is 8,363 chars (~2.1K tokens) and the
**tool array is the other ~84%**. 42 tools, 53,234 bytes of function
envelope:

| Category | Tools | Bytes | Share |
|---|---:|---:|---:|
| orchestration (`delegate`, `proposal`, `subagent_jobs`, `supervisor`, `apply_out`, `role`, `list_subagents`) | 7 | 15,268 | 28.7% |
| core (filesystem/shell/search) | 25 | 25,416 | 47.7% |
| memory (`memory_*`) | 6 | 7,029 | 13.2% |
| Go toolchain (`go_test`, `go_mod`, `go_outline`, `go_refactor`, `staticcheck`) | 5 | 6,386 | 12.0% |

Two of those four categories were provably dead weight in the benched
configuration, and both are now gated.

### The trim: Go-toolchain gate + one-shot memory gate

1. **Go toolchain tools outside a Go module.** `availability.go` already
   carried the policy — *"A workspace that is not a Go module cannot use
   them, so shipping their schemas to it is pure per-call cost"* — and
   `runner.go`'s `subAgentUnavailableTools` already applied it, but only
   on the **sub-agent** path. The main session went through
   `NewRegistryWithBelt`, which registered all 25 leaf tools ungated.
   `GoToolAvailable` had exactly one non-test call site. None of the five
   suite tasks contains a `go.mod`. The gate now runs in `wiring.go` too,
   via a new exported `GoToolchainLeafTools()` derived from the same list
   `IsGoToolchainTool` reads (pinned against drift by
   `TestGoToolchainLeafTools`).

   **Resolved once at wiring time and never re-checked.** The tools array
   is part of the cached prefix, so a set that flipped mid-session (the
   agent runs `go mod init`) would invalidate the cache on every later
   call and cost far more than the schemas save.

2. **Memory tools in one-shot sessions.** A one-shot run is a single
   non-interactive turn: there is no later conversation to recall into,
   and the store a CI or bench one-shot writes is discarded with its
   container. The schemas are per-call cost, and the audit found the
   model *had* called `memory_add` during the 2026-10-29 run (540 chars
   of output spent on a note nobody will read). Gated on
   `db != nil && !opts.OneShot`, following the existing `question`
   precedent.

   The `## Memory Guidelines` prompt block is gated on the **same**
   value, so the prompt cannot advertise an unregistered tool. That in
   turn exposed a pre-existing violation: `identity.md`'s "Memory from
   findings" bullet hard-named `memory_add`, so it advertised the tool
   even in sessions without it. Reworded to be tool-agnostic.

Observed in the session records: `system_prompt` 6,687 → 5,314 chars, and
`workspace_omitted_tools` names all 11 dropped tools, so "the model
didn't" stays distinguishable from "the model couldn't".

### Two verification trials, both 5/5

Same five-task `suite.txt`, pin `d28711d`, `deepseek-flash`,
`--n-concurrent 1`, one trial each. Binary built from `728b016` plus the
uncommitted gating diff. Runs `2026-10-31__14-17-28` (trial 1) and
`2026-10-31__14-26-16` (trial 2, run immediately after so the trimmed
prefix was already warm in DeepSeek's cache).

| Metric | baseline `8eb063f` | trial 1 | trial 2 |
|---|---:|---:|---:|
| Resolved | 5/5 | 5/5 | 5/5 |
| Prompt tokens (incl cache) | 759,265 | 604,143 | 475,754 |
| Uncached input | 13,665 | 29,167 | 18,666 |
| Output | 19,498 | 14,908 | 16,384 |
| Cache read | 745,600 (98.2%) | 574,976 (95.2%) | 457,088 (96.1%) |
| Steps (sum) | 46 | 46 | 36 |
| Agent wall time | 184s | 158s | 134s |
| Cost (off-peak) | $0.0160 | $0.0150 | $0.0140 |

`hello-world`, the deterministic instrument (3 steps in all three runs):

| | baseline | trial 1 | trial 2 |
|---|---:|---:|---:|
| in | 40,456 | 31,798 | 31,808 |
| cached | 39,808 | 31,232 | 31,232 |
| out | 159 | 136 | 135 |

Reading:

- **The floor fell 13,394 → 10,508 tokens/call, −2,886 (−21.5%).**
  Cross-checked against the cache-read delta, which is pure prefix:
  (39,808 − 31,232)/3 = **2,859 tokens/call**. Two independent
  derivations agree. `cached` is byte-identical across both trials
  (31,232), confirming the trimmed prefix is stable — the property the
  cache depends on.
- **Trial 1 is the clean aggregate comparison**, because its step count
  matches the baseline exactly (46 = 46): prompt tokens −20.4%, against
  the −17.5% predicted from floor × steps (46 × 2,886 = 132,756 off
  759,265). Trial 2 drew 36 steps — behavioural variance, not the gate —
  so its −37.3% prompt must **not** be attributed to the trim.
- **Cost fell 5.9% (trial 1) and 12.5% (trial 2)**, far less than the
  prompt reduction, for the reason the decomposition above gives: the
  trimmed tokens were cache reads at $0.003/M. The win is latency
  (−14% / −27% wall time) and window headroom, not spend.
- **The cache-hit ratio falling 98.2% → 95-96% is mechanical, not a
  regression.** The cached component (the floor) shrank while per-turn
  new content did not, so the ratio drops even though nothing got less
  cacheable. `hello-world`'s absolute uncached input is flat across all
  three runs (648 → 566 → 576), which is the number that matters.
- **Trial 1's uncached-input spike (13,665 → 29,167) is partly a
  one-time prefix-change cost.** DeepSeek keys its cache on the prefix,
  so the first run after a prompt edit re-writes it; trial 2 fell back to
  18,666. Expect any future prompt-prefix change to show the same
  single-run artifact — do not read it as a regression.

Caveats: single trial per task per configuration; the baseline row is the
2026-10-29 run at `8eb063f`, two days older than the trials, so
run-over-run model variance is not controlled for. Step counts vary
between trials (46/46/36), which is why the floor measurement — not the
aggregate — is the load-bearing result.

### Output-token findings (not acted on)

The audit measured where yaah's 19,498 output tokens went, since output
is 73% of its cost:

- 58 tool calls across 46 assistant messages — **1.26 calls per message**
  (histogram: 30× single, 8× double, 3× quad), against a system prompt
  whose first section is "## Cardinal rule: batch tool calls". Hermes
  needed 34 steps for the same five tasks.
- Tool-call arguments are 34,246 chars vs 17,565 chars of prose (1.9×),
  and **`bash` arguments alone are 76.3%** of them (26,138 chars over 51
  calls) — whole Python programs written through heredocs.
- On the data tasks yaah writes a **second independent implementation to
  cross-check** the first (jsonl-aggregator: "Independent impl: Decimal
  sums, plain dicts, different sort approach"; assign-seats: `solve.py`
  plus `verify.py`). This roughly doubles output on those tasks and is
  plausibly load-bearing for the 5/5 record — it is flagged as a
  tradeoff to decide deliberately, not optimized blindly.

Three items are deferred rather than encoded here, since each needs a
decision the run data cannot make: a lean one-shot orchestration profile
(28.7% of the schema payload, and `sub_tokens_in`/`sub_tokens_out` were
both 0 on every task in this suite — but it must be validated against
`../orchestration-tasks` first); output verbosity (the largest cost
lever, entangled with the verification behaviour above); and batching
compliance (a model-adherence problem, not harness structure).

## Fresh three-way at equal recency — yaah gated (2026-10-31)

The first same-day three-way in which every arm runs a current build:
yaah with the tool gates above, Hermes at the repacked 0.22.5, OpenCode
at 2.1.3. This closes the gap the Hermes-repin section flags ("no
same-day three-way at equal recency exists").

Same five-task `suite.txt`, same Terminal-Bench pin (`d28711d`), same
model (`deepseek-flash`), `--n-concurrent 1`, one trial per task per
arm, all three arms run within ~15 minutes of each other.

- yaah: `728b016` + the uncommitted tool-gating diff, run `2026-10-31__15-15-09`
- Hermes: `hermes-agent 0.22.5` (py314 repack), run `2026-10-31__15-20-11`
- OpenCode: `opencode 2.1.3`, run `2026-10-31__15-26-47`

| Task | yaah | Hermes | OpenCode |
|---|---|---|---|
| hello-world | resolved 4s | resolved 20s | resolved 5s |
| broken-python | resolved 32s | resolved 53s | resolved 30s |
| csv-to-parquet | resolved 22s | resolved 45s | resolved 59s |
| jsonl-aggregator | resolved 37s | resolved 49s | resolved 17s |
| assign-seats | resolved 36s | resolved 52s | resolved 29s |
| **score** | **5/5** | **5/5** | **5/5** |
| **agent time** | **131s** | **219s** | **140s** |

| Metric | yaah | Hermes | OpenCode |
|---|---:|---:|---:|
| Resolved | 5/5 | 5/5 | 5/5 |
| Prompt tokens (incl cache) | 483,476 | 1,013,556 | 268,208 |
| Uncached input | 9,748 | 513,844 | 15,408 |
| Output | 14,265 | 11,759 | 5,948 |
| Reasoning | — | 4,174 | 3,800 |
| Cache read | 473,728 (98.0%) | 499,712 (49.3%) | 252,800 (94.3%) |
| Steps (sum) | 38 | 34 | 38 |
| Prompt per step | 12,723 | 29,810 | 7,058 |
| Agent wall time | 131s | 219s | 140s |
| Sub-agent tokens | 0 | 0 | 0 |
| **Estimated cost** | **$0.0114** | **$0.0881** | **$0.0089** |

Cost decomposed at `pricing.json` off-peak rates, reasoning billed at
the output rate (this reproduces the extractor's totals exactly):

| Cost component | yaah | Hermes | OpenCode |
|---|---:|---:|---:|
| Uncached input | $0.00146 (12.8%) | $0.07708 (87.5%) | $0.00231 (25.9%) |
| Cache read | $0.00142 (12.4%) | $0.00150 (1.7%) | $0.00076 (8.5%) |
| Output (+reasoning) | $0.00856 (74.8%) | $0.00956 (10.8%) | $0.00585 (65.6%) |
| Total | $0.0114 | $0.0881 | $0.0089 |

Per-call floor, from `hello-world` (3 calls, negligible transcript —
the deterministic instrument):

| Floor per call | yaah | Hermes | OpenCode |
|---|---:|---:|---:|
| tokens/call | ~10,500 | ~25,400 | ~5,400 |

Reading:

- **The ordering on prompt volume flipped.** On 2026-10-29 yaah was the
  most prompt-heavy arm (759K vs Hermes 423K vs OpenCode 300K). It is
  now the middle arm (483K vs Hermes 1,014K vs OpenCode 268K) — Hermes
  0.22.5 more than doubled its prompt total while yaah's fell 36%.
- **yaah is now the fastest of the three** (131s), where it was the
  slowest on 2026-10-29 (184s vs Hermes 176s vs OpenCode 134s). It also
  drew fewer steps this run (38 vs 46), so part of that is variance and
  part is the smaller prefix; the two cannot be separated at one trial.
- **yaah's cache discipline is now best-in-class by a wide margin** —
  98.0% against OpenCode's 94.3% and Hermes's 49.3%. Its uncached input
  (9,748) is the lowest of the three and the lowest yaah has ever
  recorded on this suite. Hermes 0.22.5's uncached input is 513,844 —
  53× yaah's — which is 87.5% of its bill and the whole story of its
  7.7× cost.
- **The cost gap to OpenCode narrowed from 1.60× to 1.28×** ($0.0160 →
  $0.0114 against $0.0100 → $0.0089). Output is still yaah's dominant
  term at 74.8% of spend, unchanged in character from the audit above —
  the gates trimmed the floor, and the floor was never where the money
  was.
- **The gate removed no usable capability.** Across all five tasks the
  model called only `bash`, `write`, `read` and `ls`: zero calls to any
  gated `go_*`/`staticcheck` tool, and zero `memory_add` calls (the
  2026-10-29 run spent 540 output chars on one). Tool-call arguments are
  now 100% work-bearing.
- **The floor measurement is reproducible across three independent
  runs**: `hello-world` `cached` is 31,232 in all of them (trials 1 and 2
  above, and this run), with `in` 31,798 / 31,808 / 31,798. A stable
  cached figure is the property the provider cache depends on, so this
  is the load-bearing evidence that the trim did not make the prefix
  less cacheable.
- **Batching is still poor and unchanged**: 45 tool calls over 38
  assistant messages = 1.18 calls/message (histogram 23× single, 9×
  double, 1× quad), against 1.26 before the gates. Tracked as
  `yaah-pybi`; the gates were never going to move it.

Cross-run comparison for yaah, normalized for step count, since step
draws vary (46 on 2026-10-29, 46 / 36 / 38 across today's three runs):
prompt per step fell **16,506 → 12,723 (−22.9%)**, which matches the
−21.5% floor reduction measured directly on `hello-world`. The two
independent derivations agreeing is the strongest evidence here that the
aggregate movement is the gate and not the step draw.

Caveats: single trial per task per arm; yaah's arm is an uncommitted
diff on `728b016`, so it is cited as `728b016+tool-gating` in the
tracking table and is not reproducible from a commit alone until that
diff lands. Hermes's and OpenCode's totals are one trial each at their
recorded versions. `subagent_tokens` is 0 for all three arms, so this
suite says nothing about orchestration cost — see `../orchestration-tasks`.
