package yaah

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/spf13/cobra"
)

//go:embed web
var webFS embed.FS

var webAddr string

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start a local web UI for yaah",
	Long: `web starts an HTTP server with a browser-based chat interface.
The agent session persists across prompts for the lifetime of the server.
Events stream to the browser via Server-Sent Events (SSE); commands are
sent via HTTP POST. Binds to loopback by default.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runWeb,
}

func init() {
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8080", "listen address")
	rootCmd.AddCommand(webCmd)
}

type webServer struct {
	sess            Session
	view            *sseView
	am              *answerMap
	idGen           atomic.Int64
	promptCtxCancel context.CancelFunc
	running         atomic.Bool
	sseDone         chan struct{}
	mu              sync.Mutex
	ctrlChMu        sync.Mutex
	ctrlCh          chan<- types.CtrlMsg

	// models caches the available model list ("provider/model" format)
	// and provider display names, fetched once in the background.
	models        []string
	providerNames map[string]string
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

	ws := &webServer{
		sess: sess,
		am:   newAnswerMap(),
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
		models := fetchAllModels(context.Background(), sess.cfg)

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
					if len(e.Options) > 0 {
						answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
					} else {
						answers = append(answers, e.Header+": ")
					}
					continue
				}
				ansCh := make(chan string, 1)
				opts := make([]types.CtrlOption, len(e.Options))
				for i, o := range e.Options {
					opts[i] = types.CtrlOption{Label: o.Label, Description: o.Description}
				}
				q := &types.CtrlQuestion{
					Header:   e.Header,
					Question: e.Question,
					Options:  opts,
					Multiple: e.Multiple,
					AnswerCh: ansCh,
				}
				select {
				case ch <- q:
				case <-time.After(30 * time.Second):
					if len(e.Options) > 0 {
						answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
					}
					continue
				}
				select {
				case ans := <-ansCh:
					answers = append(answers, fmt.Sprintf("%s: %s", e.Header, ans))
				case <-time.After(5 * time.Minute):
					if len(e.Options) > 0 {
						answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
					}
				}
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
	mux.Handle("/", http.FileServer(http.FS(subFS)))
	mux.HandleFunc("/api/stream", ws.handleStream)
	mux.HandleFunc("/api/action", ws.handleAction)
	mux.HandleFunc("/api/commands", ws.handleCommands)

	srv := &http.Server{Addr: webAddr, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	fmt.Fprintf(os.Stderr, "%s listening on %s%s\n",
		Bold("yaah web"), "http://"+webAddr, Dim(" (Ctrl-C to stop)"))

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
	ctrlCh := make(chan types.CtrlMsg, 64)
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
