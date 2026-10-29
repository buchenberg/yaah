package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/types"
)

// TraceTranscript is a provider-shaped conversation reconstructed from one
// trace owner's facts. It is the crash-safe mirror state.db cannot provide:
// the kernel's append intent closes the debounce window between a turn and a
// durable message row.
type TraceTranscript struct {
	SessionID    string          `json:"session_id"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	CWD          string          `json:"cwd,omitempty"`
	Model        string          `json:"model,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	ConfigDigest string          `json:"config_digest,omitempty"`
	Messages     []types.Message `json:"messages"`
}

// RebuildTranscript reads one owner's trace and reconstructs its messages.
// A sub-agent owner works the same way: its synthetic ID is the owner, and
// the parent/session mapping remains in memory.trace_owners.
func RebuildTranscript(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, ownerID string) (*TraceTranscript, error) {
	// through is an ordinal bound, not a row count. Use the largest int so a
	// long-lived owner never silently loses facts to an arbitrary ceiling.
	slice, err := store.ReadOwnerPrefix(ctx, shepherdtrace.TrustedReadContext, ownerID, int(^uint(0)>>1), shepherdtrace.ModeBoth)
	if err != nil {
		return nil, fmt.Errorf("read trace %s: %w", ownerID, err)
	}
	return RebuildTranscriptFromSlice(slice, ownerID), nil
}

// RebuildTranscriptFromSlice is the pure reconstruction over an already-read
// slice, used by the CLI and tests.
func RebuildTranscriptFromSlice(slice shepherdtrace.Slice, ownerID string) *TraceTranscript {
	out := &TraceTranscript{SessionID: ownerID}
	contextSeen := false
	lastUserTurnID := ""
	for _, factID := range ownerFactIDs(slice, ownerID) {
		fact, ok := slice.FactsByID[factID]
		if !ok {
			continue
		}
		record, ok := fact.(shepherdtrace.Record)
		if !ok {
			continue
		}
		kind := record.View.KindLabel
		payload := record.Body.Payload
		switch {
		case record.Envelope.Mode == shepherdtrace.Capture && kind == "session:context":
			if !contextSeen {
				contextSeen = true
				out.SystemPrompt = payloadString(payload, "system_prompt")
				out.CWD = payloadString(payload, "cwd")
				out.Model = payloadString(payload, "model")
				out.Provider = payloadString(payload, "provider")
				out.ConfigDigest = payloadString(payload, "config_digest")
			}
		case record.Envelope.Mode == shepherdtrace.Declaration && kind == "turn:created":
			prompt := payloadString(payload, "prompt")
			turnID := payloadString(payload, "turn_id")
			// StartTurn runs once per tool iteration with the same userInput.
			// The turn id is stable for the whole Run, so one user message per
			// Run is the faithful reconstruction; a resumed Run has a new id.
			switch {
			case turnID != "" && turnID == lastUserTurnID:
				// same interaction, later iteration
			case prompt == "":
				// nothing to add
			case turnID == "" && lastUserMessageIs(out.Messages, prompt):
				// legacy trace without a turn id: avoid back-to-back duplicates
			default:
				out.Messages = append(out.Messages, types.UserMsg(prompt))
				lastUserTurnID = turnID
			}
		case record.Envelope.Mode == shepherdtrace.Capture && kind == "assistant:message":
			msg := types.Message{
				Role:               "assistant",
				Content:            payloadString(payload, "content"),
				ReasoningContent:   payloadString(payload, "reasoning_content"),
				ReasoningSignature: payloadString(payload, "reasoning_signature"),
				Refusal:            payloadString(payload, "refusal"),
			}
			msg.ToolCalls = payloadToolCalls(payload["tool_calls"])
			out.Messages = append(out.Messages, msg)
		case record.Envelope.Mode == shepherdtrace.Capture && strings.HasSuffix(kind, ":result"):
			callID := payloadString(payload, "tool_call_id")
			if callID == "" {
				// Legacy captures (pre-content facts) carry no call id and
				// cannot be joined without guessing. They remain in the
				// trace but are not fabricated into the transcript.
				continue
			}
			content := payloadString(payload, "output")
			if content == "" {
				if errText := payloadString(payload, "error"); errText != "" {
					content = "error: " + errText
				}
			}
			out.Messages = append(out.Messages, types.ToolResultMsg(callID, payloadString(payload, "tool"), content))
		}
	}
	if out.SystemPrompt != "" {
		out.Messages = append([]types.Message{types.SystemMsg(out.SystemPrompt)}, out.Messages...)
	}
	if out.Messages == nil {
		out.Messages = []types.Message{}
	}
	return out
}

func ownerFactIDs(slice shepherdtrace.Slice, ownerID string) []string {
	if ids, ok := slice.OwnerPaths[ownerID]; ok {
		return ids
	}
	// A caller can hand us an unfiltered slice. Never let another owner's
	// messages leak into this transcript: filter by the fact view instead of
	// falling back to every fact in the store.
	var ids []string
	for _, id := range slice.FactIDs() {
		if view := slice.FactsByID[id].GetView(); view != nil && view.TraceOwnerID == ownerID {
			ids = append(ids, id)
		}
	}
	return ids
}

func lastUserMessageIs(messages []types.Message, content string) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	return last.Role == "user" && last.Content == content
}

func payloadString(payload map[string]any, key string) string {
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.RawMessage:
		return string(x)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func payloadToolCalls(v any) []types.ToolCall {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var calls []types.ToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	return calls
}
