package yaah

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/sessions"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/spf13/cobra"
)

//go:embed web
var webFS embed.FS

var webAddr string
var webToken string

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start a local web UI for yaah",
	Long: `web starts an HTTP server with a browser-based chat interface.
The agent session persists across prompts for the lifetime of the server.
Events stream to the browser via Server-Sent Events (SSE); commands are
sent via HTTP POST. Binds to loopback by default.

Every request must carry the auth token (X-Yaah-Token header, ?t= query
parameter, or yaah_token cookie). With --token empty a random token is
generated and printed at startup.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runWeb,
}

func init() {
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8080", "listen address")
	webCmd.Flags().StringVar(&webToken, "token", "", "auth token for the web UI (randomly generated when empty)")
	rootCmd.AddCommand(webCmd)
}

// webTokenFromRequest extracts the presented auth token: X-Yaah-Token
// header first, then the ?t= query parameter (EventSource cannot set
// headers), then the yaah_token cookie.
func webTokenFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Yaah-Token"); t != "" {
		return t
	}
	if t := r.URL.Query().Get("t"); t != "" {
		return t
	}
	if c, err := r.Cookie("yaah_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// tokenValid reports whether presented matches expected in constant
// time. Empty tokens never validate.
func tokenValid(expected, presented string) bool {
	if expected == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}

// originAllowed is the CSRF guard for state-changing endpoints: when a
// browser sends an Origin header it must match the request's Host.
// Requests without an Origin header (curl, same-origin fetch in some
// modes) pass; the token check is the primary gate.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

// newWebToken generates a random 32-byte hex auth token.
func newWebToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type webServer struct {
	sess            Session
	view            *sseView
	am              *answerMap
	token           string
	idGen           atomic.Int64
	promptCtxCancel context.CancelFunc
	running         atomic.Bool
	sseDone         chan struct{}
	mu              sync.Mutex
	ctrlChMu        sync.Mutex
	ctrlCh          chan<- control.Msg

	// models caches the available model list ("provider/model" format)
	// and provider display names, fetched once in the background.
	models        []string
	providerNames map[string]string

	// sessionList serves /api/sessions from the shared session store
	// (same rows ACP session/list and the CLI read). Wired in runWeb;
	// nil keeps the endpoint unavailable.
	sessionList func(cwdFilter, cursor string, limit int) ([]memory.SessionSummary, string, error)
}

// requireAuth rejects requests that do not carry a valid token. A valid
// ?t= query token is additionally persisted as a yaah_token cookie so
// subsequent requests that cannot carry the query param (relative
// stylesheet/script assets, fetch, EventSource) authenticate; the
// navigational GET that carried it is redirected to the clean URL so
// the token does not linger in the address bar and history.
func (ws *webServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := webTokenFromRequest(r)
		if !tokenValid(ws.token, presented) {
			http.Error(w, "unauthorized: token missing or invalid", http.StatusUnauthorized)
			return
		}
		if q := r.URL.Query().Get("t"); q != "" && q == presented {
			// Secure is set only when the request arrived over TLS. yaah
			// binds to loopback and is normally used over plain
			// http://127.0.0.1, where a Secure cookie is never sent back:
			// auth would silently break after the first redirect. Behind a
			// TLS-terminating proxy r.TLS is non-nil and the cookie is
			// hardened.
			http.SetCookie(w, &http.Cookie{
				Name:     "yaah_token",
				Value:    presented,
				Path:     "/",
				HttpOnly: true,
				Secure:   r.TLS != nil,
				SameSite: http.SameSiteLaxMode,
			})
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// webCSP is the Content-Security-Policy for the embedded UI. index.html is a
// single self-contained page: its <script> and <style> are inline, it loads
// alpine/marked/purify/pico from this same origin, and it consumes the agent
// stream through an EventSource on /api/stream. 'unsafe-inline' is therefore
// required for script-src and style-src, default-src stays 'self', and
// connect-src 'self' keeps the same-origin SSE connection working. Nothing on
// the page submits a form or rewrites its base URL, so base-uri and
// form-action are 'none'.
// 'unsafe-eval' is required by Alpine: x-show/x-if/x-on expressions are
// evaluated with new Function(), which a policy without it blocks — the page
// then renders but nothing is reactive. 'unsafe-inline' covers the single
// inline <script>/<style> the page is built from. Both are the honest cost of
// shipping a self-contained page; removing them means adopting Alpine's
// CSP build plus a nonce, which is a larger change than this one.
const webCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// withSecurityHeaders hardens every response. It is defence in depth for a
// loopback-only tool: the browser holding the token is the only intended
// client, and a stray asset must not be able to frame the UI, have its markup
// sniffed as script, or leak the URL — which carries ?t= on first load —
// through a Referer. Applied to the whole mux, so a route added later cannot
// ship without them.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", webCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// routes builds the HTTP surface: static assets under /, the SSE stream, the
// action endpoint, and the session-registry projection. Every route sits
// behind the token check and the whole mux behind withSecurityHeaders.
//
// There is deliberately no /api/commands endpoint (yaah-dvp). Nothing ever
// fetched it: the browser's slash-command list lives in index.html and every
// one of those commands is either executed client-side or posted to
// /api/action, and the server copy had already drifted from what the client
// can actually run. A second hand-maintained copy of a purely client-side list
// only manufactures drift, so the route and its handler were deleted rather
// than kept as a pretend source of truth.
func (ws *webServer) routes() (http.Handler, error) {
	subFS, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", ws.requireAuth(http.FileServer(http.FS(subFS))))
	mux.Handle("/api/stream", ws.requireAuth(http.HandlerFunc(ws.handleStream)))
	mux.Handle("/api/action", ws.requireAuth(http.HandlerFunc(ws.handleAction)))
	mux.Handle("/api/sessions", ws.requireAuth(http.HandlerFunc(ws.handleSessions)))
	return withSecurityHeaders(mux), nil
}

type actionRequest struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ID       string `json:"id"`
	Value    string `json:"value"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func runWeb(cmd *cobra.Command, args []string) error {
	sess, err := newAgentSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()

	token := webToken
	if token == "" {
		token, err = newWebToken()
		if err != nil {
			return fmt.Errorf("generate auth token: %w", err)
		}
	}

	ws := &webServer{
		sess:  sess,
		am:    newAnswerMap(),
		token: token,
	}
	if sess.db != nil {
		// The registry is the single session seam: serve the same rows ACP
		// session/list and the CLI surface. The store is borrowed — the
		// agent session owns the handle and closes it.
		reg := sessions.New(sess.db)
		ws.sessionList = func(cwdFilter, cursor string, limit int) ([]memory.SessionSummary, string, error) {
			return reg.Page(sessions.Filter{CWD: cwdFilter, Cursor: cursor, Limit: limit})
		}
	}

	// Pre-fetch model lists from all providers in the background so the
	// command palette can offer model switching without blocking startup.
	go func() {
		names := make(map[string]string)
		for key, p := range sess.cfg.Providers {
			if p.Name != "" {
				names[key] = p.Name
			}
		}
		models := providers.FetchAllModels(context.Background(), sess.cfg, makeModelLister)

		ws.mu.Lock()
		ws.models = models
		ws.providerNames = names
		v := ws.view
		ws.mu.Unlock()

		if v != nil {
			v.write(marshalWire(sseWireEvent{Type: "ctrl.models", Models: models, Providers: names}))
		}
	}()

	// Wire question tool handler so questions are sent as CtrlQuestion
	// messages. forwardCtrl in web_view.go converts these to SSE events
	// for the browser dialog.
	if qt := sess.toolReg.Get("question"); qt != nil {
		qtp := qt.(*tools.QuestionTool)
		qtp.Handler = func(entries []tools.QuestionEntry) []string {
			var answers []string
			for _, e := range entries {
				ws.ctrlChMu.Lock()
				ch := ws.ctrlCh
				ws.ctrlChMu.Unlock()
				if ch == nil {
					// No active stream — fall back to first option.
					answers = append(answers, fallbackCtrlAnswer(e))
					continue
				}
				ansCh := make(chan string, 1)
				q := buildCtrlQuestion(e, ansCh)
				select {
				case ch <- q:
				case <-time.After(ctrlSendTimeout):
					answers = append(answers, fallbackCtrlAnswer(e))
					continue
				}
				answers = append(answers, awaitCtrlAnswer(e, ansCh))
			}
			return answers
		}
	}

	sess.SetApproveFn(func(name, args string) bool {
		ws.mu.Lock()
		v := ws.view
		done := ws.sseDone
		ws.mu.Unlock()
		if v == nil {
			return false
		}
		id := fmt.Sprintf("q%d", ws.idGen.Add(1))
		ch := make(chan string, 1)
		ws.am.register(id, ch)
		v.write(marshalWire(sseWireEvent{
			Type: "ctrl.approval", ID: id, Name: name, Args: args,
		}))
		select {
		case ans := <-ch:
			return ans == "true"
		case <-done:
			ws.am.cancel(id)
			return false
		}
	})

	handler, err := ws.routes()
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: webAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	fmt.Fprintf(os.Stderr, "%s listening on %s%s\n",
		Bold("yaah web"), "http://"+webAddr, Dim(" (Ctrl-C to stop)"))
	fmt.Fprintf(os.Stderr, "%s http://%s/?t=%s\n", Dim("open:"), webAddr, token)

	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "\n%s\n", Dim("shutting down..."))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}

func (ws *webServer) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Require Accept: text/event-stream to prevent browsers from hanging
	// when navigating directly to /api/stream.
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "use EventSource to connect — this endpoint requires Accept: text/event-stream", http.StatusNotAcceptable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	v := &sseView{w: w}
	v.provider = ws.sess.ProviderName()
	v.model = ws.sess.ModelName()
	infos := ws.sess.MCPInfos()
	v.mcpServers = make([]wireMCPServer, len(infos))
	for i, info := range infos {
		v.mcpServers[i] = wireMCPServer{
			Name:      info.Name,
			Transport: info.Transport,
			Connected: info.Connected,
			ToolCount: info.ToolCount,
			Error:     info.Error,
		}
	}
	ctrlCh := make(chan control.Msg, 64)
	done := make(chan struct{})

	// Install the session view inside the same critical section the teardown
	// re-checks below. Done separately, a reload could slip between an
	// ownership check and the clear, leaving the session detached from the
	// live connection.
	ws.mu.Lock()
	ws.view = v
	ws.sseDone = done
	ws.sess.SetView(v)
	ws.sess.SetCtrlCh(ctrlCh)
	ws.mu.Unlock()

	// Store ctrlCh for the question handler.
	ws.ctrlChMu.Lock()
	ws.ctrlCh = ctrlCh
	ws.ctrlChMu.Unlock()

	// Send meta and current todo state to the newly-connected client.
	v.SendConnect()

	// Catch-up frame. A reload or an EventSource auto-reconnect has no idea
	// whether a run is still in flight, so the client reconciles its live
	// flag from this instead of guessing. Without it, a run that finished
	// while the tab was disconnected left the composer permanently busy.
	running := ws.running.Load()
	v.write(marshalWire(sseWireEvent{Type: "ctrl.state", Running: &running}))

	// If the model list has already been fetched, deliver it immediately;
	// otherwise the background fetch goroutine sends it when ready.
	ws.mu.Lock()
	models, names := ws.models, ws.providerNames
	ws.mu.Unlock()
	if len(models) > 0 {
		v.write(marshalWire(sseWireEvent{Type: "ctrl.models", Models: models, Providers: names}))
	}

	streamCtx, streamCancel := context.WithCancel(r.Context())
	defer streamCancel()

	go forwardCtrl(streamCtx, ctrlCh, v, ws.am, &ws.idGen)

	<-r.Context().Done()

	// Ownership-conditional teardown. On reload the browser opens the new
	// stream (installing its view and ctrlCh) before this handler's context
	// is cancelled, so an unconditional teardown here wiped state that now
	// belongs to the newer connection: the live client was left with a nil
	// view and a nil ctrlCh, so approvals and tool events never arrived and
	// the composer stayed wedged.
	owned := ws.releaseStream(v)
	close(done)
	if !owned {
		return
	}

	// Clear ctrlCh only when this handler still owns it.
	ws.ctrlChMu.Lock()
	if ws.ctrlCh == ctrlCh {
		ws.ctrlCh = nil
	}
	ws.ctrlChMu.Unlock()

	// Re-check under ws.mu before detaching the session. releaseStream having
	// returned true only says no newer view existed at that instant; a reload
	// may have installed one since, and that connection must keep its view.
	ws.mu.Lock()
	if ws.view == nil {
		ws.sess.SetView(agent.NoopView{})
		ws.sess.SetCtrlCh(nil)
	}
	ws.mu.Unlock()
}

// releaseStream uninstalls v as the active SSE view and reports whether it was
// still the active one. A superseded handler — one whose request was cancelled
// after a reload or an EventSource reconnect installed a newer view — must not
// clear state that belongs to its successor, or the live client is left with a
// nil view/ctrlCh and never sees another approval prompt.
func (ws *webServer) releaseStream(v *sseView) (owned bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.view != v {
		return false
	}
	ws.view = nil
	ws.sseDone = nil
	return true
}

func (ws *webServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// CSRF hardening: reject cross-origin requests (a browser always
	// sends Origin on cross-origin POSTs) and non-JSON content types
	// (text/plain posts skip the CORS preflight).
	if !originAllowed(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	switch req.Type {
	case "prompt":
		if ws.running.Swap(true) {
			http.Error(w, "prompt already running", http.StatusConflict)
			return
		}
		go ws.runPrompt(req.Text)
		w.WriteHeader(http.StatusAccepted)

	case "abort":
		ws.mu.Lock()
		cancel := ws.promptCtxCancel
		ws.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		w.WriteHeader(http.StatusOK)

	case "answer":
		if !ws.am.deliver(req.ID, req.Value) {
			http.Error(w, "no pending answer for id", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	case "compact":
		ws.sess.Compact()
		w.WriteHeader(http.StatusOK)

	case "model":
		ws.sess.SetModel(req.Provider, req.Model)
		ws.mu.Lock()
		v := ws.view
		ws.mu.Unlock()
		if v != nil {
			v.SetHeader(req.Provider, req.Model)
		}
		w.WriteHeader(http.StatusOK)

	case "login":
		go webLogin(ws)
		w.WriteHeader(http.StatusAccepted)

	case "logout":
		go webLogout(ws)
		w.WriteHeader(http.StatusAccepted)

	case "steer":
		if req.Text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		ws.sess.Steer(req.Text)
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "unknown action type", http.StatusBadRequest)
	}
}

func (ws *webServer) runPrompt(text string) {
	ctx, cancel := context.WithCancel(context.Background())
	ws.mu.Lock()
	ws.promptCtxCancel = cancel
	ws.mu.Unlock()

	_, _, _ = ws.sess.RunPrompt(ctx, text)

	ws.mu.Lock()
	ws.promptCtxCancel = nil
	ws.mu.Unlock()
	ws.running.Store(false)
}

// maxSessionsLimit caps /api/sessions page sizes: the endpoint backs a
// picker/recent list, not a bulk export.
const maxSessionsLimit = 200

// handleSessions serves the engine-level session registry's picker
// projection — the same rows ACP session/list and the CLI surface. Optional
// query parameters mirror the CLI: cwd (exact-match, canonicalized like
// session/list), limit (1..200, default 50), cursor (keyset page).
func (ws *webServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.sessionList == nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	cwdFilter := q.Get("cwd")
	if cwdFilter != "" {
		var err error
		if cwdFilter, err = cwdpath.Canonical(cwdFilter); err != nil {
			http.Error(w, fmt.Sprintf("cwd: %v", err), http.StatusBadRequest)
			return
		}
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxSessionsLimit {
			http.Error(w, fmt.Sprintf("limit: 1..%d", maxSessionsLimit), http.StatusBadRequest)
			return
		}
		limit = n
	}
	rows, next, err := ws.sessionList(cwdFilter, q.Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, sessions.ErrInvalidCursor) {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		// Never echo internal error text to the client.
		http.Error(w, "session store error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []memory.SessionSummary{} // empty pages serialize as []
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		Sessions   []memory.SessionSummary `json:"sessions"`
		NextCursor string                  `json:"nextCursor"`
	}{Sessions: rows, NextCursor: next})
}

func webLogin(ws *webServer) {
	send := func(text string) {
		ws.mu.Lock()
		v := ws.view
		ws.mu.Unlock()
		if v != nil {
			v.write(marshalWire(sseWireEvent{Type: "ctrl.status", Text: text}))
		}
	}

	cfg := ws.sess.(*agentSession).cfg
	names := oauthProviderNames(cfg)
	if len(names) == 0 {
		send("No OAuth providers configured. Add auth: oauth to a provider in config.yaml.")
		return
	}
	providerName := names[0]

	if err := loginOAuth(cfg, providerName, send); err != nil {
		send(fmt.Sprintf("Login failed: %v", err))
	}
}

func webLogout(ws *webServer) {
	send := func(text string) {
		ws.mu.Lock()
		v := ws.view
		ws.mu.Unlock()
		if v != nil {
			v.write(marshalWire(sseWireEvent{Type: "ctrl.status", Text: text}))
		}
	}

	cfg := ws.sess.(*agentSession).cfg
	names := oauthProviderNames(cfg)
	if len(names) == 0 {
		send("No OAuth providers configured.")
		return
	}
	providerName := names[0]

	if err := logoutOAuth(cfg, providerName, send); err != nil {
		send(fmt.Sprintf("Logout failed: %v", err))
	}
}
