package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSend_sendsChatRequest(t *testing.T) {
	var received types.ChatRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		resp := types.ChatResponse{
			ID:    "test-1",
			Model: "gpt-4o-mini",
			Choices: []types.Choice{{
				Index: 0,
				Message: types.Message{
					Role:    "assistant",
					Content: "Hello back!",
				},
				FinishReason: "stop",
			}},
			Usage: types.Usage{
				PromptTokens:     5,
				CompletionTokens: 3,
				TotalTokens:      8,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test")
	req := types.ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	resp, err := client.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if received.Model != "gpt-4o-mini" {
		t.Errorf("server got wrong model: %q", received.Model)
	}
	if len(received.Messages) != 1 || received.Messages[0].Content != "Hello" {
		t.Errorf("server got wrong messages: %v", received.Messages)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello back!" {
		t.Errorf("response content = %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("total_tokens = %d, want 8", resp.Usage.TotalTokens)
	}
}

func TestSend_includesAPIKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-key" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(types.ChatResponse{})
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test-key")
	_, err := client.Send(context.Background(), types.ChatRequest{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
}

func TestSend_returnsErrorOnHTTPFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test")
	_, err := client.Send(context.Background(), types.ChatRequest{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestEstimateTokens_returnsRoughEstimate(t *testing.T) {
	// token estimator uses char/4 as a rough fallback
	tok := EstimateTokens("hello world, this is a test")
	if tok < 5 || tok > 15 {
		t.Errorf("EstimateTokens = %d, expected roughly 7-8", tok)
	}
}

func TestListModels_returnsModelIDs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("expected /models, got %s", r.URL.Path)
		}
		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "gpt-4o", "object": "model"},
				{"id": "gpt-4o-mini", "object": "model"},
				{"id": "o1", "object": "model"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test")
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(models), models)
	}
	if models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" || models[2] != "o1" {
		t.Errorf("models = %v, want [gpt-4o gpt-4o-mini o1]", models)
	}
}

func TestListModels_returnsErrorOnHTTPFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test")
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestListModels_includesAPIKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-key" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()

	client := NewOpenAIClient(ts.URL, "sk-test-key")
	_, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}
}
