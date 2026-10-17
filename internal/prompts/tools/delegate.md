Sends one subtask to a role-scoped sub-agent and returns its result. The sub-agent gets its own tool set, limits, and context; it can read, write, and execute independently.

What comes back depends on the dispatch lane:

- **Retained (the default):** the work runs in an isolated worktree and returns `status: "proposal"` with a `proposal_id`, the changed `files`, and a `diff`. Nothing has changed on your tree — settle it with the `proposal` tool (`select`, `apply`, `discard`).
- **Shared:** the sub-agent runs directly on the workspace, and the result is its report; whatever it wrote is already applied.
- **Background** (`background: true`): returns a `job_id` immediately; the result arrives later as a follow-up message and can be managed with `subagent_jobs`.

Required: `description` (3-5 word summary — also labels the dispatch in jobs and UIs), `prompt` (the complete task), `role`. Optional: `json_mode` (structured output), `max_iterations` (1-50), `max_turns` (1-50), `output_limit` (byte cap). Timeouts are configured per role.

Use it for complex multi-step work, and for work whose raw tool calls would otherwise fill your context. Call several in one turn to run them in parallel.
