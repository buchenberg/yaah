Sends one subtask to a role-scoped sub-agent and returns its result. The sub-agent gets its own tool set, limits, and context; it can read, write, and execute independently.

What comes back depends on the role's output contract and the dispatch lane it selects:

- **Proposal roles** (default; e.g. developer): the work runs in an isolated worktree and returns `status: "proposal"` with a `proposal_id`, the changed `files`, and a `diff`. Nothing has changed on your tree — settle it with the `proposal` tool (`select`, `apply`, `release`, `discard`).
- **Report roles** (`output: report` in the role file; e.g. reviewer): the work runs in an isolated worktree, and the result is the report itself — there is no proposal and nothing to settle. Roles that cannot change files at all run directly on the workspace for fresher data.
- **Shared mode** (`agents.subagent.mode: shared`): every dispatch runs directly on the workspace, and the result is its report; whatever it wrote is already applied.
- **Background** (`background: true`): returns a `job_id` immediately; the result arrives later as a follow-up message and can be managed with `subagent_jobs`.

Required: `description` (3-5 word summary — also labels the dispatch in jobs and UIs), `prompt` (the complete task), `role`. Optional: `json_mode` (structured output), `max_iterations` (1-50), `max_turns` (1-50), `output_limit` (byte cap), `timeout_seconds` (wall-clock cap in seconds, 0-600; 0 or omitted uses the role's configured timeout).

Use it for complex multi-step work, and for work whose raw tool calls would otherwise fill your context. Call several in one turn to run them in parallel.
