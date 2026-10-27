package yaah

// compact_cmd.go implements the interactive :compact command and role
// hot-reload. The :compact command delegates to agent.Loop.ForceCompact,
// sharing the same cooldowns, adaptive budgets, chunked fallback, and
// events as the in-loop compactor.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/types"
)

// compactTimeout caps how long the :compact command waits for the
// compaction provider to respond. Prevents an unresponsive provider
// from blocking the interactive session indefinitely.
const compactTimeout = 5 * time.Minute

// estimateTokens approximates the token cost of a message slice using
// the same formula as agent.messageTokens: chars/4 for content,
// reasoning content, and tool-call arguments.
func estimateTokens(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)/4 + len(m.ReasoningContent)/4
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Arguments)/4 + len(tc.Function.Name)/4
		}
	}
	return n
}

// compactContext runs the unified compaction path via a minimal agent.Loop.
// It constructs a loop with the session's provider and messages, then calls
// loop.ForceCompact() so compaction always runs (ForceCompact bypasses
// cooldowns and threshold guards for the explicit user request).
func (s *agentSession) compactContext() {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()

	msg := func(text string) {
		if ch != nil {
			select {
			case ch <- &control.Status{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}

	// Snapshot s.messages under the read lock: prompt runs swap the
	// slice under the write lock while this method runs.
	s.mu.RLock()
	msgs := s.messages
	s.mu.RUnlock()

	if len(msgs) <= 4 {
		msg("context is already small enough")
		return
	}

	window := s.cfg.Agent.Default.ContextWindow
	if window <= 0 {
		window = 1048576
	}

	estTokens := estimateTokens(msgs)

	msg(fmt.Sprintf("context: %d/%d tokens (%d%%) — compacting...", estTokens, window, estTokens*100/window))

	compactProvider, compactModel, fallbackProvider, fallbackModel, fallbackProviderName := s.auxProviders()

	b := s.loopBuilder(s.provider, s.modelName, compactProvider, compactModel, fallbackProvider, fallbackModel, fallbackProviderName)

	otelEnabled := s.cfg.Observability.Otel.Enabled
	loop := b.Build(agent.LoopBuildOptions{
		OtelEnabled: &otelEnabled,
		OtelVerbose: s.cfg.Observability.Otel.Verbose,
	})

	// ForceCompact bypasses cooldowns and threshold guards so the
	// user's explicit :compact request always runs. Use a finite
	// timeout so an unresponsive provider doesn't block indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
	defer cancel()
	res := loop.ForceCompact(ctx, 0.0)

	s.mu.Lock()
	s.messages = loop.State.Messages
	compacted := s.messages
	s.mu.Unlock()

	newEstimate := estimateTokens(compacted)

	if ch != nil {
		select {
		case ch <- &control.ContextInfo{
			Tokens:                    newEstimate,
			Window:                    window,
			LastPromptTokensInclusive: newEstimate,
		}:
		default:
		}
	}

	if res.Method == "" {
		// No pass ran (guards skipped it: too few messages, or the loop
		// already compacted this turn). Say so once.
		msg("no messages were compacted (context too small or compaction ineffective)")
		return
	}

	// Render the summary in the conversation stream (yaah-v378.5): the
	// user should be able to read what just replaced their context. The
	// summary is display-only — it already lives in the system message.
	out := &control.CompactionSummary{
		Summary: res.Summary,
		Method:  res.Method,
		Before:  estTokens,
		After:   newEstimate,
		Window:  window,
	}
	if ch != nil {
		select {
		case ch <- out:
		default:
		}
		return
	}
	block := out.Header()
	if res.Summary != "" {
		fmt.Fprintf(os.Stderr, "  %s\n  summary:\n%s\n", block, res.Summary)
	} else {
		fmt.Fprintf(os.Stderr, "  %s — no summary produced (summarization failed; old messages trimmed)\n", block)
	}
}

func (s *agentSession) reloadRoles() {
	s.mu.RLock()
	ch := s.ctrlCh
	cwd := s.cwd
	s.mu.RUnlock()

	msg := func(text string) {
		if ch != nil {
			select {
			case ch <- &control.Status{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}

	opts := subagent.ReloadDefaultRolesOptions{
		BuiltinFiles: runner.BuiltinRoleFiles(),
		SearchDirs:   runner.RoleSearchPaths(cwd),
	}
	if err := subagent.ReloadDefaultRoles(opts); err != nil {
		msg(fmt.Sprintf("role reload failed: %v", err))
		return
	}

	reg := subagent.DefaultRegistry()
	roles := reg.Names()
	msg(fmt.Sprintf("reloaded %d roles (%d built-in + %d search dirs)", len(roles), len(opts.BuiltinFiles), len(opts.SearchDirs)))
}
