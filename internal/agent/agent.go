// Package agent provides the core agent loop for yaah.
//
// The package contains several subsystems:
//   - Core loop: Loop, LoopConfig, LoopState (loop.go, types.go, options.go, turn.go)
//   - Tool execution: concurrent dispatch and tool-def building (agent_tools.go, tools.go)
//   - Context management: compaction, pruning, token estimation (context_manager.go,
//     agent_context.go, agent_prune.go, agent_chunked.go, agent_truncation.go)
//   - Persistence: SessionPersister (persist.go)
//   - Lifecycle: message init, teardown, event publishing (lifecycle_init.go, lifecycle_teardown.go)
//   - View: typed event broker adapter (view.go, event_aliases.go)
//
// Subpackages contain extracted concerns: events/ (typed event system),
// llm/ (LLM client with retry/fallback/streaming), pipeline/ (middleware),
// subagent/ (role definitions), runner/ (sub-agent composition), and
// errorclassify/ (provider error taxonomy).
package agent
