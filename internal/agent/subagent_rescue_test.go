package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// explodeTool always fails: the plan's "sub-agent that keeps failing a
// tool", whose repeated failures the HighErrorRate rule must see.
type explodeTool struct{ calls int }

func (t *explodeTool) Name() string        { return "explode" }
func (t *explodeTool) Description() string { return "always fails" }
func (t *explodeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *explodeTool) Execute(ctx context.Context, args string) (string, error) {
	t.calls++
	return "", errors.New("kaboom")
}

// TestSubAgentLoop_ErrorRateRuleRescue is the plan's Phase B exit
// criterion (B3): a sub-agent that keeps failing a tool is rescued
// with zero human effort — the advisory engine watches its trace,
// the HighErrorRate rule fires, the drain delivers rule-named
// guidance through the A2 bridge, and the loop's steer middleware
// surfaces it as a high-priority user message the next turn. The loop
// then finishes.
func TestSubAgentLoop_ErrorRateRuleRescue(t *testing.T) {
	// The supervision stack, assembled as the wiring does it.
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open trace store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)
	mgr := shepherd.NewScopeManager(store)
	if _, err := mgr.Create("sub-worker", nil); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	// The sub-agent pipeline's shepherd_trace middleware writes through
	// the session-shared store global; borrow it for the run and put it
	// back.
	saved := tools.SharedTraceStore
	tools.SharedTraceStore = store
	t.Cleanup(func() { tools.SharedTraceStore = saved })

	steerCh := make(chan string, supervision.SteerBuffer)
	runs := supervision.NewRegistry()
	runs.Register("scope:sub-worker", supervision.Run{SteerCh: steerCh})

	svc := supervision.NewService(mgr, bus, runs, supervision.AdvisoryRules{
		HighErrorRate:      true,
		ErrorRateThreshold: 0.5,
		// Window 2: the rule evaluates from one result on, so the first
		// failing capture already fires.
		ErrorRateWindow: 2,
	}, false, false, "scope:sess-root")
	svc.Start("rescue-test")
	t.Cleanup(svc.Close)

	// The sub-agent loop: two failing turns (the second gives the async
	// drain a beat), then the model wraps up.
	explode := &explodeTool{}
	reg := tools.NewEmptyRegistry()
	reg.Register(explode)
	fp := &fakeProvider{responses: []*types.ChatResponse{
		steerToolCallResponse("tc1", "explode"),
		steerToolCallResponse("tc2", "explode"),
		{
			Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "recovered"}, FinishReason: "stop"}},
			Usage:   types.Usage{TotalTokens: 5},
		},
	}}

	loop := NewSubAgentLoop(fp, reg, "test-model", "sys", SubAgentConfig{
		MaxLoopCycles: 5,
		MaxToolTurns:  5,
		SessionID:     "sub-worker",
		SteerCh:       steerCh,
	})

	res, err := loop.Run(context.Background(), "keep trying the broken thing")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != "recovered" {
		t.Errorf("Run = %q, want the loop to finish after the rescue", res)
	}
	if explode.calls == 0 {
		t.Fatal("the failing tool never ran; the rescue scenario is not exercised")
	}

	// The rescue evidence: some turn's prompt must carry the
	// rule-named guidance via the steer bridge.
	var steered bool
	for _, req := range fp.requests {
		for _, m := range req.Messages {
			if m.Role == "user" && strings.Contains(m.Content, "[STEER]") &&
				strings.Contains(m.Content, "[error_rate_guard]") {
				steered = true
			}
		}
	}
	if !steered {
		t.Error("no turn's prompt carries the injected [error_rate_guard] guidance; the rescue did not happen")
	}

	// And the firing is recorded in the target scope's trace — the A2
	// invariant: the fact is the record.
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "sub-worker", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var recorded bool
	for _, id := range slice.FactIDs() {
		if slice.FactsByID[id].GetEnvelope().SchemaRef == shepherd.SchemaSupervisorInject {
			recorded = true
		}
	}
	if !recorded {
		t.Error("the rescue is not recorded in the sub-agent's trace")
	}
}
