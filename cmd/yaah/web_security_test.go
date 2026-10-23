package yaah

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityHeaders pins the response hardening on every route. The
// middleware wraps the whole mux, so the table deliberately covers a static
// asset, the SSE stream, a JSON API route, and the auth rejection path — a
// route added later must not be able to ship without these headers (yaah-4wb).
func TestSecurityHeaders(t *testing.T) {
	ws := &webServer{token: "good-token", am: newAnswerMap()}
	h, err := ws.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	wantHeaders := map[string]string{
		"Content-Security-Policy": webCSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	}

	tests := []struct {
		name   string
		target string
		token  string
		want   int
	}{
		{"index", "/", "good-token", http.StatusOK},
		{"static asset", "/purify.min.js", "good-token", http.StatusOK},
		// /api/stream rejects a request without Accept: text/event-stream
		// before it touches session state, which is enough to prove the
		// route itself is wrapped.
		{"sse route", "/api/stream", "good-token", http.StatusNotAcceptable},
		{"action route", "/api/action", "good-token", http.StatusMethodNotAllowed},
		{"sessions route", "/api/sessions", "good-token", http.StatusServiceUnavailable},
		{"auth rejection", "/", "", http.StatusUnauthorized},
		{"unknown path", "/missing.txt", "good-token", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+tt.target, nil)
			if tt.token != "" {
				r.Header.Set("X-Yaah-Token", tt.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.want {
				t.Errorf("status = %d; want %d", rec.Code, tt.want)
			}
			for name, want := range wantHeaders {
				if got := rec.Header().Get(name); got != want {
					t.Errorf("%s = %q; want %q", name, got, want)
				}
			}
		})
	}

	// The policy must keep the inline <script>/<style> and the same-origin
	// EventSource working: dropping either directive would break the UI, not
	// just weaken the policy.
	t.Run("csp keeps the inline assets and same-origin stream", func(t *testing.T) {
		for _, want := range []string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"connect-src 'self'",
			"base-uri 'none'",
			"form-action 'none'",
			"frame-ancestors 'none'",
		} {
			if !strings.Contains(webCSP, want) {
				t.Errorf("csp = %q; missing %q", webCSP, want)
			}
		}
	})
}

// TestAuthCookieSecureOnlyOverTLS pins the conditional Secure flag. yaah binds
// to loopback and is normally reached over plain http://127.0.0.1, where a
// Secure cookie is never sent back — marking it Secure unconditionally would
// break auth after the first redirect. Under TLS it must be Secure.
func TestAuthCookieSecureOnlyOverTLS(t *testing.T) {
	ws := &webServer{token: "good-token"}
	h := ws.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		tls    bool
		secure bool
	}{
		{"plain http", false, false},
		{"tls", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/?t=good-token", nil)
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			var cookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "yaah_token" {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatal("no yaah_token cookie was set for a ?t= request")
			}
			if cookie.Secure != tt.secure {
				t.Errorf("cookie Secure = %v; want %v", cookie.Secure, tt.secure)
			}
			if !cookie.HttpOnly {
				t.Error("cookie must stay HttpOnly")
			}
		})
	}
}
