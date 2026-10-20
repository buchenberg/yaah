package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"
	"github.com/buchenberg/yaah/internal/types"
)

// NewShepherdTraceStore opens or creates a Shepherd trace store at the given path.
func NewShepherdTraceStore(path string) (*shepherdtrace.SQLiteTraceStore, error) {
	return shepherdtrace.NewSQLiteTraceStore(path)
}

// ShepherdTraceMiddleware records every tool call as a declaration (intent)
// and capture (observation) in a Shepherd trace store.
type ShepherdTraceMiddleware struct {
	store           *shepherdtrace.SQLiteTraceStore
	bus             *shepherdtrace.EffectBus
	scopeManager    *shepherd.ScopeManager
	sessionID       string
	ordinal         int
	lastFactIDs     []string
	turnRootFactIDs []string

	// turnID is the stable per-interaction ID set by the agent loop via
	// SetTurnContext; stamped into turn-lifecycle fact payloads so facts
	// join to OTel spans and persisted messages of the same interaction.
	turnID string

	// ownsStore controls whether Close closes the backing store.
	// Middleware built on the session-shared store (sub-agent pipelines)
	// must not close it — the store outlives any single sub-agent loop.
	ownsStore bool
}

var nextOrdinal atomic.Int64

// NewShepherdTraceMiddleware creates a trace middleware that owns the
// given store — Close will close it.
func NewShepherdTraceMiddleware(store *shepherdtrace.SQLiteTraceStore, sessionID string) *ShepherdTraceMiddleware {
	return &ShepherdTraceMiddleware{
		store:     store,
		sessionID: sessionID,
		ordinal:   int(nextOrdinal.Add(1 << 20)),
		ownsStore: true,
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
	if m.store == nil || m.sessionID == "" {
		return step, nil
	}

	for _, r := range results {
		m.ordinal++

		intentID := fmt.Sprintf("%s:tool:%d", m.sessionID, m.ordinal)
		schemaRef := fmt.Sprintf("yaah.tool.%s.v1", r.Name)

		// Wrap args as RawMessage only if valid JSON; otherwise store as string.
		var argsJSON json.RawMessage
		if json.Valid([]byte(r.Args)) {
			argsJSON = json.RawMessage(r.Args)
		} else {
			argsJSON, _ = json.Marshal(r.Args)
		}

		payload := map[string]any{
			"tool": r.Name,
			"args": argsJSON,
		}
		// Tool-observed supervision facts ride the declaration so the
		// advisory rules see what the tool saw (e.g. whether the write
		// target already existed) instead of re-deriving it — the
		// middleware has no workspace of its own.
		for k, v := range r.Meta {
			payload[k] = v
		}
		// The kernel's supervision rules read the shell command from
		// the declaration payload's "cmd" (shepherd-adoption plan B1:
		// the advisory destructive rule matches patterns in it). yaah's
		// tool schemas call the field "command", and the shell tool is
		// bash on POSIX but powershell on Windows; copy it across for
		// both so rule events and trace facts stay one shape on every
		// platform the rules evaluate.
		if r.Name == "bash" || r.Name == "powershell" {
			var args struct {
				Command string `json:"command"`
			}
			if json.Unmarshal([]byte(r.Args), &args) == nil && args.Command != "" {
				payload["cmd"] = args.Command
			}
		}

		receipt, err := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
			AppendIntentID: intentID,
			Groups: []shepherdtrace.AppendGroup{{
				TraceOwnerID:  m.sessionID,
				CausalParents: m.lastFactIDs,
				FactDrafts: []shepherdtrace.RecordDraft{{
					Mode:      shepherdtrace.Declaration,
					SchemaRef: schemaRef,
					KindLabel: r.Name,
					Payload:   payload,
				}},
			}},
		})
		if err != nil {
			slog.Debug("shepherd_trace: declaration failed", "err", err)
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
		captureReceipt, err := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
			AppendIntentID: captureIntentID,
			Groups: []shepherdtrace.AppendGroup{{
				TraceOwnerID:  m.sessionID,
				CausalParents: receipt.FactIDs,
				FactDrafts: []shepherdtrace.RecordDraft{{
					Mode:      shepherdtrace.Capture,
					SchemaRef: schemaRef + ".applied",
					KindLabel: r.Name + ":result",
					Payload:   capturePayload,
				}},
			}},
		})
		if err != nil {
			slog.Debug("shepherd_trace: capture failed", "err", err)
		}

		m.lastFactIDs = captureReceipt.FactIDs
	}

	return step, nil
}

// SetTurnContext records the stable per-interaction turn ID (generated by
// the agent loop) so every turn-lifecycle fact payload carries it. This
// makes Shepherd facts joinable to the OTel spans and messages rows of the
// same interaction. Safe to call before each Run.
func (m *ShepherdTraceMiddleware) SetTurnContext(turnID string) {
	m.turnID = turnID
}

// turnPayload returns the common turn-lifecycle payload fields including
// the cross-link IDs (session_id is the trace owner, which equals the
// session ID for main agents and the synthetic sub-ID for sub-agents).
func (m *ShepherdTraceMiddleware) turnPayload(turnNumber int) map[string]any {
	payload := map[string]any{
		"turn":       turnNumber,
		"session_id": m.sessionID,
	}
	if m.turnID != "" {
		payload["turn_id"] = m.turnID
	}
	return payload
}

// StartTurn records an execution-lifecycle declaration+capture pair marking
// the start of a turn. Returns the ordinal of the created record.
func (m *ShepherdTraceMiddleware) StartTurn(ctx context.Context, turnNumber int, model string, prompt string) {
	if m.store == nil || m.sessionID == "" {
		return
	}
	// Turn records are audit facts: they must land even when the caller's
	// context is already cancelled (FailTurn is reached precisely on the
	// cancelled path), so the store writes run cancel-free.
	ctx = context.WithoutCancel(ctx)
	m.ordinal++

	intentID := fmt.Sprintf("%s:turn:%d", m.sessionID, m.ordinal)
	payload := m.turnPayload(turnNumber)
	payload["model"] = model
	if prompt != "" {
		payload["prompt"] = prompt
	}

	receipt, err := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.lastFactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: "yaah.execution.created.v1",
				KindLabel: "turn:created",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("shepherd_trace: turn start failed", "err", err)
		return
	}

	m.turnRootFactIDs = receipt.FactIDs

	startPayload := m.turnPayload(turnNumber)
	startPayload["model"] = model
	captureIntentID := fmt.Sprintf("%s:turn_start:%d", m.sessionID, m.ordinal)
	captureReceipt, err := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: captureIntentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: receipt.FactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.execution.started.v1",
				KindLabel: "turn:started",
				Payload:   startPayload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("shepherd_trace: turn start capture failed", "err", err)
	} else {
		m.lastFactIDs = captureReceipt.FactIDs
	}
}

// EndTurn records a turn completion capture.
func (m *ShepherdTraceMiddleware) EndTurn(ctx context.Context, turnNumber int, promptTokens, completionTokens int) {
	if m.store == nil || m.sessionID == "" {
		return
	}
	// Audit facts must land regardless of cancellation (see StartTurn).
	ctx = context.WithoutCancel(ctx)
	m.ordinal++

	payload := m.turnPayload(turnNumber)
	payload["success"] = true
	payload["prompt_tokens"] = promptTokens
	payload["completion_tokens"] = completionTokens

	intentID := fmt.Sprintf("%s:turn_done:%d", m.sessionID, m.ordinal)
	receipt, err := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.turnRootFactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.execution.completed.v1",
				KindLabel: "turn:completed",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("shepherd_trace: turn complete failed", "err", err)
	} else {
		m.lastFactIDs = receipt.FactIDs
	}

	m.publishFrontier(ctx, turnNumber)
}

// FailTurn records a turn failure capture.
func (m *ShepherdTraceMiddleware) FailTurn(ctx context.Context, turnNumber int, err error) {
	if m.store == nil || m.sessionID == "" {
		return
	}
	// Audit facts must land regardless of cancellation (see StartTurn).
	ctx = context.WithoutCancel(ctx)
	m.ordinal++

	payload := m.turnPayload(turnNumber)
	payload["success"] = false
	payload["error"] = err.Error()

	intentID := fmt.Sprintf("%s:turn_fail:%d", m.sessionID, m.ordinal)
	receipt, aerr := m.store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: intentID,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID:  m.sessionID,
			CausalParents: m.turnRootFactIDs,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.execution.failed.v1",
				KindLabel: "turn:failed",
				Payload:   payload,
			}},
		}},
	})
	if aerr != nil {
		slog.Debug("shepherd_trace: turn fail recording failed", "err", aerr)
	} else {
		m.lastFactIDs = receipt.FactIDs
	}

	m.publishFrontier(ctx, turnNumber)
}

// publishFrontier publishes an immutable cut snapshot at the current
// ordinal so the CLI can read stable turn boundaries.
func (m *ShepherdTraceMiddleware) publishFrontier(ctx context.Context, turnNumber int) {
	if len(m.lastFactIDs) == 0 {
		return
	}
	// The frontier cut is part of the turn's durable record (see StartTurn).
	ctx = context.WithoutCancel(ctx)

	spec := shepherdtrace.FrontierSpec{
		FrontierID:         fmt.Sprintf("%s:frontier:%d", m.sessionID, turnNumber),
		TargetTraceOwnerID: m.sessionID,
		ThroughFactID:      m.lastFactIDs[len(m.lastFactIDs)-1],
		AppendIntentID:     fmt.Sprintf("%s:frontier_intent:%d", m.sessionID, turnNumber),
	}
	_, err := m.store.PublishFrontier(ctx, shepherdtrace.TrustedAppendContext, spec)
	if err != nil {
		slog.Debug("shepherd_trace: frontier publish failed", "err", err)
	}
}

// Ordinal returns the current trace ordinal.
func (m *ShepherdTraceMiddleware) Ordinal() int {
	return m.ordinal
}
func (m *ShepherdTraceMiddleware) Close(ctx context.Context) error {
	if m.store != nil && m.ownsStore {
		// Closing must not depend on the caller's cancellation: a Close
		// reached on an expired context would leak the store handle.
		return m.store.Close(context.WithoutCancel(ctx))
	}
	return nil
}

// Store returns the underlying trace store for inspection by CLI commands.
func (m *ShepherdTraceMiddleware) Store() *shepherdtrace.SQLiteTraceStore {
	return m.store
}

// SessionID returns the session identifier for this trace.
func (m *ShepherdTraceMiddleware) SessionID() string {
	return m.sessionID
}

// Bus returns the effect bus for real-time event subscription.
// Returns nil if supervision is not enabled.
func (m *ShepherdTraceMiddleware) Bus() *shepherdtrace.EffectBus {
	return m.bus
}

// ScopeManager returns the scope manager for fork/merge/discard operations.
// Returns nil if supervision is not enabled.
func (m *ShepherdTraceMiddleware) ScopeManager() *shepherd.ScopeManager {
	return m.scopeManager
}
