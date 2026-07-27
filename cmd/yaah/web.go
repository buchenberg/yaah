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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	v := &sseView{w: w}
	ctrlCh := make(chan types.CtrlMsg, 64)
	done := make(chan struct{})

	ws.mu.Lock()
	ws.view = v
	ws.sseDone = done
	ws.mu.Unlock()

	ws.sess.SetView(v)
	ws.sess.SetCtrlCh(ctrlCh)

	streamCtx, streamCancel := context.WithCancel(r.Context())
	defer streamCancel()

	go forwardCtrl(streamCtx, ctrlCh, v, ws.am, &ws.idGen)

	<-r.Context().Done()

	ws.mu.Lock()
	ws.view = nil
	close(done)
	ws.mu.Unlock()

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
