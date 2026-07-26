package agent

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestPreflightTokens_emptyMessages(t *testing.T) {
	got := preflightTokens(nil, nil, defaultEstimateFactor)
	if got != 0 {
		t.Errorf("preflightTokens(nil, nil) = %d, want 0", got)
	}
}

func TestPreflightTokens_messagesOnly(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello world"),
	}
	got := preflightTokens(msgs, nil, defaultEstimateFactor)
	if got <= 0 {
		t.Errorf("preflightTokens with messages = %d, want > 0", got)
	}
}

func TestPreflightTokens_withTools(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
	}
	tools := []types.ToolDef{
		{
			Type: "function",
			Function: types.ToolFn{
				Name:        "read",
				Description: "Read a file from disk",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		},
	}
	withTools := preflightTokens(msgs, tools, defaultEstimateFactor)
	withoutTools := preflightTokens(msgs, nil, defaultEstimateFactor)
	if withTools <= withoutTools {
		t.Errorf("preflightTokens with tools (%d) should be > without tools (%d)", withTools, withoutTools)
	}
}

func TestPreflightTokens_1_3xMultiplier(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello world this is a test message with some content"),
	}
	raw := float64(messageTokens(msgs[0]))
	got := preflightTokens(msgs, nil, defaultEstimateFactor)
	expected := int(math.Ceil(raw * defaultEstimateFactor))
	if got != expected {
		t.Errorf("preflightTokens = %d, want %d (raw=%v * factor=%v)", got, expected, raw, defaultEstimateFactor)
	}
}

func TestPreflightTokens_customFactor(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
	}
	f1x := preflightTokens(msgs, nil, 1.0)
	f2x := preflightTokens(msgs, nil, 2.0)
	if f2x <= f1x {
		t.Errorf("2x factor (%d) should be > 1x factor (%d)", f2x, f1x)
	}
}

func TestPreflightTokens_toolEstimate(t *testing.T) {
	tools := []types.ToolDef{
		{
			Type: "function",
			Function: types.ToolFn{
				Name:        "grep",
				Description: "Search file contents using regular expressions",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}}}`),
			},
		},
	}
	got := preflightTokens(nil, tools, 1.0)
	if got <= 0 {
		t.Errorf("preflightTokens for tools = %d, want > 0", got)
	}
}

func TestCompactContext_continuationGuard(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   100,
		EstimateFactor:  1.3,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("", []types.ToolCall{{
		ID:   "c1",
		Type: "function",
		Function: types.ToolCallFn{
			Name:      "read",
			Arguments: `{"path":"/tmp"}`,
		},
	}}))
	msgs = append(msgs, types.Message{Role: "tool", ToolCallID: "c1", Name: "read", Content: "result"})
	loop.Messages = msgs

	loop.compactContext(context.Background(), 0.25)

	// splitTail is boundary-safe: compaction fires during continuation and
	// preserves tool-call linkage.
	if len(fp.requests) == 0 {
		t.Error("compact provider should be called even during continuation (splitTail is boundary-safe)")
	}
	// Verify tool linkage: every assistant tool_call id has a matching tool result.
	seen := make(map[string]bool)
	for _, m := range loop.Messages {
		if m.Role == "tool" {
			seen[m.ToolCallID] = true
		}
	}
	for _, m := range loop.Messages {
		for _, tc := range m.ToolCalls {
			if !seen[tc.ID] {
				t.Errorf("orphaned tool call %q after compaction during continuation", tc.ID)
			}
		}
	}
}

func TestCompactContext_preflightEstimate(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   10000,
		EstimateFactor:  1.3,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 400)))
	}
	msgs = append(msgs, types.AssistantMsg("final response", nil))
	loop.Messages = msgs

	if loop.LastPromptTokens != 0 {
		t.Fatalf("expected LastPromptTokens=0 for preflight test")
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) >= before {
		t.Errorf("compactContext should have compacted: before=%d, after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_actualTokensPreferred(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:         fp,
		CompactProvider:  fp,
		CompactModel:     "test",
		ContextWindow:    100000,
		EstimateFactor:   1.3,
		LastPromptTokens: 500,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("final", nil))
	loop.Messages = msgs

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) != before {
		t.Errorf("compactContext should NOT compact when LastPromptTokens (500) < target: before=%d, after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_customEstimateFactor(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("final", nil))

	factor1x := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   500,
		EstimateFactor:  1.0,
		Messages:        append([]types.Message{}, msgs...),
	}
	factor2x := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   500,
		EstimateFactor:  2.0,
		Messages:        append([]types.Message{}, msgs...),
	}

	est1x := preflightTokens(factor1x.Messages, nil, factor1x.EstimateFactor)
	est2x := preflightTokens(factor2x.Messages, nil, factor2x.EstimateFactor)

	if est2x <= est1x {
		t.Errorf("2x factor estimate (%d) should be > 1x factor estimate (%d)", est2x, est1x)
	}
}

// --- turns() ---

func TestTurns(t *testing.T) {
	tests := []struct {
		name     string
		messages []types.Message
		want     []turnRange
	}{
		{
			name:     "empty",
			messages: nil,
			want:     nil,
		},
		{
			name:     "system_only_ignored",
			messages: []types.Message{types.SystemMsg("sys")},
			want:     nil,
		},
		{
			name: "system_at_zero_ignored",
			messages: []types.Message{
				types.SystemMsg("sys"),
				types.UserMsg("hi"),
			},
			want: []turnRange{{start: 1, end: 2}},
		},
		{
			name: "multiple_users",
			messages: []types.Message{
				types.SystemMsg("sys"),
				types.UserMsg("a"),
				{Role: "assistant", Content: "b"},
				{Role: "tool", ToolCallID: "c1", Content: "r"},
				types.UserMsg("c"),
				{Role: "assistant", Content: "d"},
			},
			want: []turnRange{{start: 1, end: 4}, {start: 4, end: 6}},
		},
		{
			name: "tool_results_grouped_in_turn",
			messages: []types.Message{
				types.SystemMsg("sys"),
				types.UserMsg("a"),
				types.AssistantMsg("", []types.ToolCall{{
					ID: "c1", Type: "function",
					Function: types.ToolCallFn{Name: "read"},
				}}),
				{Role: "tool", ToolCallID: "c1", Content: "r1"},
				{Role: "tool", ToolCallID: "c2", Content: "r2"},
				types.UserMsg("b"),
			},
			want: []turnRange{{start: 1, end: 5}, {start: 5, end: 6}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := turns(tt.messages)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("turns() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// --- preserveBudget() ---

func TestPreserveBudget(t *testing.T) {
	tests := []struct {
		window int
		want   int
	}{
		{128000, 8000},  // 25% = 32000, clamped to max
		{4000, 2000},    // 25% = 1000, clamped to min
		{100, 2000},     // tiny window -> min floor
		{1000000, 8000}, // huge window -> max cap
		{24000, 6000},   // 25% = 6000, within range
		{16000, 4000},   // 25% = 4000, within range
		{0, 2000},       // zero window -> min floor
	}
	for _, tt := range tests {
		got := preserveBudget(tt.window)
		if got != tt.want {
			t.Errorf("preserveBudget(%d) = %d, want %d", tt.window, got, tt.want)
		}
	}
}

// --- splitTail() / splitTurn() ---

func bigUserMsg(label string) types.Message {
	// ~100 tokens (400 chars / 4)
	return types.UserMsg(label + strings.Repeat("x", 400))
}

func TestSplitTail_keepsRecentTurns(t *testing.T) {
	// system + 5 user turns, each ~20 tokens (80 chars). budget fits 3 turns.
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, types.UserMsg("u"+strings.Repeat("x", 76))) // ~19 tokens
	}
	split := splitTail(msgs, 60) // fits 3 turns
	// keepStart should land at the 3rd turn from the end: index 3.
	if split.keepStart != 3 {
		t.Errorf("keepStart = %d, want 3 (3 most recent turns preserved)", split.keepStart)
	}
}

func TestSplitTail_budgetExceeded(t *testing.T) {
	// system + 3 turns of (user + assistant), each ~100 tokens. budget fits ~1.5 turns.
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 3; i++ {
		msgs = append(msgs, bigUserMsg("u"))                                   // ~100 tokens
		msgs = append(msgs, types.AssistantMsg(strings.Repeat("y", 400), nil)) // ~100 tokens
	}
	split := splitTail(msgs, 150)
	if split.keepStart < 1 || split.keepStart >= len(msgs) {
		t.Errorf("keepStart = %d out of expected range [1, %d)", split.keepStart, len(msgs))
	}
	// The tail must be strictly smaller than the full conversation (something summarized).
	if split.keepStart <= 1 {
		t.Errorf("keepStart = %d, expected some turns summarized", split.keepStart)
	}
}

func TestSplitTail_toolCallLinkage(t *testing.T) {
	// Two turns. Turn 1 has a tool-call/result pair and is large; turn 2 is small.
	// Budget fits only turn 2, so the split lands at turn 2's user message — the
	// tool-call/result pair in turn 2 is never split.
	msgs := []types.Message{
		types.SystemMsg("sys"),
		bigUserMsg("u1"), // idx 1
		types.AssistantMsg(strings.Repeat("a", 400), []types.ToolCall{{ // idx 2
			ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read"},
		}}),
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("r", 400)}, // idx 3
		types.UserMsg("u2"), // idx 4
		types.AssistantMsg("", []types.ToolCall{{ // idx 5
			ID: "c2", Type: "function", Function: types.ToolCallFn{Name: "write"},
		}}),
		{Role: "tool", ToolCallID: "c2", Content: "ok"}, // idx 6
	}
	split := splitTail(msgs, 100) // turn 2 (~30 tokens) fits, turn 1 (~300) does not
	if split.keepStart != 4 {
		t.Fatalf("keepStart = %d, want 4 (turn 2 boundary)", split.keepStart)
	}
	// Verify the kept tail has the tool-call AND its result together (no orphan).
	tail := msgs[split.keepStart:]
	hasCall, hasResult := false, false
	for _, m := range tail {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c2" {
				hasCall = true
			}
		}
		if m.Role == "tool" && m.ToolCallID == "c2" {
			hasResult = true
		}
	}
	if !hasCall || !hasResult {
		t.Errorf("tool-call/result pair split: hasCall=%v hasResult=%v", hasCall, hasResult)
	}
}

func TestSplitTail_userAnchor(t *testing.T) {
	// The most recent user message must always be in the preserved tail when it
	// fits within the budget.
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, bigUserMsg("u"))
		msgs = append(msgs, types.AssistantMsg("ok", nil))
	}
	lastUserIdx := len(msgs) - 2   // last user before final assistant
	split := splitTail(msgs, 8000) // large budget keeps the last turn
	if split.keepStart > lastUserIdx {
		t.Errorf("keepStart = %d > lastUserIdx %d: most recent user summarized away",
			split.keepStart, lastUserIdx)
	}
}

func TestSplitTail_singleTurnTooLarge(t *testing.T) {
	// One turn (single user at idx 1) followed by large assistant messages.
	// splitTurn must find the earliest suffix that fits the budget.
	msgs := []types.Message{
		types.SystemMsg("sys"),                            // idx 0
		types.UserMsg("u"),                                // idx 1 (~10 tokens)
		types.AssistantMsg(strings.Repeat("a", 400), nil), // idx 2 (~100)
		types.AssistantMsg(strings.Repeat("b", 400), nil), // idx 3 (~100)
		types.AssistantMsg(strings.Repeat("c", 400), nil), // idx 4 (~100)
		types.AssistantMsg(strings.Repeat("d", 400), nil), // idx 5 (~100)
	}
	split := splitTail(msgs, 250) // fits last ~2.5 assistant messages
	if split.keepStart <= 1 {
		t.Errorf("keepStart = %d, expected split within the oversized turn", split.keepStart)
	}
}

func TestSplitTail_systemProtection(t *testing.T) {
	// Even with a budget of 0, keepStart must never drop below 1 (system prompt).
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		types.AssistantMsg("a1", nil),
		types.UserMsg("u2"),
	}
	split := splitTail(msgs, 0)
	if split.keepStart < 1 {
		t.Errorf("keepStart = %d, must be >= 1 (system protection)", split.keepStart)
	}
}

func TestSplitTail_noUserMessages(t *testing.T) {
	// No real user turns -> nothing to split (keepStart = len).
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.AssistantMsg("a1", nil),
		types.AssistantMsg("a2", nil),
	}
	split := splitTail(msgs, 1000)
	if split.keepStart != len(msgs) {
		t.Errorf("keepStart = %d, want %d (nothing to summarize)", split.keepStart, len(msgs))
	}
}

func TestSplitTurn_emptyOrSingleMessage(t *testing.T) {
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u"),
		types.AssistantMsg("a", nil),
	}
	if got := splitTurn(msgs, turnRange{start: 1, end: 1}, 100); got != -1 {
		t.Errorf("splitTurn(empty turn) = %d, want -1", got)
	}
	if got := splitTurn(msgs, turnRange{start: 1, end: 2}, 100); got != -1 {
		t.Errorf("splitTurn(single message turn) = %d, want -1", got)
	}
	if got := splitTurn(msgs, turnRange{start: 1, end: 3}, 0); got != -1 {
		t.Errorf("splitTurn(zero budget) = %d, want -1", got)
	}
}

func TestSplitTurn_findsEarliestFittingSuffix(t *testing.T) {
	// Turn spans idx 1..6 (5 messages). Budget fits the last 3 messages (idx 3,4,5).
	msgs := []types.Message{
		types.SystemMsg("sys"),                            // idx 0
		types.UserMsg("u"),                                // idx 1 (turn start)
		types.AssistantMsg(strings.Repeat("a", 400), nil), // idx 2 (~100)
		types.AssistantMsg(strings.Repeat("b", 400), nil), // idx 3 (~100)
		types.AssistantMsg(strings.Repeat("c", 400), nil), // idx 4 (~100)
		types.AssistantMsg(strings.Repeat("d", 400), nil), // idx 5 (~100)
	}
	// Budget 300 fits exactly messages[3..6) (300 tokens) but not messages[2..6) (400).
	got := splitTurn(msgs, turnRange{start: 1, end: 6}, 300)
	if got != 3 {
		t.Errorf("splitTurn = %d, want 3 (earliest fitting suffix)", got)
	}
}

// --- compactContext integration tests ---

// largeConversation builds a system + N user turns of ~400 chars each, ending
// with an assistant message so the conversation is not mid-tool-loop.
func largeConversation(n int) []types.Message {
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < n; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 400)))
	}
	msgs = append(msgs, types.AssistantMsg("final response", nil))
	return msgs
}

func TestCompactContext_structuredSummary(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "## Goal\n- do the thing"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   10000,
		EstimateFactor:  1.3,
		Messages:        largeConversation(30),
	}

	loop.compactContext(context.Background(), 0.25)

	if len(fp.requests) == 0 {
		t.Fatal("expected compact provider to be called")
	}
	prompt := fp.requests[0].Messages[0].Content
	for _, want := range []string{"## Goal", "## Progress", "## Key Decisions", "## Next Steps", "## Relevant Files"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("compact prompt missing structured section %q", want)
		}
	}
}

func TestCompactContext_budgetSplit(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	msgs := largeConversation(30)
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   10000, // preserveBudget = 2500
		EstimateFactor:  1.3,
		Messages:        msgs,
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	// Token-budgeted split must reduce the message count (not a fixed keepCount).
	if len(loop.Messages) >= before {
		t.Errorf("expected compaction to reduce messages: before=%d after=%d", before, len(loop.Messages))
	}
	// The preserved tail must be within the preserve budget (2500 tokens + overhead).
	// Allow generous slack for the summary system message and message flooring.
	tailTokens := 0
	for _, m := range loop.Messages {
		tailTokens += messageTokens(m)
	}
	if tailTokens > 4000 {
		t.Errorf("preserved tail too large: %d tokens (budget 2500)", tailTokens)
	}
}

func TestCompactContext_toolLinkagePreserved(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	// A conversation with several turns, the last two containing tool-call/result
	// pairs that must survive compaction intact.
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, types.UserMsg(strings.Repeat("x", 400)))
		msgs = append(msgs, types.AssistantMsg(strings.Repeat("y", 400), nil))
	}
	// Last two turns carry tool calls.
	msgs = append(msgs, types.UserMsg("run the tests"))
	msgs = append(msgs, types.AssistantMsg("", []types.ToolCall{{
		ID: "t1", Type: "function", Function: types.ToolCallFn{Name: "bash", Arguments: `{}`},
	}}))
	msgs = append(msgs, types.Message{Role: "tool", ToolCallID: "t1", Name: "bash", Content: "all passed"})

	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   10000,
		EstimateFactor:  1.3,
		Messages:        msgs,
	}

	loop.compactContext(context.Background(), 0.25)

	// Every assistant tool_call id in the compacted messages must have a
	// matching tool result message after it.
	seen := make(map[string]bool)
	for _, m := range loop.Messages {
		if m.Role == "tool" {
			seen[m.ToolCallID] = true
		}
	}
	for _, m := range loop.Messages {
		for _, tc := range m.ToolCalls {
			if !seen[tc.ID] {
				t.Errorf("orphaned tool call %q: no matching tool result after compaction", tc.ID)
			}
		}
	}
}

func TestCompactContext_recompaction(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "updated summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   10000,
		EstimateFactor:  1.3,
		PreviousSummary: "## Goal\n- prior goal",
		Messages:        largeConversation(30),
	}

	loop.compactContext(context.Background(), 0.25)

	if len(fp.requests) == 0 {
		t.Fatal("expected compact provider to be called")
	}
	prompt := fp.requests[0].Messages[0].Content
	for _, want := range []string{"Update the anchored summary", "<previous-summary>", "## Goal\n- prior goal"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("re-compaction prompt missing %q", want)
		}
	}
}

func TestCompactContext_cacheSubtraction(t *testing.T) {
	summary := func() *types.ChatResponse {
		return &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}
	}
	msgs := largeConversation(30)

	// Loop WITHOUT cache: LastPromptTokens=3000 > target(2500) -> compacts.
	noCache := &Loop{
		Provider:         &fakeProvider{responses: []*types.ChatResponse{summary()}},
		CompactModel:     "test",
		ContextWindow:    10000,
		Messages:         append([]types.Message{}, msgs...),
		LastPromptTokens: 3000,
	}
	// Loop WITH cache: effective = 3000-1500 = 1500 < target(2500) -> skips.
	withCache := &Loop{
		Provider:               &fakeProvider{responses: []*types.ChatResponse{summary()}},
		CompactModel:           "test",
		ContextWindow:          10000,
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       3000,
		LastCachedPromptTokens: 1500,
	}

	noCache.compactContext(context.Background(), 0.25)
	withCache.compactContext(context.Background(), 0.25)

	if len(noCache.Messages) >= len(msgs) {
		t.Errorf("uncached loop should have compacted: before=%d after=%d",
			len(msgs), len(noCache.Messages))
	}
	if len(withCache.Messages) != len(msgs) {
		t.Errorf("cached loop should NOT have compacted: before=%d after=%d",
			len(msgs), len(withCache.Messages))
	}
}

// summaryProvider returns a fakeProvider that responds to compaction with a
// fixed summary, used by the dual-trigger tests below.
func summaryProvider() *fakeProvider {
	return &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
}

func TestCompactContext_rawTriggerFires(t *testing.T) {
	// A heavily-cached conversation: effective tokens (10k) are well under the
	// cost target (64k floor), but raw tokens (60k) exceed the raw latency
	// target (50k = 0.5 * 100k). The raw trigger must fire compaction.
	msgs := largeConversation(30)
	loop := &Loop{
		Provider:               summaryProvider(),
		CompactModel:           "test",
		ContextWindow:          100000,
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       60000,
		LastCachedPromptTokens: 50000, // effective = 10000 < 64000 target
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) >= before {
		t.Errorf("raw trigger should have compacted: before=%d after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_bothTriggersUnderThreshold(t *testing.T) {
	// Both effective tokens (5k) and raw tokens (40k) are under their targets
	// (64k cost floor, 50k raw). Compaction must be skipped.
	msgs := largeConversation(30)
	loop := &Loop{
		Provider:               summaryProvider(),
		CompactModel:           "test",
		ContextWindow:          100000,
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       40000,
		LastCachedPromptTokens: 35000, // effective = 5000
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) != before {
		t.Errorf("neither trigger met; should NOT compact: before=%d after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_latchSelfResetsOnGrowth(t *testing.T) {
	// Two prior ineffective compactions latched the guard off, and the last
	// attempt left context at lastCompactionTokens. The context has since
	// grown well beyond 1.5x, so the latch must self-reset and compaction
	// must proceed despite ineffectiveCompactions >= 2. Without the
	// self-reset the guard returns early and compaction stays dead — the
	// catch-22 seen in long sessions (0 compaction spans across 30 traces).
	msgs := largeConversation(30) // ~3k estimated tokens
	loop := &Loop{
		Provider:               summaryProvider(),
		CompactModel:           "test",
		ContextWindow:          100000,
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       70000, // effective/raw well above targets
		LastCachedPromptTokens: 0,
		ineffectiveCompactions: 2,
		lastCompactionTokens:   1000, // grew from 1k -> ~3k (>= 1.5x) → self-reset
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) >= before {
		t.Errorf("latch should self-reset on context growth and compact: before=%d after=%d", before, len(loop.Messages))
	}
	if loop.ineffectiveCompactions >= 2 {
		t.Errorf("ineffectiveCompactions should have been cleared, still %d", loop.ineffectiveCompactions)
	}
}

func TestCompactContext_rawThresholdConfigurable(t *testing.T) {
	// Same raw token count (60k) with two different RawCompactionThreshold
	// values. Default (0.5 -> target 50k) fires; a high threshold (0.9 ->
	// target 90k) does not. This proves the field controls the raw trigger.
	msgs := largeConversation(30)

	defaultThreshold := &Loop{
		Provider:               summaryProvider(),
		CompactModel:           "test",
		ContextWindow:          100000,
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       60000,
		LastCachedPromptTokens: 55000, // effective = 5000 < 64000
	}
	highThreshold := &Loop{
		Provider:               summaryProvider(),
		CompactModel:           "test",
		ContextWindow:          100000,
		RawCompactionThreshold: 0.9, // rawTarget = 90000
		Messages:               append([]types.Message{}, msgs...),
		LastPromptTokens:       60000,
		LastCachedPromptTokens: 55000,
	}

	beforeDefault := len(defaultThreshold.Messages)
	beforeHigh := len(highThreshold.Messages)
	defaultThreshold.compactContext(context.Background(), 0.25)
	highThreshold.compactContext(context.Background(), 0.25)

	if len(defaultThreshold.Messages) >= beforeDefault {
		t.Errorf("default raw threshold (0.5) should fire: before=%d after=%d",
			beforeDefault, len(defaultThreshold.Messages))
	}
	if len(highThreshold.Messages) != beforeHigh {
		t.Errorf("high raw threshold (0.9) should NOT fire: before=%d after=%d",
			beforeHigh, len(highThreshold.Messages))
	}
}

// --- estimatePayloadBytes() / payload-size guard ---

func TestEstimatePayloadBytes_emptyIsZero(t *testing.T) {
	if got := estimatePayloadBytes(nil, nil); got != 0 {
		t.Errorf("estimatePayloadBytes(nil, nil) = %d, want 0", got)
	}
}

func TestEstimatePayloadBytes_accountsForAllFields(t *testing.T) {
	msgs := []types.Message{
		{
			Role:             "assistant",
			Content:          strings.Repeat("x", 100),
			ReasoningContent: strings.Repeat("x", 200),
			ToolCalls: []types.ToolCall{{
				ID:   "0123456789", // 10 chars
				Type: "function",
				Function: types.ToolCallFn{
					Name:      "read", // 4 chars
					Arguments: strings.Repeat("x", 50),
				},
			}},
		},
	}
	tools := []types.ToolDef{{
		Type: "function",
		Function: types.ToolFn{
			Name:        "read", // 4 chars
			Description: strings.Repeat("x", 30),
			Parameters:  json.RawMessage(strings.Repeat("x", 40)),
		},
	}}

	got := estimatePayloadBytes(msgs, tools)
	// message: 100 content + 200 reasoning + 50 args + 4 name + 10 id = 364
	// tools:   30 description + 40 parameters + 4 name = 74
	want := 364 + 74
	if got != want {
		t.Errorf("estimatePayloadBytes = %d, want %d", got, want)
	}
}

func TestPayloadGuard_oversizedPayloadCompacts(t *testing.T) {
	// Build a conversation whose serialized payload exceeds maxPayloadBytes,
	// then verify compaction (the guard's remediation) brings it back under.
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, types.UserMsg(strings.Repeat("x", 40000)))
		msgs = append(msgs, types.AssistantMsg(strings.Repeat("y", 1000), nil))
	}

	before := estimatePayloadBytes(msgs, nil)
	if before <= maxPayloadBytes {
		t.Fatalf("test setup: payload %d should exceed maxPayloadBytes %d", before, maxPayloadBytes)
	}

	loop := &Loop{
		Provider:       summaryProvider(),
		CompactModel:   "test",
		ContextWindow:  200000,
		EstimateFactor: 1.3,
		Messages:       msgs,
	}
	loop.compactContext(context.Background(), 0.25)

	after := estimatePayloadBytes(loop.Messages, nil)
	if after >= before {
		t.Errorf("compaction should reduce oversized payload: before=%d after=%d", before, after)
	}
	if after > maxPayloadBytes {
		t.Errorf("post-compaction payload %d still exceeds maxPayloadBytes %d", after, maxPayloadBytes)
	}
}

// --- prepareRequestMessages tests (merged from agent_reasoning_test.go) ---

func assistantWithReasoning(content, reasoning string) types.Message {
	return types.Message{Role: "assistant", Content: content, ReasoningContent: reasoning}
}

func TestPrepareRequestMessages_preservesReasoning(t *testing.T) {
	loop := &Loop{}
	loop.ensurePruner()

	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		assistantWithReasoning("a1", "reason-1"),
		types.UserMsg("u2"),
		assistantWithReasoning("a2", "reason-2"),
		types.UserMsg("u3"),
		assistantWithReasoning("a3", "reason-3"),
	}

	expected := map[string]string{}
	for _, m := range msgs {
		if m.Role == "assistant" {
			expected[m.Content] = m.ReasoningContent
		}
	}

	out := loop.prepareRequestMessages(msgs)

	for _, m := range out {
		if m.Role == "assistant" {
			want := expected[m.Content]
			if m.ReasoningContent != want {
				t.Errorf("assistant %q: reasoning = %q, want %q", m.Content, m.ReasoningContent, want)
			}
			delete(expected, m.Content)
		}
	}
	if len(expected) > 0 {
		t.Errorf("missing assistant messages in output: %v", contentKeys(expected))
	}
}

func TestPrepareRequestMessages_doesNotMutateInput(t *testing.T) {
	loop := &Loop{}
	loop.ensurePruner()

	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		assistantWithReasoning("a1", "reason-1"),
	}

	_ = loop.prepareRequestMessages(msgs)

	if msgs[2].ReasoningContent != "reason-1" {
		t.Errorf("stored history mutated: idx 2 reasoning = %q, want %q", msgs[2].ReasoningContent, "reason-1")
	}
}

func TestPrepareRequestMessages_toolLinkageUntouched(t *testing.T) {
	loop := &Loop{}
	loop.ensurePruner()

	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		types.Message{
			Role:             "assistant",
			ReasoningContent: "reason",
			ToolCalls: []types.ToolCall{{
				ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read"},
			}},
		},
		{Role: "tool", ToolCallID: "c1", Name: "read", Content: "result"},
		types.UserMsg("u2"),
	}

	out := loop.prepareRequestMessages(msgs)

	if len(out) != len(msgs) {
		t.Fatalf("message count changed: before=%d after=%d", len(msgs), len(out))
	}
	if out[2].ReasoningContent != "reason" {
		t.Errorf("reasoning should be preserved, got %q", out[2].ReasoningContent)
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "c1" {
		t.Errorf("tool call lost: %#v", out[2].ToolCalls)
	}
	if out[3].Role != "tool" || out[3].ToolCallID != "c1" {
		t.Errorf("tool result lost: %#v", out[3])
	}
}

func TestMessageTokens_countsReasoning(t *testing.T) {
	withoutReasoning := types.Message{Role: "assistant", Content: ""}
	withReasoning := types.Message{Role: "assistant", Content: "", ReasoningContent: repeatChars(400)}

	base := messageTokens(withoutReasoning)
	withR := messageTokens(withReasoning)

	if withR <= base {
		t.Errorf("messageTokens should count reasoning: without=%d with=%d", base, withR)
	}
	if withR != 100 {
		t.Errorf("messageTokens with 400 chars reasoning = %d, want 100", withR)
	}
}

func contentKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func repeatChars(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
