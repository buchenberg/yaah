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

	if len(s.messages) <= 4 {
		msg("context is already small enough")
		return
	}

	window := s.cfg.Agent.Default.ContextWindow
	if window <= 0 {
		window = 1048576
	}

	estTokens := estimateTokens(s.messages)

	msg(fmt.Sprintf("context: %d/%d tokens (%d%%) — compacting...", estTokens, window, estTokens*100/window))

	rawCompactProvider, compactModel := resolveCompact(s.cfg)
	compactProvider := agent.ResolveCompactProvider(rawCompactProvider, s.cfg.Observability.Otel.Verbose)
	fallbackProvider, fallbackModel, _ := resolveFallback(s.cfg)

	b := s.loopBuilder(s.provider, s.modelName, compactProvider, compactModel, fallbackProvider, fallbackModel)

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
	loop.ForceCompact(ctx, 0.0)

	s.messages = loop.State.Messages

	newEstimate := estimateTokens(s.messages)

	if ch != nil {
		select {
		case ch <- &control.ContextInfo{
			Tokens: newEstimate,
			Window: window,
		}:
		default:
		}
	}

	if newEstimate < estTokens {
		msg(fmt.Sprintf("compacted: %d/%d tokens (%d%%)", newEstimate, window, newEstimate*100/window))
	} else {
		msg("no messages were compacted (context too small or compaction ineffective)")
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
