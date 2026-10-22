package yaah

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
)

// sessionsStub installs a canned sessionList on a bare webServer (the
// production wiring links the store in runWeb). The returned slice
// records each store call as "cwd|cursor|limit". A "bad" cursor fails
// with memory.ErrInvalidCursor; a "boom" cursor fails with internal
// error text that must not leak to clients.
func sessionsStub(t *testing.T) (*webServer, *[]string) {
	t.Helper()
	touched := &[]string{}
	ws := &webServer{token: "good-token"}
	ws.sessionList = func(cwdFilter, cursor string, limit int) ([]memory.SessionSummary, string, error) {
		*touched = append(*touched, fmt.Sprintf("%s|%s|%d", cwdFilter, cursor, limit))
		if cursor == "bad" {
			return nil, "", memory.ErrInvalidCursor
		}
		if cursor == "boom" {
			return nil, "", fmt.Errorf("sqlite: disk I/O error at /home/user/.yaah/state.db")
		}
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
	// envelope. The cwd is a real directory so the test passes on
	// Windows too (where "/x" is not an absolute path).
	dir := t.TempDir()
	canonical, err := acp.CanonicalFilterCwd(dir)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet,
		"/api/sessions?cwd="+url.QueryEscape(dir)+"&limit=2", nil)
	req.Header.Set("X-Yaah-Token", "good-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache control = %q, want no-store", cc)
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

	// The store was queried exactly once with the canonicalized cwd.
	if len(*touched) != 1 || (*touched)[0] != canonical+"||2" {
		t.Fatalf("sessionList args = %v, want %q", *touched, canonical+"||2")
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
		{"invalid cursor", "/api/sessions?cursor=bad", http.MethodGet, http.StatusBadRequest},
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

// TestWebSessions_internalErrorsNotEchoed pins that store failures map
// to a generic 500 body: the stub's error text mentions sqlite and a
// filesystem path and must never reach the client.
func TestWebSessions_internalErrorsNotEchoed(t *testing.T) {
	ws, _ := sessionsStub(t)
	handler := ws.requireAuth(http.HandlerFunc(ws.handleSessions))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?cursor=boom", nil)
	req.Header.Set("X-Yaah-Token", "good-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sqlite") || strings.Contains(body, "state.db") {
		t.Fatalf("internal error text echoed to client: %q", body)
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
