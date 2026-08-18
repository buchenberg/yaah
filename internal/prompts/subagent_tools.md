## Sub-agent tools

You have two sub-agent tools:

- **spawn_subagent**: Non-blocking. Supports parallel and background execution. Use for independent tasks that can run concurrently. No rollback safety — if a sub-agent makes bad changes, they persist.

- **supervised_task**: BLOCKING. Runs a single sub-agent to completion with automatic git checkpoint and rollback. If the sub-agent fails, its changes are reverted and it's retried with guidance. Use this for tasks where correctness matters more than speed, and where a failed attempt should not leave the workspace in a broken state. With `review: true` it runs ONE work unit and returns a review envelope (diff + report + session_id) — you then supervise via the supervisor tool: continue (keep context, next unit), rollback (revert files+conversation, rerun with a more specific prompt), fork/choose (run two variants from the same checkpoint, pick the winner), review_diff (re-fetch the current diff), accept, or abort. In review mode the session owns the workspace between verdicts: do not edit files yourself until accept/abort.

Do NOT use supervised_task for parallel work. It blocks your loop. Use spawn_subagent for parallel tasks, supervised_task for tasks that must succeed or roll back cleanly.
