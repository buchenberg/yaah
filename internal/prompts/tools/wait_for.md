Wait blockingly inside this call until a predicate fires. Because the turn
stays open, a wait costs zero model turns: prefer it to repeatedly asking
"is it done yet".

`on` names one predicate:

- `job:<id>` — a background sub-agent dispatched with `delegate` and
  `background: true`.
- `process:<id>` — a tracked `background_process` id (`proc-N`).
- `time:<duration>` — for example `time:90s`, `time:5m`.
- `command:<cmd>` — run a shell command; it fires on exit zero by default.
- `http:<url>` — probe a URL; it fires on any 2xx by default.
- `file:<glob>` — wait for a file to exist or match content. Patterns are
  workspace-relative; root-anchored or absolute patterns are refused.

`until` refines the polling sources. For `command`: `exit_zero` (the
default), `exit:<n>`, `regex:<re>`, or `contains:<text>`. For `http`:
`status:<code>`, `status:<n>xx`, `regex:<re>`, or `contains:<text>`. For
`file`: `exists` (the default), `contains:<text>`, or `regex:<re>`. The job,
process and time predicates reach a terminal state on their own and accept
no `until`.

`timeout_seconds` bounds one call (default 900, maximum 86400). When it
elapses the result is `fired: false, timed_out: true` with a `handle` and the
evidence from the last attempt. `interval_seconds` sets the initial poll
delay; the engine backs off on its own, so tuning it is rarely needed.

To keep waiting after a timeout, call `wait_for` again with `handle` (and
omit `on`/`until`, or repeat them — they must agree with the handle). The
handle preserves the wait's original start, so elapsed time does not reset.

The result carries a typed `evidence` object, not just a boolean. Its
fields are `kind`, `status`, `exit_code`, `matched_paths`, `excerpt`,
`result` and `detail`: command output tail, HTTP status and body tail,
process output tail, job status and the sub-agent's result, or the matched
file and excerpt. `detail` carries the human-readable summary. On success
read that evidence and continue; do not poll the same thing again.

A `command` predicate reruns the command on every attempt, so it is for cheap
status probes, not long builds — use `background_process` plus a
`process:` predicate for those. `wait_for` with a `command` predicate is
treated as a dangerous call and may require approval. If the caller cancels
the turn, the result is `interrupted: true`, which is not a timeout.

The `on`/`until` string form is deliberate; the rationale is recorded in
`docs/wake-predicate-schema-decision.md`.
