package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// fakeCheckpointer is an in-memory TurnCheckpointer for loop tests.
// Checkpoints are single-use, mirroring the shepherd git checkpoints.
type fakeCheckpointer struct {
	mu            sync.Mutex
	snapshots     map[string][]byte
	restored      []string
	checkpoints   int
	prunes        int
	seq           int
	checkpointErr error
	restoreErr    error
}

func newFakeCheckpointer() *fakeCheckpointer {
	return &fakeCheckpointer{snapshots: make(map[string][]byte)}
}

func (f *fakeCheckpointer) Checkpoint(_ context.Context, snapshot []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkpointErr != nil {
		return "", f.checkpointErr
	}
	f.seq++
	id := fmt.Sprintf("cp-%d", f.seq)
	cp := make([]byte, len(snapshot))
	copy(cp, snapshot)
	f.snapshots[id] = cp
	f.checkpoints++
	return id, nil
}

func (f *fakeCheckpointer) Restore(_ context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	snap, ok := f.snapshots[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %s not found or already consumed", id)
	}
	delete(f.snapshots, id)
	f.restored = append(f.restored, id)
	return snap, nil
}

func (f *fakeCheckpointer) Prune(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes++
	f.snapshots = make(map[string][]byte)
	return nil
}

func (f *fakeCheckpointer) checkpointCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpoints
}

func (f *fakeCheckpointer) restoredIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.restored))
	copy(out, f.restored)
	return out
}

func turnCkToolResponse(id, name, args string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:       id,
					Type:     "function",
					Function: types.ToolCallFn{Name: name, Arguments: args},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}
}

func turnCkTextResponse(content string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}
}

func TestLoop_TurnCheckpoint_OnePerTurn(t *testing.T) {
	ckpt := newFakeCheckpointer()
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkToolResponse("tc1", "noop", `{}`),
		turnCkTextResponse("done"),
	}}

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok"})

	loop := &Loop{
		Provider: fp,
		Registry: reg,
		Config: LoopConfig{
			SystemPrompt:          "sp",
			MaxLoopCycles:         10,
			TurnCheckpointer:      ckpt,
			TurnCheckpointEnabled: true,
		},
	}

	resp, err := loop.Run(context.Background(), "work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp != "done" {
		t.Errorf("response = %q, want done", resp)
	}
	if got := ckpt.checkpointCount(); got != 2 {
		t.Errorf("checkpoints = %d, want 2 (one per model turn)", got)
	}
	if restored := ckpt.restoredIDs(); len(restored) != 0 {
		t.Errorf("restored = %v, want none on a clean run", restored)
	}
	if loop.State.TurnRestores != 0 {
		t.Errorf("TurnRestores = %d, want 0", loop.State.TurnRestores)
	}
	if loop.State.TurnCheckpoints != nil {
		t.Errorf("TurnCheckpoints should be cleared at Run end, got %v", loop.State.TurnCheckpoints)
	}
}

func TestLoop_TurnCheckpoint_NotCalledWhenDisabled(t *testing.T) {
	ckpt := newFakeCheckpointer()

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok"})

	for name, cfg := range map[string]LoopConfig{
		"disabled": {SystemPrompt: "sp", MaxLoopCycles: 10, TurnCheckpointer: ckpt, TurnCheckpointEnabled: false},
		"nil":      {SystemPrompt: "sp", MaxLoopCycles: 10, TurnCheckpointer: nil, TurnCheckpointEnabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			// A fresh provider per subtest so each one exercises the
			// scripted tool turn regardless of map iteration order.
			fp := &fakeProvider{responses: []*types.ChatResponse{
				turnCkToolResponse("tc1", "noop", `{}`),
				turnCkTextResponse("done"),
			}}
			loop := &Loop{Provider: fp, Registry: reg, Config: cfg}
			if _, err := loop.Run(context.Background(), "work"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(fp.requests) != 2 {
				t.Fatalf("requests = %d, want 2 (tool turn + final turn)", len(fp.requests))
			}
			if got := ckpt.checkpointCount(); got != 0 {
				t.Errorf("checkpoints = %d, want 0 when checkpointing is inactive", got)
			}
		})
	}
}

func TestLoop_TurnRestore_OnToolPhaseError(t *testing.T) {
	ckpt := newFakeCheckpointer()
	// Two identical tool calls trip loop detection (count=2, window=2) on
	// the second one, producing a hard executeToolPhase error.
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkToolResponse("tc1", "noop", `{"x":1}`),
		turnCkToolResponse("tc2", "noop", `{"x":1}`),
		turnCkTextResponse("done"),
	}}

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok"})

	loop := &Loop{
		Provider: fp,
		Registry: reg,
		Config: LoopConfig{
			SystemPrompt:          "sp",
			MaxLoopCycles:         10,
			LoopDetectCount:       2,
			LoopDetectWindow:      2,
			TurnCheckpointer:      ckpt,
			TurnCheckpointEnabled: true,
		},
	}

	resp, err := loop.Run(context.Background(), "work")
	if err != nil {
		t.Fatalf("Run should recover via restore, got error: %v", err)
	}
	if resp != "done" {
		t.Errorf("response = %q, want done", resp)
	}

	// Turns: turn0 (ok), turn1 (loop-detect failure, restored), turn2 (final).
	if got := ckpt.checkpointCount(); got != 3 {
		t.Errorf("checkpoints = %d, want 3", got)
	}
	restored := ckpt.restoredIDs()
	if len(restored) != 1 || restored[0] != "cp-2" {
		t.Errorf("restored = %v, want [cp-2] (the pre-failure checkpoint)", restored)
	}
	if loop.State.TurnRestores != 1 {
		t.Errorf("TurnRestores = %d, want 1", loop.State.TurnRestores)
	}
	if loop.State.RestoredFrom != "cp-2" {
		t.Errorf("RestoredFrom = %q, want cp-2", loop.State.RestoredFrom)
	}

	// The rewound conversation carries the guidance message.
	var hasGuidance bool
	for _, m := range loop.State.Messages {
		if strings.Contains(m.Content, "failed while executing tools") {
			hasGuidance = true
		}
	}
	if !hasGuidance {
		t.Error("post-restore messages should contain the supervisor guidance")
	}
}

func TestLoop_TurnRestore_ExhaustionRetries(t *testing.T) {
	ckpt := newFakeCheckpointer()
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkToolResponse("tc1", "noop", `{}`),
		turnCkTextResponse("done"),
	}}

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok"})

	loop := &Loop{
		Provider: fp,
		Registry: reg,
		Config: LoopConfig{
			SystemPrompt:          "sp",
			MaxLoopCycles:         1, // exhaust after one turn
			MaxTurnRestores:       1,
			TurnCheckpointer:      ckpt,
			TurnCheckpointEnabled: true,
		},
	}

	resp, err := loop.Run(context.Background(), "work")
	if err != nil {
		t.Fatalf("Run should recover from exhaustion via restore, got error: %v", err)
	}
	if resp != "done" {
		t.Errorf("response = %q, want done", resp)
	}
	if got := ckpt.checkpointCount(); got != 2 {
		t.Errorf("checkpoints = %d, want 2 (first pass + retry)", got)
	}
	if loop.State.TurnRestores != 1 {
		t.Errorf("TurnRestores = %d, want 1", loop.State.TurnRestores)
	}
	var hasGuidance bool
	for _, m := range loop.State.Messages {
		if strings.Contains(m.Content, "exhausted your iteration budget") {
			hasGuidance = true
		}
	}
	if !hasGuidance {
		t.Error("post-restore messages should contain the exhaustion guidance")
	}
}

func TestLoop_TurnRestore_MaxRestoresHonored(t *testing.T) {
	ckpt := newFakeCheckpointer()
	// Enough identical tool-call responses for every pass; the provider
	// never produces a final answer.
	responses := make([]*types.ChatResponse, 8)
	for i := range responses {
		responses[i] = turnCkToolResponse(fmt.Sprintf("tc%d", i), "noop", `{}`)
	}
	fp := &fakeProvider{responses: responses}

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok"})

	loop := &Loop{
		Provider: fp,
		Registry: reg,
		Config: LoopConfig{
			SystemPrompt:          "sp",
			MaxLoopCycles:         1,
			MaxTurnRestores:       2,
			TurnCheckpointer:      ckpt,
			TurnCheckpointEnabled: true,
		},
	}

	_, err := loop.Run(context.Background(), "work")
	var maxErr MaxIterationsError
	if !errors.As(err, &maxErr) {
		t.Fatalf("expected MaxIterationsError after exhausting restores, got %v", err)
	}
	if loop.State.TurnRestores != 2 {
		t.Errorf("TurnRestores = %d, want 2 (capped by MaxTurnRestores)", loop.State.TurnRestores)
	}
	if got := ckpt.checkpointCount(); got != 3 {
		t.Errorf("checkpoints = %d, want 3 (initial + one per restored retry)", got)
	}
}

func TestLoop_TurnCheckpoint_SnapshotRoundTrip(t *testing.T) {
	messages := []types.Message{
		types.SystemMsg("system prompt"),
		types.UserMsg("fix the bug"),
		{
			Role:             "assistant",
			Content:          "on it",
			ReasoningContent: "thinking hard",
			ToolCalls: []types.ToolCall{{
				ID:   "tc1",
				Type: "function",
				Function: types.ToolCallFn{
					Name:      "write",
					Arguments: `{"filePath":"/tmp/x.go","content":"package x"}`,
				},
			}},
		},
		types.ToolResultMsg("tc1", "write", "Wrote 9 bytes to /tmp/x.go"),
	}

	snap, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored []types.Message
	if err := json.Unmarshal(snap, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	got, err := json.Marshal(restored)
	if err != nil {
		t.Fatalf("re-marshal restored: %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("snapshot round-trip lost data:\nwant %s\ngot  %s", want, got)
	}
}
