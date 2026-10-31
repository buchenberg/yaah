# Benchmarks

yaah's benchmark harness lives in [`bench/terminal-bench/`](./bench/terminal-bench/).
Run totals are recorded in one table in
[`bench/terminal-bench/RESULTS.md`](./bench/terminal-bench/RESULTS.md), with
the narrative detail behind them in
[`bench/terminal-bench/HISTORY.md`](./bench/terminal-bench/HISTORY.md); this
page describes how measurements are taken so results stay comparable.

## What is measured

- **Task success** — how many tasks in the suite pass, out of the runnable
  set.
- **Wall-clock time** — end-to-end time for the suite.
- **Token usage** — prompt, completion, and total tokens, with cache reads
  and writes reported separately where the provider exposes them.
- **Tool behavior** — tool calls, failures, and retries are available from the
  run's session and Shepherd trace data.

## Method

- Pin the harness, model, and yaah commit in every row.
- Run each suite in a fresh workspace; do not reuse a session database or
  caches between trials.
- Record the exact run id and any excluded tasks (with the reason).
- Prefer at least two trials for comparisons; single-trial deltas are noisy.
- Keep raw logs and usage files with the run; the summary row is not enough
  to reproduce it.

## Running

See [`bench/terminal-bench/README.md`](./bench/terminal-bench/README.md) for
the image build, adapter, and command lines. The harness is separate from the
`yaah` binary; it drives yaah through the same CLI or MCP surface a user
would.

Historical results are preserved in git, in the `RESULTS.md` table, and in
the append-only `HISTORY.md` narrative. New results go in `RESULTS.md` as one
row, with any analysis appended to `HISTORY.md` — not to this page.
