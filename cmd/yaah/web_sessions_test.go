package yaah

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

// sessionsStub installs a canned sessionList on a bare webServer (the
// production wiring links the store in runWeb). The returned slice
// records each store call as "cwd|cursor|limit".
func sessionsStub(t *testing.T) (*webServer, *[]string) {
	t.Helper()
	touched := &[]string{}
	ws := &webServer{token: "good-token"}
	ws.sessionList = func(cwdFilter, cursor string, limit int) ([]memory.SessionSummary, string, error) {
		*touched = append(*touched, fmt.Sprintf("%s|%s|%d", cwdFilter, cursor, limit))
		rows := []memory.SessionSummary{{
			ID: "sess-1", Title: "fix the flaky test", CWD: cwdFilter,
			StartedAt:    time.Now().Unix() - 60,
			UpdatedAt:    time.Now().Unix(),
			MessageCount: 3,
		}}
		return rows, "", nil
	}
	return ws, touched
}

// TestWebSessions_authAndShape pins GET-only behavior behind requireAuth
// and the JSON envelope (empty pages as [], keyset cursor passthrough).
func TestWebSessions_authAndShape(t *testing.T) {
	ws, touched := sessionsStub(t)
	handler := ws.requireAuth(http.HandlerFunc(ws.handleSessions))

	// Missing token: auth middleware rejects with 401.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}

	// Authenticated GET: query parameters pass through; assert the JSON
	// envelope.
	req = httptest.NewRequest(http.MethodGet, "/api/sessions?cwd=/x&limit=2", nil)
	req.Header.Set("X-Yaah-Token", "good-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	var body struct {
		Sessions   []memory.SessionSummary `json:"sessions"`
		NextCursor string                  `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Title != "fix the flaky test" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}

	// The store was queried exactly once with the translated parameters.
	if len(*touched) != 1 || (*touched)[0] != "/x||2" {
		t.Fatalf("sessionList args = %v", *touched)
	}
}

func TestWebSessions_validationErrors(t *testing.T) {
	ws, _ := sessionsStub(t)
	handler := ws.requireAuth(http.HandlerFunc(ws.handleSessions))

	tests := []struct {
		name, url, method string
		want              int
	}{
		{"post rejected", "/api/sessions", http.MethodPost, http.StatusMethodNotAllowed},
		{"bad cwd", "/api/sessions?cwd=relative", http.MethodGet, http.StatusBadRequest},
		{"bad limit", "/api/sessions?limit=0", http.MethodGet, http.StatusBadRequest},
		{"huge limit", "/api/sessions?limit=1000", http.MethodGet, http.StatusBadRequest},
		{"non-numeric limit", "/api/sessions?limit=big", http.MethodGet, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, nil)
			req.Header.Set("X-Yaah-Token", "good-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestWebSessions_unwiredReturns503(t *testing.T) {
	ws := &webServer{token: "good-token"}
	handler := ws.requireAuth(http.HandlerFunc(ws.handleSessions))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("X-Yaah-Token", "good-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
