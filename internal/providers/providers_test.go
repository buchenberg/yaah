package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSessionIDFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("SessionIDFromContext(empty) = %q, want \"\"", got)
	}
}

func TestWithSessionID_RoundTrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "test-session-123")
	if got := SessionIDFromContext(ctx); got != "test-session-123" {
		t.Errorf("SessionIDFromContext(WithSessionID(x)) = %q, want %q", got, "test-session-123")
	}
}

func TestWithSessionID_Empty(t *testing.T) {
	ctx := WithSessionID(context.Background(), "")
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("SessionIDFromContext(WithSessionID(empty)) = %q, want \"\"", got)
	}
}

func TestSetSessionHeaders_Present(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	setSessionHeaders(req, "my-session")

	id := req.Header.Get("x-session-id")
	aff := req.Header.Get("x-session-affinity")

	if id == "" {
		t.Error("x-session-id header not set")
	}
	if aff == "" {
		t.Error("x-session-affinity header not set")
	}
	if id != aff {
		t.Errorf("x-session-id (%q) != x-session-affinity (%q)", id, aff)
	}

	// SHA-256 produces 64 hex chars.
	if len(id) != 64 {
		t.Errorf("x-session-id length = %d, want 64 (SHA-256 hex)", len(id))
	}
}

func TestSetSessionHeaders_Empty(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	setSessionHeaders(req, "")

	if req.Header.Get("x-session-id") != "" {
		t.Error("x-session-id should not be set for empty session ID")
	}
}

func TestSetSessionHeaders_Stable(t *testing.T) {
	req1, _ := http.NewRequest("POST", "http://example.com", nil)
	req2, _ := http.NewRequest("POST", "http://example.com", nil)

	setSessionHeaders(req1, "same-session")
	setSessionHeaders(req2, "same-session")

	if got := req1.Header.Get("x-session-id"); got != req2.Header.Get("x-session-id") {
		t.Errorf("same session ID produced different hashes: %q vs %q",
			got, req2.Header.Get("x-session-id"))
	}
}

func TestSetSessionHeaders_Different(t *testing.T) {
	req1, _ := http.NewRequest("POST", "http://example.com", nil)
	req2, _ := http.NewRequest("POST", "http://example.com", nil)

	setSessionHeaders(req1, "session-a")
	setSessionHeaders(req2, "session-b")

	if req1.Header.Get("x-session-id") == req2.Header.Get("x-session-id") {
		t.Error("different session IDs produced same hash")
	}
}

func TestOpenAISendMarksUsageInclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":90}}}`)
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "test-key", 1)
	resp, err := c.Send(context.Background(), types.ChatRequest{
		Model:    "test-model",
		Messages: []types.Message{types.UserMsg("hi")},
	})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !resp.Usage.PromptIncludesCache {
		t.Fatal("OpenAI usage must be marked PromptIncludesCache")
	}
	if got := resp.Usage.InclusivePromptTokens(); got != 100 {
		t.Errorf("InclusivePromptTokens() = %d, want 100", got)
	}
}
