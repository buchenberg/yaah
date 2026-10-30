Manage background sub-agent jobs dispatched via `delegate` with `background: true`. Provides `action: list` (all jobs), `status` (details for one `job_id`), `cancel` (abort a running job), and `wait` (block until a job finishes or `timeout_seconds` elapses — default 60s, max 300s). Use `list` first to discover job ids and pending count.

A job snapshot carries `id`, `role`, `description`, `model` when known,
`status`, `still_running` when true, `duration_ms` when finished, and `error`
when it failed. `list` returns `jobs` and `pending`; `status` returns one
snapshot; `cancel` adds `cancel` and optional `notes`; `wait` adds `timed_out`,
`interrupted`, and optional `notes`.

Job state is reported truthfully. A job that has not finished carries
`still_running: true` alongside its `status`; a `wait` that hits its deadline
reports **both** `timed_out: true` and `still_running: true` (the job is not
done, the *wait call* timed out). A `wait` cut short because the turn was
cancelled or a parent deadline fired reports `interrupted: true` instead of
`timed_out`, again with `still_running: true` — the job was not asked to stop.

`cancel` reports what it actually did in a `cancel` field, in one of exactly
three values:

- `cancelled` — the id named a running job and the request reached it. The job
  may still be winding down; read `status`/`still_running` in the same response
  to know whether it has stopped. A request is not a stop.
- `already_finished` — the job had already reached a terminal state, so nothing
  was interrupted. `status` carries that real final state (`completed`,
  `failed`, `cancelled`).
- `unknown` — no such job: never dispatched in this session, or its finished
  record was reaped. No job fields are reported, because there is no job.

An envelope may carry `notes` when a call returned early or has a caveat: a cancellation request that has not stopped the job yet, a wait interrupted before the job finished, or a job record that disappeared. `notes` explains the early return; it does not change the reported `status` or `still_running` facts. It is absent when there is no caveat.
