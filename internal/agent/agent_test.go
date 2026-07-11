package agent

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// fakeProvider implements Provider for testing.
type fakeProvider struct {
	responses []*types.ChatResponse
	index     int
	requests  []types.ChatRequest
}

func (f *fakeProvider) Send(req types.ChatRequest) (*types.ChatResponse, error) {
	f.requests = append(f.requests, req)
	if f.index >= len(f.responses) {
		return &types.ChatResponse{
			Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			}},
		}, nil
	}
	resp := f.responses[f.index]
	f.index++
	return resp, nil
}

func TestLoop_plainTextResponse(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "Hello! How can I help?"},
				FinishReason: "stop",
			}},
		}},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 10,
	}

	resp, err := loop.Run(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp != "Hello! How can I help?" {
		t.Errorf("response = %q", resp)
	}
	if len(fp.requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(fp.requests))
	}
}

func TestLoop_toolCalling(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				// First response: model wants to call a tool
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "read",
								Arguments: `{"path":"/tmp/nonexistent"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				// Second response: model summarizes tool output
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "The file doesn't exist."},
					FinishReason: "stop",
				}},
			},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 10,
	}

	resp, err := loop.Run(context.Background(), "Read /tmp/nonexistent")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp != "The file doesn't exist." {
		t.Errorf("response = %q", resp)
	}
	// Should have made 2 requests (tool call + follow-up)
	if len(fp.requests) != 2 {
		t.Errorf("expected 2 requests, got %d", len(fp.requests))
	}
}

func TestLoop_hitsMaxIterations(t *testing.T) {
	// Model keeps calling tools forever
	fp := &fakeProvider{}
	for i := 0; i < 20; i++ {
		fp.responses = append(fp.responses, &types.ChatResponse{
			Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:   "call_loop",
						Type: "function",
						Function: types.ToolCallFn{
							Name:      "read",
							Arguments: `{"path":"/dev/null"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
		})
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 3,
	}

	_, err := loop.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected error for max iterations")
	}
	if len(fp.requests) > 3 {
		t.Errorf("expected at most 3 requests, got %d", len(fp.requests))
	}
}
