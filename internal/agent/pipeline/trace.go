package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/types"
)

// NewShepherdTraceStore opens or creates a Shepherd trace store at the given path.
func NewShepherdTraceStore(path string) (*shepherd.SQLiteTraceStore, error) {
	return shepherd.NewSQLiteTraceStore(path)
}

// ShepherdTraceMiddleware records every tool call as a declaration (intent)
// and capture (observation) in a Shepherd trace store.
type ShepherdTraceMiddleware struct {
	store           *shepherd.SQLiteTraceStore
	sessionID       string
	ordinal         int
	lastFactIDs     []string
	turnRootFactIDs []string
}

var nextOrdinal atomic.Int64

// NewShepherdTraceMiddleware creates a trace middleware backed by the given store.
func NewShepherdTraceMiddleware(store *shepherd.SQLiteTraceStore, sessionID string) *ShepherdTraceMiddleware {
	return &ShepherdTraceMiddleware{
		store:     store,
		sessionID: sessionID,
		ordinal:   int(nextOrdinal.Add(1 << 20)),
	}
}

func (m *ShepherdTraceMiddleware) Name() string { return "shepherd_trace" }

func (m *ShepherdTraceMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ShepherdTraceMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *ShepherdTraceMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.store == nil {
		return step, nil
	}

	for _, r := range results {
		m.ordinal++

		intentID := fmt.Sprintf("%s:tool:%d", m.sessionID, m.ordinal)
		schemaRef := fmt.Sprintf("yaah.tool.%s.v1", r.Name)

		payload := map[string]any{
			"tool": r.Name,
			"args": json.RawMessage(r.Args),
		}

		receipt, err := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: intentID,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID:  m.sessionID,
				CausalParents: m.lastFactIDs,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Declaration,
					SchemaRef: schemaRef,
					KindLabel: r.Name,
					Payload:   payload,
				}},
			}},
		})
		if err != nil {
			slog.Error("shepherd_trace: declaration failed", "err", err)
			continue
		}

		capturePayload := map[string]any{
			"tool":     r.Name,
			"success":  r.Error == nil,
			"duration": r.Duration.String(),
		}
		if r.Error != nil {
			capturePayload["error"] = r.Error.Error()
		}

		captureIntentID := fmt.Sprintf("%s:result:%d", m.sessionID, m.ordinal)
		captureReceipt, err := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: captureIntentID,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID:  m.sessionID,
				CausalParents: receipt.FactIDs,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Capture,
					SchemaRef: schemaRef + ".applied",
					KindLabel: r.Name + ":result",
					Payload:   capturePayload,
				}},
			}},
		})
		if err != nil {
			slog.Error("shepherd_trace: capture failed", "err", err)
		}

		m.lastFactIDs = captureReceipt.FactIDs
	}

	return step, nil
}

// StartTurn records an execution-lifecycle declaration+capture pair marking
// the start of a turn. Returns the ordinal of the created record.
func (m *ShepherdTraceMiddleware) StartTurn(turnNumber int, model string, prompt string) {
	if m.store == nil {
		return
	}
	m.ordinal++

	intentID := fmt.Sprintf("%s:turn:%d", m.sessionID, m.ordinal)
	payload := map[string]any{
		"turn":  turnNumber,
		"model": model,
	}
	if prompt != "" {
		payload["prompt"] = prompt
	}

	receipt, err := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.lastFactIDs,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Declaration,
				SchemaRef: "yaah.execution.created.v1",
				KindLabel: "turn:created",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Error("shepherd_trace: turn start failed", "err", err)
		return
	}

	m.turnRootFactIDs = receipt.FactIDs

	startPayload := map[string]any{
		"turn":  turnNumber,
		"model": model,
	}
	captureIntentID := fmt.Sprintf("%s:turn_start:%d", m.sessionID, m.ordinal)
	captureReceipt, err := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: captureIntentID,
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: receipt.FactIDs,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Capture,
				SchemaRef: "yaah.execution.started.v1",
				KindLabel: "turn:started",
				Payload:   startPayload,
			}},
		}},
	})
	if err != nil {
		slog.Error("shepherd_trace: turn start capture failed", "err", err)
	} else {
		m.lastFactIDs = captureReceipt.FactIDs
	}
}

// EndTurn records a turn completion capture.
func (m *ShepherdTraceMiddleware) EndTurn(turnNumber int, promptTokens, completionTokens int) {
	if m.store == nil {
		return
	}
	m.ordinal++

	payload := map[string]any{
		"turn":              turnNumber,
		"success":           true,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
	}

	intentID := fmt.Sprintf("%s:turn_done:%d", m.sessionID, m.ordinal)
	receipt, err := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.turnRootFactIDs,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Capture,
				SchemaRef: "yaah.execution.completed.v1",
				KindLabel: "turn:completed",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Error("shepherd_trace: turn complete failed", "err", err)
	} else {
		m.lastFactIDs = receipt.FactIDs
	}

	m.publishFrontier(turnNumber)
}

// FailTurn records a turn failure capture.
func (m *ShepherdTraceMiddleware) FailTurn(turnNumber int, err error) {
	if m.store == nil {
		return
	}
	m.ordinal++

	payload := map[string]any{
		"turn":    turnNumber,
		"success": false,
		"error":   err.Error(),
	}

	intentID := fmt.Sprintf("%s:turn_fail:%d", m.sessionID, m.ordinal)
	receipt, aerr := m.store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.turnRootFactIDs,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Capture,
				SchemaRef: "yaah.execution.failed.v1",
				KindLabel: "turn:failed",
				Payload:   payload,
			}},
		}},
	})
	if aerr != nil {
		slog.Error("shepherd_trace: turn fail recording failed", "err", aerr)
	} else {
		m.lastFactIDs = receipt.FactIDs
	}

	m.publishFrontier(turnNumber)
}

// publishFrontier publishes an immutable cut snapshot at the current
// ordinal so the CLI can read stable turn boundaries.
func (m *ShepherdTraceMiddleware) publishFrontier(turnNumber int) {
	if len(m.lastFactIDs) == 0 {
		return
	}

	spec := shepherd.FrontierSpec{
		FrontierID:         fmt.Sprintf("%s:frontier:%d", m.sessionID, turnNumber),
		TargetTraceOwnerID: m.sessionID,
		ThroughFactID:      m.lastFactIDs[len(m.lastFactIDs)-1],
		AppendIntentID:     fmt.Sprintf("%s:frontier_intent:%d", m.sessionID, turnNumber),
	}
	_, err := m.store.PublishFrontier(shepherd.TrustedAppendContext, spec)
	if err != nil {
		slog.Error("shepherd_trace: frontier publish failed", "err", err)
	}
}

// Ordinal returns the current trace ordinal.
func (m *ShepherdTraceMiddleware) Ordinal() int {
	return m.ordinal
}
func (m *ShepherdTraceMiddleware) Close() error {
	if m.store != nil {
		return m.store.Close()
	}
	return nil
}

// Store returns the underlying trace store for inspection by CLI commands.
func (m *ShepherdTraceMiddleware) Store() *shepherd.SQLiteTraceStore {
	return m.store
}

// SessionID returns the session identifier for this trace.
func (m *ShepherdTraceMiddleware) SessionID() string {
	return m.sessionID
}
