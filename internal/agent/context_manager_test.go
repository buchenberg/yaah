package agent

import (
	"context"
	"sync"
	"testing"

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
