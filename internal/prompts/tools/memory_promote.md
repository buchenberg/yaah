Moves a memory entry from the `session` tier into the `global` tier. Use it when a session atom turns out to state a durable fact: something true beyond the conversation it was distilled from — how this project works, a standing preference, a decision that will outlive the task at hand.

Promotion is one-way and idempotent. A promoted atom survives any single session: it stops being prunable by session and starts appearing in default recall, so every later conversation sees it. Promoting an atom that is already global does nothing. A superseded atom cannot be promoted — promote its replacement instead.

Promote sparingly. The session tier exists so working context does not become everyone's context; promoting everything recreates the noise the tiers are there to prevent. Requires the memory `id`, which memory_search returns for each hit.
