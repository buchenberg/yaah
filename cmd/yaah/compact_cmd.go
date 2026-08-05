package yaah

// compact_cmd.go implements the interactive :compact command and role
// hot-reload. NOTE: compactContext is a standalone summarizer that
// predates the agent loop's compactor; it does NOT share cooldowns,
// adaptive budgets, chunked fallback, or events with
// agent.Loop.compactContext. Unification is tracked by the
// agent-context-split plan (compaction sub-package phase).

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

func (s *agentSession) compactContext() {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()

	msg := func(text string) {
		if ch != nil {
			select {
			case ch <- &types.CtrlStatus{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}

	window := s.cfg.Agent.Default.ContextWindow
	if window <= 0 {
		window = 128000
	}

	msgs := s.messages
	if len(msgs) <= 4 {
		msg("context is already small enough")
		return
	}

	totalChars := 0
	for _, m := range msgs {
		totalChars += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	estTokens := totalChars / 4
	target := window * 4 / 5
	if estTokens <= target {
		msg(fmt.Sprintf("context: %d/%d tokens (%d%%)", estTokens, window, estTokens*100/window))
		return
	}

	msg(fmt.Sprintf("context: %d/%d tokens (%d%%) — compacting...", estTokens, window, estTokens*100/window))

	sysMsg := msgs[0]
	rest := msgs[1:]
	keepRecent := 6
	if len(rest) <= keepRecent {
		msg("not enough messages to compact")
		return
	}
	split := len(rest) - keepRecent

	if protect := s.cfg.Agent.Default.ReasoningProtect; protect > 0 {
		if adj := agent.ProtectReasoningTurns(msgs, 1+split, protect); adj < 1+split {
			split = adj - 1
			if split < 0 {
				split = 0
			}
		}
	}

	oldMsgs := rest[:split]
	keepMsgs := rest[split:]

	var sb strings.Builder
	sb.WriteString(prompts.ConversationSummaryPreamble())
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}

	compactModel := s.cfg.Agent.Default.SmallModel
	if compactModel == "" {
		compactModel = s.modelName
	}

	req := types.ChatRequest{
		Model:    compactModel,
		Messages: []types.Message{types.UserMsg(sb.String())},
	}

	resp, err := s.provider.Send(context.Background(), req)
	if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		s.messages = append([]types.Message{sysMsg}, keepMsgs...)
		msg("compacted (trimmed)")
		if ch != nil {
			t := 0
			for _, m := range s.messages {
				t += len(m.Content) / 4
			}
			select {
			case ch <- &types.CtrlContextInfo{Tokens: t, Window: window}:
			default:
			}
		}
		return
	}

	summary := resp.Choices[0].Message.Content
	newMsgs := []types.Message{sysMsg}
	newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
	newMsgs = append(newMsgs, keepMsgs...)
	s.messages = newMsgs

	newEstimate := 0
	for _, m := range newMsgs {
		newEstimate += len(m.Content) / 4
	}
	if ch != nil {
		select {
		case ch <- &types.CtrlContextInfo{
			Tokens: newEstimate,
			Window: window,
		}:
		default:
		}
	}

	msg(fmt.Sprintf("compacted: %d/%d tokens (%d%%)", newEstimate, window, newEstimate*100/window))
}

func (s *agentSession) reloadRoles() {
	s.mu.RLock()
	ch := s.ctrlCh
	cwd := s.cwd
	s.mu.RUnlock()

	msg := func(text string) {
		if ch != nil {
			select {
			case ch <- &types.CtrlStatus{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}

	opts := subagent.ReloadDefaultRolesOptions{
		BuiltinFiles: builtinRoleFiles(),
		SearchDirs:   roleSearchPaths(cwd),
	}
	if err := subagent.ReloadDefaultRoles(opts); err != nil {
		msg(fmt.Sprintf("role reload failed: %v", err))
		return
	}

	reg := subagent.DefaultRegistry()
	roles := reg.Names()
	msg(fmt.Sprintf("reloaded %d roles (%d built-in + %d search dirs)", len(roles), len(opts.BuiltinFiles), len(opts.SearchDirs)))
}
