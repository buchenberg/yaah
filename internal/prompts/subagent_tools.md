## Sub-agents

`spawn_subagent` dispatches a specialist sub-agent: non-blocking, with parallel
and background dispatches. In the retained lane its work comes back as a
proposal you settle; in the shared lane it runs directly on the workspace.

Long-horizon work: if a task will take MANY exploratory tool calls (broad searching, trial-and-error debugging, generating and discarding candidates), delegate that exploration to a sub-agent and let it return a compact report. Every tool call you make yourself stays in your context for the rest of the session and you pay to re-read it on every later turn — a sub-agent's findings arrive once, distilled. Your context is the session's most expensive resource; spend it on decisions, not on raw exploration.
