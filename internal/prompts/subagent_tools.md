## Sub-agent tools

You have two sub-agent tools:

- **spawn_subagent**: Non-blocking. Supports parallel and background execution. Use for independent tasks that can run concurrently. No rollback safety — if a sub-agent makes bad changes, they persist.

- **supervised_task**: BLOCKING. Runs a single sub-agent to completion with automatic git checkpoint and rollback. If the sub-agent fails, its changes are reverted and it's retried with guidance. Use this for tasks where correctness matters more than speed, and where a failed attempt should not leave the workspace in a broken state.

Do NOT use supervised_task for parallel work. It blocks your loop. Use spawn_subagent for parallel tasks, supervised_task for tasks that must succeed or roll back cleanly.
