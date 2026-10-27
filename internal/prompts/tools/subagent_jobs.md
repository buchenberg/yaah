Manage background sub-agent jobs dispatched via `delegate` with `background: true`. Provides `action: list` (all jobs), `status` (details for one `job_id`), `cancel` (abort a running job), and `wait` (block until a job finishes or `timeout_seconds` elapses — default 60s). Use `list` first to discover job ids and pending count.

Job state is reported truthfully. A job that has not finished carries
`still_running: true` alongside its `status`; a `wait` that hits its deadline
reports **both** `timed_out: true` and `still_running: true` (the job is not
done, the *wait call* timed out). `cancel` only requests cancellation — it does
not guarantee the job has stopped; read the `status`/`still_running` fields in
the response to know whether it is actually done.
