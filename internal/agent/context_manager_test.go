package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
)

// compactionRecordingProvider records every compaction request. It can force
// every reply to be reasoning-only (the shape a reasoning small-model produces
// when it exhausts its output budget before emitting content).
type compactionRecordingProvider struct {
	mu        sync.Mutex
	requests  []types.ChatRequest
	responses []*types.ChatResponse
	index     int

	emptyAll bool
}

func (p *compactionRecordingProvider) Send(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)

	if p.emptyAll {
		return thoughtOnlyResponse(), nil
	}
	if p.index < len(p.responses) {
		resp := p.responses[p.index]
		p.index++
		return resp, nil
	}
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "summary"},
			FinishReason: "stop",
		}},
	}, nil
}

func (p *compactionRecordingProvider) snapshot() []types.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]types.ChatRequest(nil), p.requests...)
}

func thoughtOnlyResponse() *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role:             "assistant",
				ReasoningContent: "thinking until the output budget runs out",
			},
			FinishReason: "length",
		}},
	}
}

// compactionTestManager builds a manager whose conversation is large enough to
// require chunked compaction, so every request site (primary, chunk, merge) is
// exercised.
func compactionTestManager(p Provider) *ContextManager {
	return &ContextManager{
		ContextWindow:   6000,
		EstimateFactor:  1.3,
		Provider:        p,
		CompactProvider: p,
		CompactModel:    "test-small-model",
		State: &LoopState{
			Messages:                   largeConversation(80),
			CompactionBudgetMultiplier: 1.0,
		},
	}
}

func TestCompactContextDoesNotPinMaxTokens(t *testing.T) {
	p := &compactionRecordingProvider{responses: []*types.ChatResponse{thoughtOnlyResponse()}}
	cm := compactionTestManager(p)

	cm.compactContext(context.Background(), 0.25)

	reqs := p.snapshot()
	if len(reqs) < 3 {
		t.Fatalf("expected primary, chunk, and merge requests; got %d", len(reqs))
	}
	for i, req := range reqs {
		if req.MaxTokens != 0 {
			t.Errorf("request %d pins MaxTokens=%d; compaction must leave the cap to the provider", i, req.MaxTokens)
		}
	}
	if cm.State.PreviousSummary == "" {
		t.Error("expected chunked compaction to recover from an empty primary summary")
	}
}

func TestCompactContextRejectsThoughtOnlySummary(t *testing.T) {
	p := &compactionRecordingProvider{emptyAll: true}
	cm := compactionTestManager(p)

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	reqs := p.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected a chunked fallback attempt after the empty primary summary; got %d requests", len(reqs))
	}
	if cm.State.PreviousSummary != "" {
		t.Errorf("thought-only response was accepted as a summary: %q", cm.State.PreviousSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Errorf("expected heuristic trim after every summary attempt failed: before=%d after=%d", before, len(cm.State.Messages))
	}
	if cm.State.Messages[0].Role != "system" {
		t.Errorf("system message lost during fallback: role=%q", cm.State.Messages[0].Role)
	}
}

// TestCompactContextAnthropicRawGate: cache-heavy Anthropic usage reports a
// small uncached prompt, but the context is large. The raw gate must use the
// inclusive size or compaction never fires.
func TestCompactContextAnthropicRawGate(t *testing.T) {
	p := &compactionRecordingProvider{}
	cm := &ContextManager{
		ContextWindow:   6000,
		EstimateFactor:  1.3,
		Provider:        p,
		CompactProvider: p,
		CompactModel:    "test-small-model",
		State: &LoopState{
			Messages:                   largeConversation(8),
			LastPromptTokens:           1000,  // uncached input
			LastCachedPromptTokens:     20000, // cache reads
			LastPromptIncludesCache:    false, // Anthropic shape
			CompactionBudgetMultiplier: 1.0,
		},
	}
	cm.compactContext(context.Background(), 0.3)

	if len(p.snapshot()) == 0 {
		t.Fatal("compaction did not fire: raw gate used uncached prompt only")
	}
	if cm.State.PreviousSummary == "" {
		t.Fatal("compaction did not persist a summary")
	}
}

// waitCompactionDone drains the broker stream until a CompactionDoneEvent
// arrives (start events precede it), failing the test on timeout.
func waitCompactionDone(t *testing.T, done <-chan Event) *CompactionDoneEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-done:
			if e, ok := ev.(*CompactionDoneEvent); ok {
				return e
			}
		case <-deadline:
			t.Fatal("no CompactionDoneEvent within deadline")
			return nil
		}
	}
}

// TestCompactContextTrimFallbackEmitsDone checks the terminal compaction
// event on the trim fallback: when every summary attempt fails and
// trimContext is the remaining path, the emitted CompactionDoneEvent
// must carry the trim method, a real savings fraction, and the
// preserve budget — a zero SavingsPct alongside positive before/after
// deltas renders a self-contradictory "compacted 0%" card.
func TestCompactContextTrimFallbackEmitsDone(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	done := broker.Subscribe("done", 8)
	p := &compactionRecordingProvider{emptyAll: true}
	cm := compactionTestManager(p)
	cm.Broker = broker
	defer broker.Close()

	cm.compactContext(context.Background(), 0.25)

	e := waitCompactionDone(t, done)
	if e.Method != "trim" {
		t.Fatalf("method = %q, want trim", e.Method)
	}
	if e.SavingsPct <= 0 {
		t.Fatalf("savingsPct = %v, want positive after trim (before=%d after=%d)", e.SavingsPct, e.BeforeTokens, e.AfterTokens)
	}
	if e.Budget <= 0 {
		t.Fatalf("budget = %v, want the preserve budget carried through", e.Budget)
	}
	if e.IneffectiveNote == "" {
		t.Fatal("trim fallback must note why summarization was skipped")
	}
}

// TestCompactContextChunkedFallbackEmitsDone checks the terminal
// compaction event on the chunked path (same savings/budget rules as
// the trim fallback).
func TestCompactContextChunkedFallbackEmitsDone(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	done := broker.Subscribe("done", 8)
	// Empty primary summary forces the chunked fallback, whose merge
	// succeeds and produces a summary.
	p := &compactionRecordingProvider{responses: []*types.ChatResponse{thoughtOnlyResponse()}}
	cm := compactionTestManager(p)
	cm.Broker = broker
	defer broker.Close()

	cm.compactContext(context.Background(), 0.25)

	e := waitCompactionDone(t, done)
	if e.Method != "chunked" {
		t.Fatalf("method = %q, want chunked", e.Method)
	}
	if e.SavingsPct <= 0 {
		t.Fatalf("savingsPct = %v, want positive after chunked compaction", e.SavingsPct)
	}
	if e.Budget <= 0 {
		t.Fatalf("budget = %v, want the preserve budget carried through", e.Budget)
	}
}

// templateEchoResponse is the unfilled summary template — the recorded
// failure shape of the 2026-10-26 incident (yaah-v378.1): a valid
// non-empty content string that carries zero information.
func templateEchoResponse() *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role:    "assistant",
				Content: "## Goal\n- (none)\n\n## Progress\n### Done\n- (none)\n### In Progress\n- (none)\n\n## Key Decisions\n- (none)\n\n## Next Steps\n- (none)\n\n## Critical Context\n- (none)\n\n## Relevant Files\n- (none)\n",
			},
			FinishReason: "stop",
		}},
	}
}

// TestCompactContextRejectsTemplateEchoSummary pins yaah-v378.1: a summary
// whose every section is a placeholder must not replace the conversation.
// Installing it is what turned the 2026-10-26 session into a 173-minute
// re-orientation loop.
func TestCompactContextRejectsTemplateEchoSummary(t *testing.T) {
	p := &compactionRecordingProvider{responses: []*types.ChatResponse{templateEchoResponse()}}
	cm := compactionTestManager(p)

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	reqs := p.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected a chunked fallback attempt after the template-echo summary; got %d requests", len(reqs))
	}
	if strings.Contains(cm.State.PreviousSummary, "(none)") {
		t.Errorf("template echo was accepted as a summary: %q", cm.State.PreviousSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Errorf("expected heuristic trim after every summary attempt failed: before=%d after=%d", before, len(cm.State.Messages))
	}
	if cm.State.Messages[0].Role != "system" {
		t.Errorf("system message lost during fallback: role=%q", cm.State.Messages[0].Role)
	}
}

// TestCompactContextAcceptsSubstantiveSummary: the validator must not reject
// a real summary — one substantive bullet anywhere carries it.
func TestCompactContextAcceptsSubstantiveSummary(t *testing.T) {
	p := &compactionRecordingProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{
			Role:    "assistant",
			Content: "## Goal\n- (none)\n\n## Progress\n### In Progress\n- implementing the reasoning replay knob\n",
		}}}},
	}}
	cm := compactionTestManager(p)

	cm.compactContext(context.Background(), 0.25)

	if cm.State.PreviousSummary == "" {
		t.Fatal("a substantive summary must be applied, not rejected")
	}
	if !strings.Contains(cm.State.PreviousSummary, "reasoning replay knob") {
		t.Errorf("applied summary lost its substance: %q", cm.State.PreviousSummary)
	}
}

// TestCompactContextRejectsTemplateEchoFromChunkedSummarizer: validating only
// the primary response would leave the yaah-v378.1 shape reachable through
// the chunked path — every chunk/merge reply echoing the template must fall
// through to trim, never install.
func TestCompactContextRejectsTemplateEchoFromChunkedSummarizer(t *testing.T) {
	echoes := make([]*types.ChatResponse, 12)
	for i := range echoes {
		echoes[i] = templateEchoResponse()
	}
	p := &compactionRecordingProvider{responses: echoes}
	cm := compactionTestManager(p)

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	if strings.Contains(cm.State.PreviousSummary, "(none)") {
		t.Errorf("chunked template echo was installed: %q", cm.State.PreviousSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Errorf("expected heuristic trim after every summary source failed: before=%d after=%d", before, len(cm.State.Messages))
	}
	if cm.State.Messages[0].Role != "system" {
		t.Errorf("system message lost: role=%q", cm.State.Messages[0].Role)
	}
}
