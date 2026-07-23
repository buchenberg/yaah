package providers

import (
	"context"
	"net/http"
	"testing"
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
