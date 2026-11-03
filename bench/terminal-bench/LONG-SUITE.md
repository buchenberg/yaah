# Long-horizon suite results

One row per recorded run on [`long-suite.txt`](long-suite.txt). This is a
separate scoreboard from [RESULTS.md](RESULTS.md): the denominator is 2,
and both tasks are deliberately long and outlier-prone, so do not blend
these rows into the five-task totals.

## 2026-11-02 three-way

Per task:

| Agent | Task | Resolved | Failure | Prompt in | Out | Reasoning | Cache % | Steps | Time | Cost |
|---|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|
| yaah | gomoku-planner | 1/1 | — | 151,307 | 11,568 | — | 97% | 10 | 68s | $0.0160 |
| yaah | path-tracing | 1/1 | agent_timeout | 2,554,716 | 81,526 | — | 89% | 50 | 2100s | $0.1935 |
| hermes | gomoku-planner | 1/1 | — | 1,548,915 | 25,857 | 18,512 | 50% | 29 | 478s | $0.2917 |
| hermes | path-tracing | 1/1 | agent_timeout* | — | — | — | — | — | 2100s | — |
| opencode | gomoku-planner | 1/1 | — | 165,171 | 2,369 | 13,461 | 98% | 14 | 108s | $0.0105 |
| opencode | path-tracing | 1/1 | — | 8,738,628 | 37,158 | 67,696 | 99% | 91 | 1433s | $0.0988 |

Totals:

| Agent | Resolved | Prompt in | Out | Reasoning | Cache % | Steps | Time | Cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| yaah | **2/2** | 2,706,023 | 93,094 | — | 90% | 60 | 2168s | **$0.2095** |
| hermes | **2/2** | 1,548,915* | 25,857* | 18,512* | 50%* | 29* | 2578s | **≥$0.2917*** |
| opencode | **2/2** | 8,903,799 | 39,527 | 81,157 | 99% | 105 | 1541s | **$0.1093** |

`*` Hermes's `path-tracing` hit the agent timeout before its usage file was
written. The resolved verdict and wall time are known; prompt, output,
reasoning, cache, steps, and cost cover only `gomoku-planner`, so the
long-suite totals are a lower bound.

Reading:

- **All three arms resolved both long-horizon tasks.** `path-tracing` is
  the expensive one: yaah and Hermes hit the agent timeout after leaving a
  passing `image.c`, while OpenCode finished inside the budget with no
  timeout. All five parser checks passed for all three.
- **OpenCode is cheapest and fastest on this suite** — 1541s and $0.1093
  for 105 steps. Its 8.9M prompt tokens are 99% cached, so the huge prompt
  volume costs far less than it looks. yaah is 1.9× the cost and 1.4× the
  wall time; Hermes is at least 2.7× OpenCode on `gomoku` alone, with its
  path-tracing cost unrecorded.
- **`gomoku-planner` is high-variance.** This run: yaah 10 steps / 68s /
  $0.0160, OpenCode 14 / 108s / $0.0105, Hermes 29 / 478s / $0.2917.
  Earlier gomoku-only runs saw yaah at 15–16 steps / 212–245s and Hermes
  at 18 steps / 116s, so do not turn one trial into a ranking.
- **The long suite is a wall-clock and cost outlier.** yaah's two long
  tasks are 2168s and $0.2095; the same run's five main-suite tasks are
  125s and $0.0215 (~17× the time, ~10× the cost). OpenCode's two long
  tasks are 1541s and $0.1093; its five main-suite tasks are 231s and
  $0.0193 (~7× the time, ~6× the cost). Keep them on their own scoreboard
  and run them when long-horizon behavior matters.
- **The same combined run also resolved all five main-suite tasks for
  every arm.** Those results stay on the main scoreboard in RESULTS.md;
  this file records only the gomoku + path-tracing subset.

Caveats: one trial per arm; all costs are peak-tier. The 2026-11-02 run
was a combined seven-task harness run, so the run IDs also cover the main
five tasks; the long-suite rows above are the two-task subset. The yaah
arm is `0fa67b0+seven-task` (the bench-config diff does not change the
binary); Hermes is 0.22.5; OpenCode is 2.1.3.
