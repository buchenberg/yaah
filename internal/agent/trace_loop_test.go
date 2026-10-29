package agent

import (
	"context"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func TestLoopWritesReconstructableTranscript(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open trace store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	fp := &fakeProvider{responses: []*types.ChatResponse{{
		Choices: []types.Choice{{
			Message: types.Message{
				Role:               "assistant",
				Content:            "the answer is 42",
				ReasoningContent:   "because",
				ReasoningSignature: "sig",
			},
			FinishReason: "stop",
		}},
		Usage: types.Usage{PromptTokens: 10, CompletionTokens: 5},
	}}}
	loop := &Loop{
		Config: LoopConfig{
			SessionID:     "sess-transcript",
			TraceStore:    store,
			SystemPrompt:  "system marker",
			Model:         "test-model",
			MaxLoopCycles: 2,
		},
		Provider:  fp,
		Registry:  tools.NewRegistry(),
		Persister: NewSessionPersister(nil, nil, "sess-transcript"),
	}
	if _, err := loop.Run(context.Background(), "what is the answer?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	transcript, err := pipeline.RebuildTranscript(context.Background(), store, "sess-transcript")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if transcript.SystemPrompt != "system marker" {
		t.Errorf("system prompt = %q", transcript.SystemPrompt)
	}
	if len(transcript.Messages) != 3 {
		t.Fatalf("messages = %#v", transcript.Messages)
	}
	if transcript.Messages[1].Role != "user" || transcript.Messages[1].Content != "what is the answer?" {
		t.Errorf("user = %#v", transcript.Messages[1])
	}
	if got := transcript.Messages[2]; got.Content != "the answer is 42" || got.ReasoningSignature != "sig" {
		t.Errorf("assistant = %#v", got)
	}
}

func TestLoopBuilderCarriesTraceStore(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close(context.Background())
	loop := (&LoopBuilder{TraceStore: store, SessionID: "sess-builder"}).Build(LoopBuildOptions{})
	if loop.Config.TraceStore != store {
		t.Fatal("LoopBuilder dropped the trace store")
	}
}
