package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/types"
)

// tracePayloadMaxBytes caps every free-text field written into the trace. The
// kernel itself imposes no payload cap; this mirrors the 64KiB session-prompt
// precedent and the tool-result truncation point, and keeps a runaway model
// response or a multi-megabyte file read from turning the trace into a second
// copy of the workspace.
const tracePayloadMaxBytes = 64 * 1024

// traceSpillPattern recovers the spill path the loop's truncation hint already
// carries. The trace does not need to re-spill: the full body is already on
// disk, and the fact only needs a stable reference to it.
var traceSpillPattern = regexp.MustCompile(`full output at (.+?)\. To read portions`)

// TraceSessionContext is the immutable session-start context recorded once per
// trace owner. SystemPrompt is capped like every other free-text field.
type TraceSessionContext struct {
	SystemPrompt  string
	CWD           string
	Model         string
	Provider      string
	ToolNames     []string
	PipelineNames []string
}

// RecordAssistant appends the assistant response for one model turn: content,
// reasoning content and its replay signature, and tool-call intents. It is a
// capture because it records what the model emitted; the turn's declaration is
// the prompt that caused it.
//
// The agent loop calls this for every model response, including the final
// tool-free response, which never reaches PostModel.
func (m *ShepherdTraceMiddleware) RecordAssistant(ctx context.Context, msg *types.Message, turn int) {
	if m.store == nil || m.sessionID == "" || msg == nil {
		return
	}
	if msg.Content == "" && msg.ReasoningContent == "" && len(msg.ToolCalls) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	m.ordinal++

	payload := m.turnPayload(turn)
	if msg.Content != "" {
		text, truncated := capTraceText(msg.Content)
		payload["content"] = text
		payload["content_bytes"] = len(msg.Content)
		if truncated {
			payload["content_truncated"] = true
		}
	}
	if msg.ReasoningContent != "" {
		text, truncated := capTraceText(msg.ReasoningContent)
		payload["reasoning_content"] = text
		payload["reasoning_bytes"] = len(msg.ReasoningContent)
		if truncated {
			payload["reasoning_truncated"] = true
		}
	}
	if msg.ReasoningSignature != "" {
		sig, truncated := capTraceText(msg.ReasoningSignature)
		payload["reasoning_signature"] = sig
		if truncated {
			payload["reasoning_signature_truncated"] = true
		}
	}
	if msg.Refusal != "" {
		refusal, truncated := capTraceText(msg.Refusal)
		payload["refusal"] = refusal
		if truncated {
			payload["refusal_truncated"] = true
		}
	}
	if len(msg.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			args, truncated := capTraceText(call.Function.Arguments)
			entry := map[string]any{
				"id":   call.ID,
				"type": call.Type,
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": args,
				},
			}
			if truncated {
				entry["arguments_truncated"] = true
			}
			calls = append(calls, entry)
		}
		payload["tool_calls"] = calls
	}

	intentID := fmt.Sprintf("%s:assistant:%d", m.sessionID, m.ordinal)
	receipt, err := m.append(ctx, shepherdtrace.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.lastFactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.message.assistant.v1",
				KindLabel: "assistant:message",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("shepherd_trace: assistant message failed", "err", err)
		return
	}
	m.lastFactIDs = receipt.FactIDs
}

// RecordSessionContext appends the start-of-session context: the system prompt
// (capped), cwd, model/provider, a config fingerprint, and — for sub-agents —
// the dispatch's retained context (substrate, containment, tool belt).
//
// The loop calls it every turn; the middleware records the first call only.
// That keeps the fact a session baseline while allowing a caller that
// re-constructs the pipeline mid-run to seed a new owner exactly once.
func (m *ShepherdTraceMiddleware) RecordSessionContext(ctx context.Context, in TraceSessionContext) {
	if m.store == nil || m.sessionID == "" || m.sessionContextRecorded {
		return
	}
	m.sessionContextRecorded = true
	ctx = context.WithoutCancel(ctx)
	m.ordinal++

	payload := map[string]any{"session_id": m.sessionID}
	if m.turnID != "" {
		payload["turn_id"] = m.turnID
	}
	if in.SystemPrompt != "" {
		text, truncated := capTraceText(in.SystemPrompt)
		payload["system_prompt"] = text
		payload["system_prompt_bytes"] = len(in.SystemPrompt)
		if truncated {
			payload["system_prompt_truncated"] = true
		}
	}
	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	payload["cwd"] = cwd
	if in.Model != "" {
		payload["model"] = in.Model
	}
	if in.Provider != "" {
		payload["provider"] = in.Provider
	}
	if digest, ok := traceConfigDigest(in); ok {
		payload["config_digest"] = digest
	}
	if len(in.ToolNames) > 0 {
		tools := append([]string(nil), in.ToolNames...)
		sort.Strings(tools)
		payload["tools"] = tools
	}
	if len(in.PipelineNames) > 0 {
		pipelines := append([]string(nil), in.PipelineNames...)
		sort.Strings(pipelines)
		payload["pipelines"] = pipelines
	}
	if m.retainedContext != nil {
		payload["retained_context"] = retainedContextPayload(m.retainedContext)
	}

	intentID := fmt.Sprintf("%s:session_context:%d", m.sessionID, m.ordinal)
	receipt, err := m.append(ctx, shepherdtrace.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.lastFactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.session.context.v1",
				KindLabel: "session:context",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("shepherd_trace: session context failed", "err", err)
		return
	}
	m.lastFactIDs = receipt.FactIDs
}

// traceToolOutput returns the output fields merged into a tool capture. The
// body is capped; a spill path already embedded in the loop's truncation hint
// is preserved as a reference.
func traceToolOutput(result string) map[string]any {
	if result == "" {
		return nil
	}
	out := map[string]any{"output_bytes": len(result)}
	text, truncated := capTraceText(result)
	out["output"] = text
	if truncated {
		out["output_truncated"] = true
	}
	if strings.Contains(result, "[output truncated") || strings.Contains(result, "[truncated]") {
		out["output_truncated"] = true
	}
	if match := traceSpillPattern.FindStringSubmatch(result); len(match) == 2 {
		out["output_spill"] = strings.TrimSpace(match[1])
	}
	return out
}

func capTraceText(s string) (string, bool) {
	if len(s) <= tracePayloadMaxBytes {
		return s, false
	}
	cut := tracePayloadMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n...[truncated at %d bytes]...", tracePayloadMaxBytes), true
}

func traceConfigDigest(in TraceSessionContext) (string, bool) {
	type fingerprint struct {
		Model     string   `json:"model,omitempty"`
		Provider  string   `json:"provider,omitempty"`
		Tools     []string `json:"tools,omitempty"`
		Pipelines []string `json:"pipelines,omitempty"`
	}
	tools := append([]string(nil), in.ToolNames...)
	sort.Strings(tools)
	pipelines := append([]string(nil), in.PipelineNames...)
	sort.Strings(pipelines)
	raw, err := json.Marshal(fingerprint{Model: in.Model, Provider: in.Provider, Tools: tools, Pipelines: pipelines})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), true
}

func retainedContextPayload(rc *shepherdtrace.RetainedContext) map[string]any {
	return map[string]any{
		"substrate_ref":             rc.SubstrateRef,
		"containment":               string(rc.Containment),
		"active_binding_refs":       append([]string(nil), rc.ActiveBindingRefs...),
		"capability_witness_refs":   append([]string(nil), rc.CapabilityWitnessRefs...),
		"semantic_environment_refs": append([]string(nil), rc.SemanticEnvironmentRefs...),
		"visibility_policy_refs":    append([]string(nil), rc.VisibilityPolicyRefs...),
	}
}
