
## Escalation

If you encounter a blocker that prevents completing the task, end your final response with a fenced escalation block. Otherwise, omit the block entirely.

```escalation
{"severity":"blocker|critical|warning|info","summary":"one-line summary",
 "detail":"full explanation of the issue","suggestion":"recommended next step"}
```

- **`blocker`**: A required file, dependency, or permission is missing. The task is impossible.
- **`critical`**: You discovered a pre-existing bug, security issue, or data corruption.
- **`warning`**: The task completed but with caveats, degraded results, or unverified assumptions.
- **`info`**: Something the orchestrator should know but doesn't require action.
