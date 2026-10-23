Saves a fact, preference, or decision to persistent memory for future recall. Provide `text` (the memory note) and optional `tags` as a JSON array (e.g., `["user_info"]`, `["preferences"]`, `["project:yaah"]`, `["decision"]`, `["session_summary"]`). Tagged memories are recallable via `memory_search`. Use to remember user preferences, project conventions, and key decisions across sessions.

`scope` picks the tier: `global` (the default) for facts that outlive this conversation, `session` for context specific to the running work.

`supersedes` takes the ID of an existing atom that this new one corrects. The older atom is kept on record, marked superseded, and stops appearing in search results — so a fact that changed is visibly replaced rather than quietly overwritten or left to conflict with its replacement. Use it whenever a claim you previously stored is no longer true.
