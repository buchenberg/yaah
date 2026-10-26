package acp

import (
	"strconv"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/toolfmt"
)

// View translates agent events into ACP session/update payloads.
// Tool calls are delivered as an ACP tool_call at creation (status
// in_progress — dispatch executes immediately after publish) and a
// tool_call_update at completion, correlated via the per-execution ID
// carried on the events themselves and rendered as the string
// toolCallId "t<N>".
type View struct {
	mu sync.Mutex
	// announced holds tool call ids already sent as tool_call by a
	// permission request. Their execution update must use
	// tool_call_update instead of creating the call again.
	announced map[string]bool
	// compaction records whether the client advertised
	// clientCapabilities.session.compaction (set at prompt start from
	// the server's stored caps).
	compaction bool
	// compactionSeq fuels agent-owned compaction ids, unique per ACP
	// session: "cmp-<n>".
	compactionSeq map[string]int
	// compactionOpen holds the id of the started-but-not-yet-finished
	// compaction, if any, so the done event patches the card the start
	// event created.
	compactionOpen map[string]string
}

// NewView creates a View ready to translate agent events.
func NewView() *View {
	return &View{
		announced:      make(map[string]bool),
		compactionSeq:  make(map[string]int),
		compactionOpen: make(map[string]string),
	}
}

// EnableCompaction turns on ID-addressed compaction updates. The
// server calls it when the prompt's connection advertised the
// session.compaction capability.
func (v *View) EnableCompaction() {
	v.mu.Lock()
	v.compaction = true
	v.mu.Unlock()
}

func (v *View) compactionEnabled() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.compaction
}

// nextCompactionID allocates the next agent-owned compaction id for
// the session and records it as the open compaction.
func (v *View) nextCompactionID(sessionID string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.compactionSeq[sessionID]++
	id := "cmp-" + strconv.Itoa(v.compactionSeq[sessionID])
	v.compactionOpen[sessionID] = id
	return id
}

// currentCompactionID returns the id of the open compaction, or a
// freshly allocated one when the done event arrives with no recorded
// start (e.g. a compaction that began before the prompt turn).
func (v *View) currentCompactionID(sessionID string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if id, ok := v.compactionOpen[sessionID]; ok {
		delete(v.compactionOpen, sessionID)
		return id
	}
	v.compactionSeq[sessionID]++
	return "cmp-" + strconv.Itoa(v.compactionSeq[sessionID])
}

// Announce marks a tool call as already created by a permission
// request, so the execution event updates it instead of re-creating it.
func (v *View) Announce(callID string) {
	if callID == "" {
		return
	}
	v.mu.Lock()
	v.announced[callID] = true
	v.mu.Unlock()
}

func (v *View) wasAnnounced(callID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.announced[callID]
}

// toolCallID renders the per-execution event ID as an ACP toolCallId.
func toolCallID(id int64) string { return "t" + strconv.FormatInt(id, 10) }

// SendTo converts evt to an update and passes it to send, tagging it
// with the given session ID. Events with no ACP representation are
// dropped.
// isEditClassTool reports whether a tool name describes a file
// modification whose arguments the client can render as a diff.
func isEditClassTool(name string) bool {
	return name == "write" || name == "edit"
}

// SendTo converts evt to an update and passes it to send, tagging it
// with the given session ID. Events with no ACP representation are
// dropped.
func (v *View) SendTo(sessionID string, send updateSender, evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(e.Text),
		})
	case *agent.ThinkingEvent:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentThoughtChunk,
			Content:       textContent(e.Text),
		})
	case *agent.FlushEvent:
		// ACP streams a continuous markdown narrative; a flush only
		// separates logical segments for the REPL. Emit one newline so
		// segment boundaries do not glue words together.
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent("\n"),
		})
	case *agent.ToolStartEvent:
		callID := e.CallID
		if callID == "" {
			callID = toolCallID(e.ID)
		}
		updateKind := UpdateToolCall
		if v.wasAnnounced(callID) {
			updateKind = UpdateToolCallUpdate
		}
		call := ToolCallUpdate{
			SessionUpdate: updateKind,
			ToolCallID:    callID,
			Name:          e.Name,
			Kind:          toolKind(e.Name),
			Status:        StatusInProgress,
		}
		// tool_call requires a title; the abbreviated args hint makes
		// rows distinguishable before any result content exists.
		if hint := compactHint(e.Args); hint != "" {
			call.Title = e.Name + ": " + hint
		} else {
			call.Title = e.Name
		}
		// Jump-to-file location for file-modifying calls, available
		// while the tool is still running.
		if isEditClassTool(e.Name) {
			if locs, _ := diffWalker(e.Name, e.RawArgs); len(locs) > 0 {
				call.Locations = locs
			}
		}
		send(sessionID, call)
	case *agent.ToolEndEvent:
		callID := e.CallID
		if callID == "" {
			callID = toolCallID(e.ID)
		}
		update := ToolCallUpdate{
			SessionUpdate: UpdateToolCallUpdate,
			ToolCallID:    callID,
			Status:        StatusCompleted,
			Content: []UpdateContent{{
				Type:    "content",
				Content: &ContentBlock{Type: "text", Text: e.Result},
			}},
		}
		// File-modifying calls surface their change as a diff block so
		// rendering clients (Zed) show a file diff instead of raw
		// arguments; the plain result text keeps its own block.
		if isEditClassTool(e.Name) && e.Error == "" {
			if locs, diffs := diffWalker(e.Name, e.RawArgs); len(diffs) > 0 {
				update.Locations = locs
				update.Content = append(diffs, update.Content...)
			}
		}
		if e.Error != "" {
			update.Status = StatusFailed
			update.Content = []UpdateContent{{
				Type:    "content",
				Content: &ContentBlock{Type: "text", Text: e.Error},
			}}
		}
		send(sessionID, update)
	case *agent.SubAgentStartEvent:
		label := toolfmt.SubagentRoleLabel(e.Role)
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("\n[sub-agent: %s] %s\n", label, e.Prompt)),
		})
	case *agent.SubAgentEndEvent:
		label := toolfmt.SubagentRoleLabel(e.Role)
		status := "completed"
		if e.Error != "" {
			status = e.Error
		}
		modelStr := ""
		if e.Model != "" {
			modelStr = " [" + e.Model + "]"
		}
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("[sub-agent: %s%s] %s\n", label, modelStr, status)),
		})
	case *agent.EscalationEvent:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("ESCALATION [%s] %s: %s", e.Severity, e.SubAgentRole, e.Summary)),
		})
	case *agent.CompactionStartedEvent:
		// The client negotiates session.compaction before ID-addressed
		// compaction updates may be sent; with the capability, the card
		// is an in_progress compaction_update, the details riding _meta;
		// without it, compaction surfaces as an ordinary message (the
		// spec's fallback rule).
		if v.compactionEnabled() {
			send(sessionID, CompactionUpdate{
				SessionUpdate: UpdateCompaction,
				CompactionID:  v.nextCompactionID(sessionID),
				Status:        CompactionInProgress,
				Meta: map[string]any{
					"beforeTokens": e.BeforeTokens,
					"targetTokens": e.TargetTokens,
					"reason":       e.Reason,
					"text":         e.StartedSummary(),
				},
			})
			break
		}
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent("[" + e.StartedSummary() + "]"),
		})
	case *agent.CompactionDoneEvent:
		// Capability-gated like the start event: with it, the completed
		// summary streams as a summary chunk appended to the (patched)
		// compaction card, then a completed upsert carries the tally in
		// _meta. Fallback stays a plain text chunk.
		if v.compactionEnabled() {
			id := v.currentCompactionID(sessionID)
			meta := map[string]any{
				"beforeTokens":   e.BeforeTokens,
				"afterTokens":    e.AfterTokens,
				"savingsPct":     e.SavingsPct,
				"method":         e.Method,
				"elapsedSeconds": e.ElapsedSeconds,
				"oldMsgCount":    e.OldMsgCount,
				"keepMsgCount":   e.KeepMsgCount,
				"budget":         e.Budget,
			}
			if e.IneffectiveNote != "" {
				meta["note"] = e.IneffectiveNote
			}
			send(sessionID, CompactionSummaryChunk{
				SessionUpdate: UpdateCompactionSummaryChunk,
				CompactionID:  id,
				Content:       textContent("[" + e.DoneSummary() + "]"),
			})
			send(sessionID, CompactionUpdate{
				SessionUpdate: UpdateCompaction,
				CompactionID:  id,
				Status:        CompactionCompleted,
				Meta:          meta,
			})
			break
		}
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent("[" + e.DoneSummary() + "]"),
		})
	case *agent.TitleSetEvent:
		// Live session metadata: the persisted title transitioned from
		// empty. Part of the update stream (not capability-gated in v1),
		// matching what session/list would read back from the store.
		send(sessionID, SessionInfoUpdate{
			SessionUpdate: UpdateSessionInfo,
			Title:         e.Title,
			UpdatedAt:     time.Unix(e.UpdatedAt, 0).UTC().Format(time.RFC3339),
		})
	case *agent.DoneEvent:
		// End-of-turn usage: the context occupancy mirrors the
		// control.ContextInfo path, and the _meta bag carries the
		// engine's cumulative token tally, finish reason, and response
		// model. The turn completion itself still rides the
		// session/prompt response stopReason, not DoneEvent. Cumulative
		// cost is not emitted: yaah has no pricing source outside the
		// TUI's estimate table, and a wrong cost renders worse than no
		// cost (the field is optional in the schema).
		used := e.LastPromptTokensInclusive
		if used == 0 {
			used = e.ContextTokens
		}
		size := e.ContextWindow
		if size <= 0 {
			size = used
		}
		send(sessionID, UsageUpdate{
			SessionUpdate: UpdateUsageUpdate,
			Used:          used,
			Size:          size,
			Meta:          usageMeta(e.Usage, e.FinishReason, e.ResponseModel),
		})
	default:
		return
	}
}

// ViewWithWrite wraps View and sends session/update notifications
// for each event, tagging them with the active session ID.
type ViewWithWrite struct {
	av        *View
	send      updateSender
	sessionID string
}

// NewViewWithWrite creates an agent.View that forwards translated events
// to send as session/update payloads for sessionID.
func NewViewWithWrite(send updateSender, sessionID string) *ViewWithWrite {
	return &ViewWithWrite{
		av:        NewView(),
		send:      send,
		sessionID: sessionID,
	}
}

// HandleEvent implements agent.View.
func (v *ViewWithWrite) HandleEvent(evt agent.Event) {
	v.av.SendTo(v.sessionID, v.send, evt)
}

// EnableCompaction turns on ID-addressed compaction updates (see
// agent.View.EnableCompaction). The server calls it per prompt turn
// when the connection advertised session.compaction.
func (v *ViewWithWrite) EnableCompaction() {
	v.av.EnableCompaction()
}

// Announce marks a tool call as created by a permission request.
func (v *ViewWithWrite) Announce(callID string) {
	v.av.Announce(callID)
}

// compile-time check
var _ agent.View = (*ViewWithWrite)(nil)
