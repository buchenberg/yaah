Arm a predicate that outlives the turn. The session ends its turn normally;
when the predicate fires, the harness injects a wake message with the
predicate's evidence, so a long wait needs no polling turns. Use `wait_for`
when the wait fits inside one turn; use `watch` when it may take longer.

`action` selects the verb:

- `arm` — create a watch. `on` is required and uses the same source
  vocabulary as `wait_for`: `job:<id>`, `process:<id>`, `time:<duration>`,
  `command:<cmd>`, `http:<url>`, or `file:<glob>` (workspace-relative; a
  root-anchored pattern is refused). `until` is the same
  optional condition set as `wait_for`. `id` optionally names the watch
  (a generated `wN` is used otherwise). `expires_in_seconds` bounds how long
  it stays armed (default 86400, maximum 604800). `interval_seconds` sets the
  initial poll delay.
- `list` — report the session's watches with their state and evidence.
- `cancel` — remove one watch by `id`; cancelling an unknown id reports
  `cancelled: false` rather than pretending it worked.

Fires are batched: watches that fire close together arrive as one digest, so
arming three CI checks wakes the session once. Each entry names the watch,
its predicate, and the evidence (output tail, status and body tail, file and
excerpt, or job result). Treat the wake as a harness message, not as a
message from the user: continue the task from where you left off.

Interactive sessions (TUI, REPL, web) start a turn when the wake arrives.
Headless surfaces (MCP serve, ACP) queue the digest and deliver it with the
next turn, because they have no client-initiated prompt to run.

Watches persist in the session database. On `--resume` they are re-armed with
their original armed time; one that fired while the session was down is
reported as a missed wake. `job:` and `process:` watches cannot survive a
restart because those ids are process-local, so they are reported as missed
and must be re-armed if still needed.

Guardrails: at most 32 armed watches per session, auto-expiry, and bounded
evidence. Arming a `command` watch is dangerous and may require approval;
that approval covers every later poll, so prefer a cheap status command.
