package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/types"
)

// recordSubAgentUsage persists the child's cumulative usage as a trace fact.
// The orchestrator's session totals fold child usage in, so state.db exposes
// only the aggregate sub-agent share. This fact keeps the per-dispatch and
// per-role split available to benchmark metrics and trace readers.
func recordSubAgentUsage(ctx context.Context, store pipeline.TraceSink, sessionID, parentSession, role, model, lane string, usage types.Usage) {
	if store == nil || sessionID == "" {
		return
	}
	payload := map[string]any{
		"role":                 role,
		"model":                model,
		"lane":                 lane,
		"parent_session":       parentSession,
		"prompt_tokens":        usage.PromptTokens,
		"completion_tokens":    usage.CompletionTokens,
		"total_tokens":         usage.TotalTokens,
		"cached_prompt_tokens": usage.CacheTokens(),
	}
	_, err := store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: sessionID + ":usage",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: sessionID,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.subagent.usage.v1",
				KindLabel: "subagent:usage",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("subagent usage trace failed", "session", sessionID, "err", err)
	}
}

func subagentTraceProfile(ctx context.Context, tracePath, sessionID string) string {
	// Do not open the store unless it already exists: NewSQLiteTraceStore
	// creates the file, so probing speculatively would conjure an empty
	// store at the default path as a side effect of a failed dispatch.
	if _, err := os.Stat(tracePath); err != nil {
		return ""
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return ""
	}
	// The whole read is deliberately cancel-free, not just the Close: this
	// profile exists to enrich a failed sub-agent, and the failure it
	// diagnoses is often the very cancellation that would abort the read.
	defer store.Close(context.WithoutCancel(ctx))

	slice, err := store.ReadOwnerPrefix(context.WithoutCancel(ctx), shepherdtrace.TrustedReadContext, sessionID, 99999, shepherdtrace.ModeBoth)
	if err != nil || len(slice.FactIDs()) == 0 {
		return ""
	}

	captureByParent := make(map[string]struct {
		success bool
		errMsg  string
	})
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherdtrace.Capture {
			continue
		}
		causedBy := fact.GetEnvelope().CausedByIDs
		if len(causedBy) == 0 {
			continue
		}
		parentID := causedBy[0]
		if _, exists := captureByParent[parentID]; exists {
			continue
		}
		rec, ok := fact.(shepherdtrace.Record)
		if !ok {
			continue
		}
		success, _ := rec.Body.Payload["success"].(bool)
		errMsg := ""
		if e, ok2 := rec.Body.Payload["error"].(string); ok2 {
			errMsg = e
		}
		captureByParent[parentID] = struct {
			success bool
			errMsg  string
		}{success, errMsg}
	}

	var lines []string
	seq := 0
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherdtrace.Declaration {
			continue
		}
		kind := fact.GetView().KindLabel
		if kind == "turn:created" || kind == "turn:started" || kind == "turn:completed" || kind == "turn:failed" {
			continue
		}
		seq++
		cap := captureByParent[factID]
		status := "ok"
		if cap.errMsg != "" {
			status = "error: " + cap.errMsg
		}
		lines = append(lines, fmt.Sprintf("  T%d %s  %s", seq, kind, status))
	}

	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%d tool calls:\n%s", seq, strings.Join(lines, "\n"))
}
