package yaah

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenValid(t *testing.T) {
	tests := []struct {
		name      string
		expected  string
		presented string
		want      bool
	}{
		{"match", "secret-token", "secret-token", true},
		{"mismatch", "secret-token", "wrong-token", false},
		{"empty presented", "secret-token", "", false},
		{"empty expected", "", "anything", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenValid(tt.expected, tt.presented); got != tt.want {
				t.Errorf("tokenValid(%q, %q) = %v; want %v", tt.expected, tt.presented, got, tt.want)
			}
		})
	}
}

func TestWebTokenFromRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://h/x?t=query-token", nil)
	if got := webTokenFromRequest(r); got != "query-token" {
		t.Errorf("query token = %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "http://h/x?t=query-token", nil)
	r.Header.Set("X-Yaah-Token", "header-token")
	if got := webTokenFromRequest(r); got != "header-token" {
		t.Errorf("header token should win, got %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "http://h/x", nil)
	r.AddCookie(&http.Cookie{Name: "yaah_token", Value: "cookie-token"})
	if got := webTokenFromRequest(r); got != "cookie-token" {
		t.Errorf("cookie token = %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "http://h/x", nil)
	if got := webTokenFromRequest(r); got != "" {
		t.Errorf("no token expected, got %q", got)
	}
}

func TestOriginAllowed(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin passes", "127.0.0.1:8080", "", true},
		{"same origin", "127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"cross origin", "127.0.0.1:8080", "http://evil.example", false},
		{"cross origin with path", "127.0.0.1:8080", "http://evil.example/phish.html", false},
		{"port mismatch", "127.0.0.1:8080", "http://127.0.0.1:9090", false},
		{"null origin", "127.0.0.1:8080", "null", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+tt.host+"/api/action", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if got := originAllowed(r); got != tt.want {
				t.Errorf("originAllowed = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestRequireAuth(t *testing.T) {
	ws := &webServer{token: "good-token"}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ws.requireAuth(ok)

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"valid token via header", "good-token", http.StatusOK},
		{"missing token", "", http.StatusUnauthorized},
		{"wrong token", "bad-token", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://h/", nil)
			if tt.token != "" {
				r.Header.Set("X-Yaah-Token", tt.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.want {
				t.Errorf("status = %d; want %d", rec.Code, tt.want)
			}
		})
	}

	t.Run("valid token via query", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://h/?t=good-token", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
		}
	})
}

func TestHandleAction_Guards(t *testing.T) {
	ws := &webServer{}

	t.Run("rejects cross-origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/action",
			strings.NewReader(`{"type":"prompt","text":"pwn"}`))
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ws.handleAction(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
		}
	})

	t.Run("rejects non-JSON content type", func(t *testing.T) {
		// text/plain cross-origin POSTs skip the CORS preflight — the
		// classic CSRF vector this guard closes.
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/action",
			strings.NewReader(`{"type":"prompt","text":"pwn"}`))
		r.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		ws.handleAction(rec, r)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusUnsupportedMediaType)
		}
	})

	t.Run("rejects non-POST", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/action", nil)
		rec := httptest.NewRecorder()
		ws.handleAction(rec, r)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

// TestIndexHTML_sanitizedRendering pins the XSS mitigations in the
// embedded web UI: markdown must be rendered through DOMPurify before
// reaching an x-html binding, and the sanitizer script must be present.
func TestIndexHTML_sanitizedRendering(t *testing.T) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	if !strings.Contains(html, `src="purify.min.js"`) {
		t.Error("index.html does not load purify.min.js")
	}
	// No x-html binding may call marked.parse directly — all markdown
	// must flow through the DOMPurify-wrapped render()/md() helpers.
	if strings.Contains(html, "x-html=\"marked.parse") {
		t.Error("x-html binding uses unsanitized marked.parse output")
	}
	if strings.Contains(html, "html: role === 'asst' ? marked.parse(text)") {
		t.Error("add() stores unsanitized marked.parse output for x-html")
	}
	if !strings.Contains(html, "DOMPurify.sanitize(marked.parse(") {
		t.Error("render() does not sanitize marked output with DOMPurify")
	}
}

func TestNewWebToken(t *testing.T) {
	a, err := newWebToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newWebToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 {
		t.Errorf("token length = %d; want 64 hex chars", len(a))
	}
	if a == b {
		t.Error("two generated tokens are identical")
	}
}
