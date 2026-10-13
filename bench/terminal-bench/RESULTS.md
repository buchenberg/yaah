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
