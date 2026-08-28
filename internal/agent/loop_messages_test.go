package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func summarizerFake() *fakeProvider {
	return &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "SUMMARY of earlier conversation"},
				FinishReason: "stop",
			}},
		}},
	}
}

// TestLoop_overflowCompactionSurvivesToNextIteration covers review
// finding B6: overflow-recovery compaction triggered inside LLM.Call
// must be adopted by the loop so the next iteration builds on the
// compacted baseline, l.State.Messages reflects the compacted slice,
// and the persistence cursor is rebased to match.
//
// The pipeline's compaction middleware is disabled so the only
// compaction path exercised is the in-Call overflow recovery.
func TestLoop_overflowCompactionSurvivesToNextIteration(t *testing.T) {
	fp := &fakeProvider{
		maxFails: 1,
		failErr:  fmtErrorf("context length exceeded: maximum tokens 4096"),
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "success after compact"},
				FinishReason: "stop",
			}},
		}},
	}
	summarizer := summarizerFake()

	loop := &Loop{
		Config: LoopConfig{
			SystemPrompt:     "test prompt",
			MaxLoopCycles:    3,
			MaxRetries:       0, // auto-compact handles the overflow
			ContextWindow:    4000,
			CompactModel:     "test",
			PipelineDisabled: []string{"compaction"},
		},
		Provider:        fp,
		Registry:        tools.NewRegistry(),
		CompactProvider: summarizer,
	}

	// Pre-populate enough messages that compaction must trim them
	// (~100 tokens per message pair exceeds the 40% target of 4000).
	loop.State.Messages = []types.Message{types.SystemMsg("test prompt")}
	for i := 0; i < 20; i++ {
		loop.State.Messages = append(loop.State.Messages,
			types.UserMsg("message "+strings.Repeat("x", 400)),
			types.AssistantMsg("response "+strings.Repeat("y", 400), nil))
	}
	pre := len(loop.State.Messages)

	resp, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "success after compact" {
		t.Errorf("response = %q", resp)
	}
	if fp.failCount != 1 {
		t.Fatalf("expected exactly 1 overflow failure, got %d", fp.failCount)
	}
	if summarizer.index == 0 {
		t.Fatal("summarizer was never called; overflow compaction did not run")
	}

	after := loop.State.Messages
	if len(after) >= pre {
		t.Fatalf("compacted state did not survive: pre=%d after=%d", pre, len(after))
	}

	// The successful (second) LLM call must have received the compacted
	// conversation, not the pre-overflow slice.
	if len(fp.requests) == 0 {
		t.Fatal("expected a recorded successful request")
	}
	got := fp.requests[len(fp.requests)-1]
	if len(got.Messages) >= pre {
		t.Errorf("provider received un-compacted history: got=%d pre=%d", len(got.Messages), pre)
	}

	// Persistence cursor must track the adopted compacted baseline
	// (Rebase resets it to the post-compaction length). With a nil-DB
	// persister the assistant response is not persisted, so the cursor
	// may lag the final slice by exactly the appended response.
	if n := loop.Persister.MsgIdx(); n < len(after)-1 {
		t.Errorf("persister cursor = %d, want >= %d (compacted baseline)", n, len(after)-1)
	}

	// The compacted baseline must carry the summary, not the raw turns.
	if !strings.Contains(after[0].Content, "SUMMARY") {
		t.Errorf("compacted baseline missing summary header: %.80q", after[0].Content)
	}
}

// TestLoop_compactDoesNotMutateStateFromCall asserts the narrower B6
// invariant directly: the llm.Compactor entry points never write
// l.State.Messages — they return the compacted slice instead.
func TestLoop_compactDoesNotMutateStateFromCall(t *testing.T) {
	loop := &Loop{
		Config: LoopConfig{
			SystemPrompt:  "sys",
			ContextWindow: 1000,
		},
		Registry:        tools.NewRegistry(),
		CompactProvider: summarizerFake(),
	}
	loop.applyDefaults()

	loop.State.Messages = []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 30; i++ {
		loop.State.Messages = append(loop.State.Messages,
			types.UserMsg("message "+strings.Repeat("x", 120)),
			types.AssistantMsg("response "+strings.Repeat("y", 120), nil))
	}
	snapshot := make([]types.Message, len(loop.State.Messages))
	copy(snapshot, loop.State.Messages)

	compacted := loop.Compact(context.Background(), loop.State.Messages, 0.1)
	if len(compacted) >= len(snapshot) {
		t.Fatalf("expected compaction to shrink messages: got=%d", len(compacted))
	}
	if len(loop.State.Messages) != len(snapshot) {
		t.Errorf("Compact mutated l.State.Messages: before=%d after=%d",
			len(snapshot), len(loop.State.Messages))
	}
	for i := range snapshot {
		if loop.State.Messages[i].Content != snapshot[i].Content {
			t.Errorf("Compact corrupted l.State.Messages at %d", i)
			break
		}
	}
}
