package yaah

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/providers"
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
}

// requireAuth rejects requests that do not carry a valid token.
func (ws *webServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenValid(ws.token, webTokenFromRequest(r)) {
			http.Error(w, "unauthorized: token missing or invalid", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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

	mux := http.NewServeMux()

	subFS, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	mux.Handle("/", ws.requireAuth(http.FileServer(http.FS(subFS))))
	mux.Handle("/api/stream", ws.requireAuth(http.HandlerFunc(ws.handleStream)))
	mux.Handle("/api/action", ws.requireAuth(http.HandlerFunc(ws.handleAction)))
	mux.Handle("/api/commands", ws.requireAuth(http.HandlerFunc(ws.handleCommands)))

	srv := &http.Server{Addr: webAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

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

	ws.mu.Lock()
	ws.view = v
	ws.sseDone = done
	ws.mu.Unlock()

	ws.sess.SetView(v)
	ws.sess.SetCtrlCh(ctrlCh)

	// Store ctrlCh for the question handler.
	ws.ctrlChMu.Lock()
	ws.ctrlCh = ctrlCh
	ws.ctrlChMu.Unlock()

	// Send meta and current todo state to the newly-connected client.
	v.SendConnect()

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

	ws.mu.Lock()
	ws.view = nil
	close(done)
	ws.mu.Unlock()

	// Clear ctrlCh when stream disconnects.
	ws.ctrlChMu.Lock()
	ws.ctrlCh = nil
	ws.ctrlChMu.Unlock()

	ws.sess.SetView(agent.NoopView{})
	ws.sess.SetCtrlCh(nil)
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

func (ws *webServer) handleCommands(w http.ResponseWriter, r *http.Request) {
	type cmd struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Arg         bool   `json:"arg,omitempty"`
	}
	cmds := []cmd{
		{Name: ":compact", Description: "Summarize old messages"},
		{Name: ":model", Description: "Switch model"},
		{Name: ":steer", Description: "Inject text into current turn", Arg: true},
		{Name: ":clear", Description: "Clear chat history"},
		{Name: ":help", Description: "Show available commands"},
		{Name: ":stop", Description: "Abort the running agent"},
		{Name: ":login", Description: "Authenticate with an OAuth provider"},
		{Name: ":logout", Description: "Remove stored OAuth credentials"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cmds)
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
