package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
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
	// placeholderAll makes every reply echo the unfilled summary
	// template — the recorded orch-compact shape (yaah-l12q), where the
	// compaction model returned placeholder-only content on every call.
	// The queued responses cannot express it: a request count that
	// outlives the queue silently falls through to the substantive
	// default, which is what let a chunked merge "succeed" in a test
	// that claimed every reply echoed the template.
	placeholderAll bool
}

func (p *compactionRecordingProvider) Send(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)

	if p.emptyAll {
		return thoughtOnlyResponse(), nil
	}
	if p.placeholderAll {
		return templateEchoResponse(), nil
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
	p := &compactionRecordingProvider{placeholderAll: true}
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

// TestCompactContextChunkedSummaryPersists pins yaah-l12q's recorded
// failure: the chunked fallback installed a real summary in the context
// but recorded nothing, so the session's compacted_summary row stayed
// empty — the shape the orch-compact verifier caught when the compaction
// model returned placeholder-only summaries and every rollover rode the
// fallback. A restart would have resumed without the summary too.
func TestCompactContextChunkedSummaryPersists(t *testing.T) {
	// The primary reply echoes the template (rejected); the chunk and
	// merge replies fall through to the recorder's substantive default.
	p := &compactionRecordingProvider{responses: []*types.ChatResponse{templateEchoResponse()}}
	db := newPersistTestDB(t, "sess-chunked-persist")
	cm := compactionTestManager(p)
	cm.DB = db
	cm.SessionID = "sess-chunked-persist"

	cm.compactContext(context.Background(), 0.25)

	if cm.State.LastCompactionMethod != "chunked" {
		t.Fatalf("method = %q, want chunked — the test presumes the fallback accepted a summary", cm.State.LastCompactionMethod)
	}
	session, err := db.GetSession("sess-chunked-persist")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(session.CompactedSummary) == "" {
		t.Fatal("the chunked summary was not persisted — resume and the scenario checkers see an empty rollover (yaah-l12q)")
	}
}

// TestCompactContextTrimFallbackPersistsMarker pins the terminal
// fallback's record (yaah-l12q): when every summarizer attempt fails, the
// trim path drops the old messages without a summary — and an empty
// compacted_summary row would claim no rollover happened. The row carries
// the fallback marker; the in-memory summary stays empty because trim
// installs nothing.
func TestCompactContextTrimFallbackPersistsMarker(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-trim-marker")
	cm := compactionTestManager(p)
	cm.DB = db
	cm.SessionID = "sess-trim-marker"

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	if cm.State.LastCompactionMethod != "trim" {
		t.Fatalf("method = %q, want trim — the test presumes every summary source failed", cm.State.LastCompactionMethod)
	}
	// The in-memory summary contract is its own check: trim installs no
	// summary, so a future change writing the marker there must fail
	// here regardless of what the row holds.
	if cm.State.LastCompactionSummary != "" {
		t.Fatalf("trim installed an in-memory summary: %q", cm.State.LastCompactionSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Fatal("expected the trim fallback to drop messages")
	}
	session, err := db.GetSession("sess-trim-marker")
	if err != nil {
		t.Fatal(err)
	}
	marker := session.CompactedSummary
	if strings.TrimSpace(marker) == "" {
		t.Fatal("the trim fallback left no record — the rollover is invisible to resume and the scenario checkers (yaah-l12q)")
	}
	if !strings.Contains(marker, "[context rollover]") {
		t.Errorf("marker does not identify itself as a fallback: %q", marker)
	}
	if !strings.Contains(marker, "heuristic trim") {
		t.Errorf("marker does not name the fallback: %q", marker)
	}
}

// TestCompactContextCountTriggerResolvesTheCount pins yaah-l12q's
// residual: the message-count trigger fires with the conversation
// inside trimContext's 80% window, so the token trim is a no-op — and
// without a count-targeted pass the rollover dropped nothing, recorded
// nothing, and left the count above compact_max_messages, rearming
// the trigger on every subsequent message (the orch-compact shape:
// ten rollovers, zero persisted). The budget pass resolves what fired:
// messages drop under the budget, the fallback marker records the
// rollover truthfully, and the trigger does not rearm on the next
// message.
func TestCompactContextCountTriggerResolvesTheCount(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-count-trim")
	cm := &ContextManager{
		ContextWindow:      6000,
		EstimateFactor:     1.3,
		Provider:           p,
		CompactProvider:    p,
		CompactModel:       "test-small-model",
		CompactMaxMessages: 10,
		DB:                 db,
		SessionID:          "sess-count-trim",
		State: &LoopState{
			// Below both token gates, so the message-count trigger is
			// what fires — the shape this test needs.
			LastPromptTokens:           100,
			CompactionBudgetMultiplier: 1.0,
		},
	}
	cm.State.Messages = append(cm.State.Messages, types.SystemMsg("sys"))
	// ~500 chars each: the whole conversation fits trim's 80% window
	// (4800 tokens) but exceeds the 2000-token preserve budget, so the
	// split leaves old messages for the (failing) summarizer.
	for range 32 {
		cm.State.Messages = append(cm.State.Messages, types.UserMsg(strings.Repeat("x", 500)))
	}

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	if cm.State.LastCompactionMethod != "trim" {
		t.Fatalf("method = %q, want trim — the test presumes every summary source failed", cm.State.LastCompactionMethod)
	}
	if cm.State.LastCompactionSummary != "" {
		t.Fatalf("trim installed an in-memory summary: %q", cm.State.LastCompactionSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Fatalf("the count-triggered rollover dropped nothing (%d messages) — the trigger rearms on the next message", len(cm.State.Messages))
	}
	if len(cm.State.Messages) > 10 {
		t.Fatalf("messages = %d, want within the compact_max_messages budget (10) so the count trigger stays disarmed", len(cm.State.Messages))
	}
	if cm.State.Messages[0].Role != "system" {
		t.Errorf("system message lost: role=%q", cm.State.Messages[0].Role)
	}
	session, err := db.GetSession("sess-count-trim")
	if err != nil {
		t.Fatal(err)
	}
	marker := session.CompactedSummary
	if strings.TrimSpace(marker) == "" {
		t.Fatal("the count-triggered trim left no record — the rollover is invisible to resume and the scenario checkers (yaah-l12q)")
	}
	if !strings.Contains(marker, "[context rollover]") || !strings.Contains(marker, "heuristic trim") {
		t.Errorf("marker does not identify itself as the trim fallback: %q", marker)
	}
}

// TestCompactContextProtectedCountTrimRecordsNothing pins the boundary
// that survives the count fix: when reasoning protection keeps every
// candidate, the budget pass cuts nothing — and a marker there would
// tell resume that context was lost when none was. The no-record rule
// is tied to messages actually dropping, not to the trigger that fired.
func TestCompactContextProtectedCountTrimRecordsNothing(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-count-protected")
	cm := &ContextManager{
		ContextWindow:         6000,
		EstimateFactor:        1.3,
		Provider:              p,
		CompactProvider:       p,
		CompactModel:          "test-small-model",
		CompactMaxMessages:    10,
		ReasoningProtectTurns: 32,
		DB:                    db,
		SessionID:             "sess-count-protected",
		State: &LoopState{
			LastPromptTokens:           100,
			CompactionBudgetMultiplier: 1.0,
		},
	}
	cm.State.Messages = append(cm.State.Messages, types.SystemMsg("sys"))
	// The first conversation message is a reasoning-carrying assistant
	// turn: ProtectReasoningTurns anchors to it and keeps everything
	// after, so the budget pass has no candidate it may cut.
	first := types.Message{Role: "assistant", Content: strings.Repeat("r", 500), ReasoningContent: "keep everything from here"}
	cm.State.Messages = append(cm.State.Messages, first)
	for range 31 {
		cm.State.Messages = append(cm.State.Messages, types.UserMsg(strings.Repeat("x", 500)))
	}

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	if len(cm.State.Messages) != before {
		t.Fatalf("messages dropped (%d -> %d): the fixture presumes protection keeps every candidate", before, len(cm.State.Messages))
	}
	session, err := db.GetSession("sess-count-protected")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(session.CompactedSummary); got != "" {
		t.Fatalf("a no-op trim recorded %q — no rollover happened, so resume must not be told context was lost", got)
	}
}

// TestCompactContextExcerptCarriesAnchoredSummaryOnce pins yaah-an55's
// double-feed: the previous summary rides the prompt as its own
// <previous-summary> section, so the continuation notice that carries the
// same text in the transcript must not also be summarized as excerpt
// content. Live compactions and resumed sessions share this prompt.
func TestCompactContextExcerptCarriesAnchoredSummaryOnce(t *testing.T) {
	const summary = "## Goal\n- keep the anchored summary single-copy"
	p := &compactionRecordingProvider{}
	cm := compactionTestManager(p)
	// Insert the notice where compaction installs it — right after the
	// system prompt — and make it the anchored summary's carrier.
	carrier := types.SystemMsg(agentctx.ContinuationNotice(summary))
	cm.State.Messages = append([]types.Message{cm.State.Messages[0], carrier}, cm.State.Messages[1:]...)
	cm.State.PreviousSummary = summary

	cm.compactContext(context.Background(), 0.25)

	reqs := p.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no compaction request was made")
	}
	prompt := reqs[0].Messages[0].Content
	if !strings.Contains(prompt, "<previous-summary>") {
		t.Fatalf("the summarization prompt lost the anchored-summary section:\n%s", prompt)
	}
	if got := strings.Count(prompt, summary); got != 1 {
		t.Fatalf("the anchored summary appears %d time(s) in the summarization prompt, want 1 (its own section, not also excerpted)", got)
	}
}

// TestCompactContextAllReasoningConversationTrims pins yaah-6a6c: when the
// provider reasons on every turn (deepseek-flash does), the protection
// window must not make compaction a no-op. The fallback trim has to drop
// history — old reasoning turns included — while every kept assistant
// message keeps its reasoning (the thinking-mode contract applies to what a
// request sends, not to what it no longer sends), and the fallback marker
// records the rollover the scenario verifier looks for.
func TestCompactContextAllReasoningConversationTrims(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-reasoning-trim")
	cm := compactionTestManager(p)
	cm.DB = db
	cm.SessionID = "sess-reasoning-trim"
	cm.ReasoningProtectTurns = 2
	cm.State.Messages = reasoningConversation(24)

	before := len(cm.State.Messages)
	reasoningBefore := agentctx.CountReasoningMessages(cm.State.Messages)
	cm.compactContext(context.Background(), 0.25)

	if cm.State.LastCompactionMethod != "trim" {
		t.Fatalf("method = %q, want trim — the test presumes every summarizer attempt failed", cm.State.LastCompactionMethod)
	}
	if len(cm.State.Messages) >= before {
		t.Fatalf("compaction dropped nothing (%d -> %d): an all-reasoning conversation must still compact", before, len(cm.State.Messages))
	}
	if got := agentctx.CountReasoningMessages(cm.State.Messages); got >= reasoningBefore {
		t.Fatalf("old reasoning turns were not droppable (%d -> %d): the window is %d turns, not all history",
			reasoningBefore, got, cm.ReasoningProtectTurns)
	}
	for _, m := range cm.State.Messages {
		if m.Role == "assistant" && m.ReasoningContent == "" {
			t.Fatalf("a kept assistant message lost its reasoning: %+v", m)
		}
	}
	session, err := db.GetSession("sess-reasoning-trim")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(session.CompactedSummary) == "" {
		t.Fatal("the trim rollover left no record — the scenario checkers see an empty rollover")
	}
}

// reasoningConversation builds the real shape a thinking-mode provider
// produces: one user prompt, then assistant(reasoning, tool call) / tool-result
// cycles. The single user message is what made the original protection rewind
// to the start of the conversation (yaah-6a6c), so the fixture must carry it.
func reasoningConversation(turns int) []types.Message {
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("read every file in the data directory"),
	}
	for i := range turns {
		id := fmt.Sprintf("call-%d", i)
		a := types.AssistantMsg(strings.Repeat("work ", 40), []types.ToolCall{{
			ID:       id,
			Type:     "function",
			Function: types.ToolCallFn{Name: "read", Arguments: `{"filePath":"data/f"}`},
		}})
		a.ReasoningContent = strings.Repeat("thinking ", 80)
		msgs = append(msgs, a, types.ToolResultMsg(id, "read", strings.Repeat("data ", 200)))
	}
	return msgs
}

// TestCompactContextAcceptedSummaryPersists guards the shared persistence
// tail: the single-shot path's accepted summary must reach the session
// row (pre-existing behavior, now through the same helper the chunked
// path gained in yaah-l12q).
func TestCompactContextAcceptedSummaryPersists(t *testing.T) {
	p := &compactionRecordingProvider{}
	db := newPersistTestDB(t, "sess-single-persist")
	cm := compactionTestManager(p)
	cm.DB = db
	cm.SessionID = "sess-single-persist"

	cm.compactContext(context.Background(), 0.25)

	if cm.State.LastCompactionMethod != "single" {
		t.Fatalf("method = %q, want single", cm.State.LastCompactionMethod)
	}
	session, err := db.GetSession("sess-single-persist")
	if err != nil {
		t.Fatal(err)
	}
	if session.CompactedSummary != "summary" {
		t.Fatalf("compacted_summary = %q, want the accepted summary", session.CompactedSummary)
	}
}

// TestCompactContextThresholdTrimResolvesTarget pins the threshold
// trigger's half of yaah-l12q's residual: the fallback's token trim
// aimed at trimContext's 80% window, which sits far above the
// compaction target — so a threshold-triggered rollover over an
// unreachable summarizer trimmed nothing (in-window), recorded
// nothing, and rearmed the trigger on every subsequent message. The
// fallback now trims to the target the trigger itself measures
// against, disarms it, and the marker records the rollover.
func TestCompactContextThresholdTrimResolvesTarget(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-threshold-trim")
	cm := &ContextManager{
		ContextWindow:   6000,
		EstimateFactor:  1.3,
		Provider:        p,
		CompactProvider: p,
		CompactModel:    "test-small-model",
		DB:              db,
		SessionID:       "sess-threshold-trim",
		State: &LoopState{
			// Over the 0.3 target (1800), under the overflow line —
			// the threshold trigger is what fires.
			LastPromptTokens:           3000,
			CompactionBudgetMultiplier: 1.0,
		},
	}
	cm.State.Messages = append(cm.State.Messages, types.SystemMsg("sys"))
	for range 40 {
		cm.State.Messages = append(cm.State.Messages, types.UserMsg(strings.Repeat("x", 500)))
	}

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.3)

	if cm.State.LastCompactionMethod != "trim" {
		t.Fatalf("method = %q, want trim — the test presumes every summary source failed", cm.State.LastCompactionMethod)
	}
	if cm.State.LastCompactionSummary != "" {
		t.Fatalf("trim installed an in-memory summary: %q", cm.State.LastCompactionSummary)
	}
	if len(cm.State.Messages) >= before {
		t.Fatalf("the threshold-triggered rollover dropped nothing (%d messages) — the trigger rearms on the next message", len(cm.State.Messages))
	}
	chars := 0
	for _, m := range cm.State.Messages {
		chars += len(m.Content) + len(m.ReasoningContent)
	}
	if chars/4 > 1800 {
		t.Errorf("post-trim estimate %d > the 1800-token target — the threshold trigger is still armed", chars/4)
	}
	session, err := db.GetSession("sess-threshold-trim")
	if err != nil {
		t.Fatal(err)
	}
	marker := session.CompactedSummary
	if strings.TrimSpace(marker) == "" {
		t.Fatal("the threshold-triggered trim left no record — the rollover is invisible to resume and the scenario checkers (yaah-l12q)")
	}
	if !strings.Contains(marker, "[context rollover]") || !strings.Contains(marker, "heuristic trim") {
		t.Errorf("marker does not identify itself as the trim fallback: %q", marker)
	}
}

// TestCompactContextReasoningHeadTrimPreservesLinkageAndTask pins the
// invariants yaah-6a6c's regression test (AllReasoningConversationTrims)
// does not: on a single giant agentic turn whose reasoning carriers sit
// near the head, the fallback trim must land on a linkage-safe boundary
// (no tool result may outlive its assistant caller), must stay inside the
// reasoning-protection window, and must re-inject the task instruction
// when its user prompt is dropped — the live orch-compact run answered
// instead of writing the report because the re-inject carried the
// [STEER] churn notice rather than the task.
func TestCompactContextReasoningHeadTrimPreservesLinkageAndTask(t *testing.T) {
	p := &compactionRecordingProvider{placeholderAll: true}
	db := newPersistTestDB(t, "sess-reasoning-head")
	cm := &ContextManager{
		ContextWindow:         6000,
		EstimateFactor:        1.3,
		Provider:              p,
		CompactProvider:       p,
		CompactModel:          "test-small-model",
		ReasoningProtectTurns: 2,
		DB:                    db,
		SessionID:             "sess-reasoning-head",
		State: &LoopState{
			LastPromptTokens:           3000, // over the 0.3 target: threshold trigger
			CompactionBudgetMultiplier: 1.0,
		},
	}
	// One agentic turn: a big system prompt, the task's only user
	// message, then three read exchanges with reasoning on every
	// assistant — the first one at the head of the conversation.
	cm.State.Messages = append(cm.State.Messages, types.SystemMsg(strings.Repeat("s", 16000)))
	cm.State.Messages = append(cm.State.Messages, types.UserMsg("read every file in /app/data and write the report"))
	for batch := 0; batch < 3; batch++ {
		calls := []types.ToolCall{}
		for i := 0; i < 6; i++ {
			calls = append(calls, types.ToolCall{
				ID:   fmt.Sprintf("batch%d-call%d", batch, i),
				Type: "function",
				Function: types.ToolCallFn{
					Name:      "read",
					Arguments: fmt.Sprintf(`{"path":"/app/data/f%d.txt"}`, batch*6+i),
				},
			})
		}
		cm.State.Messages = append(cm.State.Messages, types.Message{
			Role:             "assistant",
			Content:          fmt.Sprintf("reading batch %d", batch),
			ReasoningContent: strings.Repeat("think ", 150),
			ToolCalls:        calls,
		})
		for i := 0; i < 6; i++ {
			cm.State.Messages = append(cm.State.Messages, types.Message{
				Role:       "tool",
				ToolCallID: fmt.Sprintf("batch%d-call%d", batch, i),
				Name:       "read",
				Content:    strings.Repeat("t", 2200),
			})
		}
	}

	// A loop-detection steer lands mid-loop — synthetic guidance, not
	// the task. The trim re-inject must capture the task, not this.
	cm.State.Messages = append(cm.State.Messages, types.UserMsg(
		"[STEER] You have called the read tool heavily for 3 consecutive turns. Consider a different approach."))

	before := len(cm.State.Messages)
	cm.compactContext(context.Background(), 0.3)

	if got := len(p.snapshot()); got == 0 {
		t.Error("the summarizer was never attempted: with the windowed split the excerpt carries real content (the rejection falls to the trim)")
	}
	if len(cm.State.Messages) >= before {
		t.Fatalf("trim dropped nothing (%d messages) — the protection collapse rearmed the trigger and recorded no rollover", len(cm.State.Messages))
	}
	// Bounded protection: the newest ReasoningProtectTurns carriers
	// survive; the head carrier is releasable.
	carriers := 0
	for _, m := range cm.State.Messages {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			carriers++
		}
	}
	if carriers > 2 {
		t.Errorf("kept %d reasoning carriers, want at most the newest %d: the earliest carriers must be trimmable or single-turn sessions never shrink", carriers, 2)
	}
	// Linkage: every kept tool result still has its assistant caller kept.
	callers := map[string]bool{}
	for _, m := range cm.State.Messages {
		for _, c := range m.ToolCalls {
			callers[c.ID] = true
		}
	}
	for _, m := range cm.State.Messages {
		if m.Role == "tool" && !callers[m.ToolCallID] {
			t.Errorf("trim orphaned tool result %q: its assistant caller was dropped (hard 400 on every OpenAI-compatible provider)", m.ToolCallID)
		}
	}
	// The task instruction must survive as the re-injected active task
	// when its user prompt was dropped.
	found := false
	for _, m := range cm.State.Messages {
		if strings.Contains(m.Content, "Active task (preserve verbatim):") {
			if !strings.Contains(m.Content, "/app/data") {
				t.Errorf("re-inject carried synthetic traffic instead of the task: %q", m.Content)
			}
			if strings.Contains(m.Content, "[STEER]") {
				t.Error("re-inject captured the steer notice — the live verify6 shape that never wrote the report")
			}
			found = true
		}
	}
	if !found {
		t.Error("the dropped task instruction was not re-injected: the agent resumes with no record of the deliverable")
	}
	session, err := db.GetSession("sess-reasoning-head")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(session.CompactedSummary); got == "" {
		t.Fatal("the rollover persisted nothing — the orch-compact session-summary check reads exactly this")
	}
}
