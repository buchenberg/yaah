package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime"
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
	mgr := goatherd.NewScopeManager(store)
	if _, err := mgr.Create("sub-worker", nil); err != nil {
		t.Fatalf("create scope: %v", err)
	}
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

	// The sub-agent pipeline's shepherd_trace middleware writes through
	// the session store injected here — the H4 seam, as the wiring does.
	loop := NewSubAgentLoop(fp, reg, "test-model", "sys", SubAgentConfig{
		MaxLoopCycles: 5,
		MaxToolTurns:  5,
		SessionID:     "sub-worker",
		SteerCh:       steerCh,
		TraceStore:    store,
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

	// The rescue evidence: the rule-named guidance reached the run.
	// Which sink holds it is a race the design owns — steering is
	// between-turn delivery, so the guidance is either drained into a
	// turn's prompt (asserted here) or still queued on the channel
	// when the loop finishes (asserted below). Either proves the
	// rescue; the middleware's prompt-side surfacing is separately
	// pinned by the A2 steer tests.
	delivered := func() bool {
		for _, req := range fp.requests {
			for _, m := range req.Messages {
				if m.Role == "user" && strings.Contains(m.Content, "[STEER]") &&
					strings.Contains(m.Content, "[error_rate_guard]") {
					return true
				}
			}
		}
		select {
		case g := <-steerCh:
			return strings.Contains(g, "[error_rate_guard]")
		default:
			return false
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for !delivered() {
		if !time.Now().Before(deadline) {
			t.Fatal("the [error_rate_guard] guidance never reached the run's steering channel")
		}
		time.Sleep(time.Millisecond)
	}

	// And the firing is recorded in the target scope's trace — the A2
	// invariant: the fact is the record.
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "sub-worker", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var recorded bool
	for _, id := range slice.FactIDs() {
		if slice.FactsByID[id].GetEnvelope().SchemaRef == goatherd.SchemaSupervisorInject {
			recorded = true
		}
	}
	if !recorded {
		t.Error("the rescue is not recorded in the sub-agent's trace")
	}
}
