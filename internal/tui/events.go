package tui

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/types"
)

// AddAssistantMessage renders markdown through glamour and stores both
// the rendered output (Content) and the raw markdown (Raw).
func (m *Model) AddAssistantMessage(raw string) {
	m.messages = append(m.messages, Message{
		Role:    "assistant",
		Content: m.renderMarkdown(raw),
		Raw:     raw,
	})
	m.markDirty()
}

// AddAssistantMessageWithReasoning adds an assistant message with attached reasoning text.
func (m *Model) AddAssistantMessageWithReasoning(raw, reasoning string) {
	m.messages = append(m.messages, Message{
		Role:      "assistant",
		Content:   m.renderMarkdown(raw),
		Raw:       raw,
		Reasoning: reasoning,
	})
	m.markDirty()
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content, Raw: content})
	m.markDirty()
}

// AddToolResult adds a tool result message. For todowrite, it parses the
// tool args and renders the todo table inline so it is always visible
// regardless of CtrlTodos delivery timing.
func (m *Model) AddToolResult(toolName, content, toolArgs, duration string) {
	rendered := m.renderToolResult(toolName, content)

	if toolName == "todowrite" && toolArgs != "" {
		if items := parseTodosFromArgs(toolArgs); len(items) > 0 {
			todoTable := NewTodoTable(items, m.width-8)
			rendered = rendered + "\n\n" + todoTable.Render()
		}
	}

	m.messages = append(m.messages, Message{
		Role:         "tool",
		Content:      rendered,
		Raw:          content,
		ToolName:     toolName,
		ToolArgs:     toolArgs,
		ToolDuration: duration,
	})
	m.markDirty()
}

// SetEphemeral sets a transient status message that auto-clears after
// ~3 seconds. Use for feedback like "Compacted." or "Copied!".
func (m *Model) SetEphemeral(msg string) {
	m.ephemMsg = msg
	m.ephemTimer = 15 // ~3 seconds at ~200ms spinner tick rate
}

// SetMCPInfos stores MCP server status info and adds an :mcp command.
func (m *Model) SetMCPInfos(infos []ServerInfo) {
	m.mcpInfos = infos
}

// SetThinking sets the thinking state.
func (m *Model) SetThinking(thinking bool) {
	m.thinking = thinking
	m.markDirty()
}

// SetCompacting sets the compaction state. When true, displays a
// compaction indicator in the status area.
func (m *Model) SetCompacting(compacting bool) {
	m.compacting = compacting
	m.markDirty()
}

// SetToolCall sets the current tool call display.
func (m *Model) SetToolCall(name, args string) {
	m.toolCall = name
	m.toolArgs = args
	m.markDirty()
}

// ClearToolCall clears the tool call display.
func (m *Model) ClearToolCall() {
	m.toolCall = ""
	m.toolArgs = ""
	m.markDirty()
}

// AppendToken appends a streaming token to the current response.
// To avoid excessive viewport rebuilds during fast streaming, only
// a full refresh + scroll runs when the debounce flag is cleared.
// The spinner tick (which fires ~15 times/sec) picks up pending
// refreshes.
func (m *Model) AppendToken(token string) {
	if !m.streaming {
		m.streaming = true
		m.streamContent = ""
		m.needsRefresh = true
	}

	m.streamContent += token

	// Refresh immediately if no pending refresh, then set the debounce flag.
	if !m.needsRefresh {
		m.refreshViewport()
		m.scrollToBottom()
		m.needsRefresh = true
	}
}

// HandleEvent processes typed agent events from the broker.
// Called from the bubbletea event loop via tea.Send in the forwarder goroutine.
func (m *Model) HandleEvent(evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		m.AppendToken(e.Text)

	case *agent.ThinkingEvent:
		m.thinkContent += e.Text

	case *agent.FlushEvent:
		haveReasoning := m.thinkContent != ""
		m.streaming = false
		m.streamContent = ""
		m.recordView = true
		if haveReasoning {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning(e.Content, reasoning)
		} else {
			m.AddAssistantMessage(e.Content)
		}

	case *agent.ToolStartEvent:
		if m.thinkContent != "" {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning("", reasoning)
		}
		m.SetToolCall(e.Name, e.Args)

	case *agent.ToolEndEvent:
		m.ClearToolCall()
		if e.Name != "spawn_subagent" {
			m.AddToolResult(e.Name, e.Result, e.Args, formatDuration(e.Duration))
		}

	case *agent.CompactionStartedEvent:
		m.SetCompacting(true)

	case *agent.CompactionDoneEvent:
		m.SetCompacting(false)
		beforeK := float64(e.BeforeTokens) / 1000.0
		afterK := float64(e.AfterTokens) / 1000.0
		pct := e.SavingsPct * 100
		note := ""
		if e.IneffectiveNote != "" {
			note = " ⚠ " + e.IneffectiveNote
		}
		m.messages = append(m.messages, Message{
			Role: "compaction",
			Content: fmt.Sprintf("Compacted %.1fK → %.1fK tokens (%.0f%% savings, %s) in %.1fs%s  [old=%d keep=%d budget=%d]",
				beforeK, afterK, pct, e.Method, e.ElapsedSeconds, note,
				e.OldMsgCount, e.KeepMsgCount, e.Budget),
		})
		m.markDirty()

	case *agent.SubAgentStartEvent:
		m.messages = append(m.messages, Message{
			Role:       "subagent",
			Content:    e.Prompt,
			SubRole:    e.Role,
			SubID:      e.SubAgentID,
			SubRunning: true,
		})
		m.markDirty()

	case *agent.SubAgentEndEvent:
		idx := -1
		for i := len(m.messages) - 1; i >= 0; i-- {
			msg := &m.messages[i]
			if msg.Role != "subagent" || !msg.SubRunning || msg.SubRole != e.Role {
				continue
			}
			if e.SubAgentID != "" && msg.SubID != e.SubAgentID {
				continue
			}
			idx = i
			break
		}
		if idx < 0 {
			m.messages = append(m.messages, Message{
				Role:    "subagent",
				SubRole: e.Role,
				SubID:   e.SubAgentID,
			})
			idx = len(m.messages) - 1
		}
		m.messages[idx].SubRunning = false
		m.messages[idx].SubError = e.Error
		m.messages[idx].SubResult = e.Result
		if e.Duration > 0 {
			m.messages[idx].ToolDuration = formatDuration(e.Duration)
		}
		m.markDirty()

	case *agent.EscalationEvent:
		severity := e.Severity
		prefix := "⚠"
		switch severity {
		case "blocker", "critical":
			prefix = "🛑"
		case "info":
			prefix = "ℹ"
		}
		content := prefix + " ESCALATION [" + severity + "] " + e.SubAgentRole + ": " + e.Summary
		if e.Detail != "" {
			content += "\n" + e.Detail
		}
		if e.Suggestion != "" {
			content += "\n→ " + e.Suggestion
		}
		m.messages = append(m.messages, Message{
			Role:    "escalation",
			Content: content,
		})
		m.markDirty()

	case *agent.DoneEvent:
		m.SetThinking(false)
		m.ClearToolCall()
		if e.Error != "" {
			m.AddMessage("error", e.Error)
		}
		haveReasoning := m.thinkContent != ""
		if m.streaming && m.streamContent != "" {
			content := m.streamContent
			m.streaming = false
			m.streamContent = ""
			if haveReasoning {
				reasoning := m.thinkContent
				m.thinkContent = ""
				m.AddAssistantMessageWithReasoning(content, reasoning)
			} else {
				m.AddAssistantMessage(content)
			}
		} else if e.Response != "" {
			if haveReasoning {
				reasoning := m.thinkContent
				m.thinkContent = ""
				m.AddAssistantMessageWithReasoning(e.Response, reasoning)
			} else {
				m.AddAssistantMessage(e.Response)
			}
		} else if haveReasoning {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning("", reasoning)
		} else {
			m.thinkContent = ""
		}
		if e.ContextWindow > 0 {
			m.HandleContextInfo(e.ContextTokens, e.ContextWindow)
		}

	default:
		// Unknown event — silently ignore for forward compatibility
	}
}

// handleControlMsg processes a control-plane message from the session.
func (m *Model) handleControlMsg(msg types.CtrlMsg) {
	switch ctrl := msg.(type) {
	case *types.CtrlStatus:
		m.AddMessage("system", ctrl.Text)
	case *types.CtrlTodos:
		m.todos = ctrl.Items
		m.markDirty()
	case *types.CtrlError:
		m.AddMessage("error", fmt.Sprintf("%v", ctrl.Err))
		m.SetThinking(false)
		m.streaming = false
		m.streamContent = ""
		m.activePrompt = ""
	case *types.CtrlQuestion:
		m.questionModal = QuestionModal{
			Header:   ctrl.Header,
			Question: ctrl.Question,
			Options:  make([]QuestionOption, len(ctrl.Options)),
			Multiple: ctrl.Multiple,
			AnswerCh: ctrl.AnswerCh,
		}
		for i, o := range ctrl.Options {
			m.questionModal.Options[i] = QuestionOption(o)
		}
		m.questionIdx = 0
		m.questionMulti = make([]bool, len(ctrl.Options))
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
	case *types.CtrlApproval:
		ch := make(chan string, 1)
		m.questionModal = QuestionModal{
			Header:   "Approve",
			Question: fmt.Sprintf("Run %s(%s)?", ctrl.Name, ctrl.Args),
			Options: []QuestionOption{
				{Label: "Yes", Description: "Approve this tool call"},
				{Label: "No", Description: "Deny this tool call"},
			},
			Multiple: false,
			AnswerCh: ch,
		}
		m.questionIdx = 0
		m.questionMulti = make([]bool, 2)
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
		go func() {
			answer := <-ch
			ctrl.ApproveCh <- (answer == "Yes" || answer == "Yes, Yes")
		}()
	case *types.CtrlContextInfo:
		m.HandleContextInfo(ctrl.Tokens, ctrl.Window)
	case *types.CtrlFallback:
		m.provider = ctrl.Provider
		m.modelName = ctrl.Model
	case *types.CtrlContinue:
		ch := make(chan string, 1)
		m.questionModal = QuestionModal{
			Header:   "Max iterations reached",
			Question: fmt.Sprintf("The agent reached the iteration limit (%d). Continue?", ctrl.MaxIter),
			Options: []QuestionOption{
				{Label: "Yes", Description: "Let the agent keep working"},
				{Label: "No", Description: "Stop the agent"},
			},
			Multiple: false,
			AnswerCh: ch,
		}
		m.questionIdx = 0
		m.questionMulti = make([]bool, 2)
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
		go func() {
			answer := <-ch
			ctrl.AnswerCh <- (answer == "Yes")
		}()
	case *types.CtrlModelList:
		m.modelItems = ctrl.Models
		if ctrl.ProviderNames != nil {
			m.providerNames = ctrl.ProviderNames
		}
		m.markDirty()
	case *types.CtrlDone:
	}
}

// HandleContextInfo updates the context window display.
func (m *Model) HandleContextInfo(tokens, window int) {
	m.contextTokens = tokens
	m.contextWindow = window
	if window > 0 {
		m.contextPct = tokens * 100 / window
		if m.contextPct > 100 {
			m.contextPct = 100
		}
	}
}

// formatDuration returns a human-readable duration string.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func parseTodosFromArgs(args string) []todo.Item {
	type rawTodo struct {
		Content  string `json:"content"`
		Status   string `json:"status"`
		Priority string `json:"priority"`
	}
	var params struct {
		Todos []rawTodo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return nil
	}
	items := make([]todo.Item, len(params.Todos))
	for i, t := range params.Todos {
		if t.Priority == "" {
			t.Priority = "medium"
		}
		items[i] = todo.Item{
			ID:       fmt.Sprintf("td-%d", i+1),
			Content:  t.Content,
			Status:   t.Status,
			Priority: t.Priority,
		}
	}
	return items
}
