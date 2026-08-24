package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// HTTPServer exposes an MCP Server over both Streamable HTTP and the
// legacy HTTP+SSE transports (MCP spec 2024-11-05). The legacy SSE
// transport is what Kilo's MCP client uses by default: the client
// opens GET /mcp, the server returns text/event-stream, and the
// first event names the URL the client should POST JSON-RPC messages
// to. Streamable HTTP (POST /mcp with Mcp-Session-Id) also works.
//
// Sessions are tracked in two forms: a 16-byte hex session ID issued
// after initialize (Streamable HTTP via Mcp-Session-Id), and the
// per-stream session ID announced in the SSE endpoint event
// (HTTP+SSE). Both share the same backing store.
//
// DELETE /mcp closes the named session.
type HTTPServer struct {
	server *Server
	addr   string

	// authToken, when non-empty, requires every MCP request to carry
	// "Authorization: Bearer <token>" (or a ?token= query parameter).
	// /health stays open for readiness probes.
	authToken string
	// allowUnknownSessions controls whether POSTs with an unrecognized
	// Mcp-Session-Id — or non-initialize POSTs without one — are
	// accepted. Strict by default; serve mode enables it so client
	// reconnects survive server restarts.
	allowUnknownSessions bool

	mu       sync.RWMutex
	sessions map[string]struct{}
	streams  map[string]*sseStream // keyed by session ID

	httpServer *http.Server
}

// sseStream is an open server-sent-event connection to one client.
// It serializes writes so concurrent messages don't interleave.
type sseStream struct {
	sessionID string
	flusher   http.Flusher
	writer    io.Writer
	mu        sync.Mutex
	closed    bool
}

// NewHTTPServer wraps an MCP Server for HTTP transport on the given
// listen address (e.g. "127.0.0.1:7333").
func NewHTTPServer(s *Server, addr string) *HTTPServer {
	return &HTTPServer{
		server:   s,
		addr:     addr,
		sessions: map[string]struct{}{},
		streams:  map[string]*sseStream{},
	}
}

// Addr returns the configured listen address. Useful when port 0
// is passed and the caller wants to discover the chosen port.
func (h *HTTPServer) Addr() string {
	return h.addr
}

// SetAuthToken enables bearer-token authentication. An empty token
// disables authentication (not recommended for non-loopback binds).
func (h *HTTPServer) SetAuthToken(token string) {
	h.authToken = token
}

// SetAllowUnknownSessions controls whether requests with unrecognized
// or missing session IDs are accepted. See HTTPServer.allowUnknownSessions.
func (h *HTTPServer) SetAllowUnknownSessions(allow bool) {
	h.allowUnknownSessions = allow
}

// authorized reports whether r carries a valid auth token. When no
// token is configured every request is authorized.
func (h *HTTPServer) authorized(r *http.Request) bool {
	if h.authToken == "" {
		return true
	}
	var presented string
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		presented = strings.TrimPrefix(auth, "Bearer ")
	} else if t := r.URL.Query().Get("token"); t != "" {
		presented = t
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.authToken)) == 1
}

func (h *HTTPServer) unauthorized(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(os.Stderr, "[yaah-mcp] %s %s from %s: unauthorized\n", r.Method, r.URL.Path, r.RemoteAddr)
	http.Error(w, "unauthorized: missing or invalid bearer token", http.StatusUnauthorized)
}

// Start binds the configured address and serves HTTP until the
// server is shut down via ctx cancellation, Shutdown, or a fatal
// listen error.
//
// The listener is bound explicitly before Serve so the caller can
// detect readiness: once Start prints the "listening" line the port
// is guaranteed to accept connections. This avoids ERR_CONNECTION_REFUSED
// races during server restarts where clients reconnect before Listen
// has completed.
//
// On ctx cancellation Start calls Shutdown to gracefully drain
// active connections, then returns nil.
func (h *HTTPServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", h.handleMCP)
	mux.HandleFunc("/mcp/messages", h.handlePost)
	mux.HandleFunc("/messages", h.handlePost)
	mux.HandleFunc("/health", h.handleHealth)

	// Bind the listener explicitly so we can signal readiness before
	// entering the blocking Serve loop. This eliminates the window
	// between process start and port binding that causes
	// ERR_CONNECTION_REFUSED on client reconnects.
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", h.addr, err)
	}

	// Store the actual address (important when port 0 is requested).
	h.addr = ln.Addr().String()

	h.httpServer = &http.Server{
		Addr:              h.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Readiness signal: the port is bound, connections will succeed.
	fmt.Fprintf(os.Stderr, "[yaah-mcp] listening on %s\n", h.addr)

	// Serve blocks until Shutdown or Close is called (or a fatal
	// listener error occurs).
	errCh := make(chan error, 1)
	go func() {
		if err := h.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for either context cancellation (shutdown signal) or a
	// fatal serve error.
	select {
	case <-ctx.Done():
		// Graceful shutdown: stop accepting new connections, wait
		// for in-flight requests to complete, then return.
		fmt.Fprintf(os.Stderr, "[yaah-mcp] shutting down...\n")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.httpServer.Shutdown(shutdownCtx)
		<-errCh // wait for Serve to return
		return nil
	case err := <-errCh:
		return fmt.Errorf("serve MCP HTTP: %w", err)
	}
}

// Shutdown gracefully shuts down the HTTP server without interrupting
// active connections. It waits for in-flight requests to complete or
// the context to expire, whichever comes first.
func (h *HTTPServer) Shutdown(ctx context.Context) error {
	if h.httpServer == nil {
		return nil
	}
	return h.httpServer.Shutdown(ctx)
}

// Close immediately closes all active connections and the listener.
// Prefer Shutdown for graceful connection draining.
func (h *HTTPServer) Close() error {
	if h.httpServer == nil {
		return nil
	}
	return h.httpServer.Close()
}

// handleHealth answers GET with a small JSON status body. Useful for
// readiness probes and for the developer to confirm the server is up
// without speaking JSON-RPC.
func (h *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"name":   h.server.name,
	})
}

// handleMCP dispatches POST/GET/DELETE. GET opens an SSE stream
// (legacy HTTP+SSE transport) and announces the message endpoint;
// POST processes JSON-RPC messages and also pushes responses to any
// open SSE stream for the same session; DELETE closes the session.
func (h *HTTPServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		h.unauthorized(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleGetSSE(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetSSE opens a long-lived text/event-stream response and
// announces the URL the client should POST JSON-RPC messages to.
// The stream is held open until the client disconnects or the
// server shuts down; the server periodically sends a keep-alive
// comment to defeat intermediary timeouts.
func (h *HTTPServer) handleGetSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Reject requests that don't accept SSE. Some clients send
	// "text/event-stream" alone; others send the broader MCP header.
	accept := r.Header.Get("Accept")
	if accept != "" && !containsToken(accept, "text/event-stream") {
		http.Error(w, "this endpoint requires Accept: text/event-stream", http.StatusNotAcceptable)
		return
	}

	sid := newSessionID()
	h.registerSession(sid)
	defer h.unregisterSession(sid)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.Header().Set("Mcp-Session-Id", sid)
	w.WriteHeader(http.StatusOK)

	stream := &sseStream{
		sessionID: sid,
		flusher:   flusher,
		writer:    w,
	}
	h.registerStream(sid, stream)
	defer h.unregisterStream(sid)

	// Announce the message endpoint. The MCP spec recommends an
	// absolute or root-relative path; the client resolves it
	// against the request URL.
	endpoint := "/mcp/messages?sessionId=" + sid
	fmt.Fprintf(os.Stderr, "[yaah-mcp] GET /mcp from %s: opened SSE stream sid=%s endpoint=%s\n", r.RemoteAddr, sid, endpoint)
	// A POST can race us as soon as the stream is registered — take the
	// stream mutex so the announcement cannot interleave with pushToStream.
	stream.mu.Lock()
	err := writeSSEEvent(w, flusher, "endpoint", endpoint)
	stream.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[yaah-mcp] SSE write endpoint failed: %v\n", err)
		return
	}

	// Hold the stream open. Per spec we send a keep-alive comment
	// every 15s to keep idle proxies from closing the connection.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			stream.mu.Lock()
			stream.closed = true
			stream.mu.Unlock()
			fmt.Fprintf(os.Stderr, "[yaah-mcp] SSE stream sid=%s closed by client\n", sid)
			return
		case <-ticker.C:
			stream.mu.Lock()
			if stream.closed {
				stream.mu.Unlock()
				return
			}
			_, err := fmt.Fprint(stream.writer, ": keep-alive\n\n")
			stream.mu.Unlock()
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handlePost reads one or more JSON-RPC messages from the request body,
// dispatches each, and writes the response. Notifications (messages
// without an ID) produce no output.
func (h *HTTPServer) handlePost(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		h.unauthorized(w, r)
		return
	}
	if r.Header.Get("Content-Type") != "" && !isJSONContentType(r.Header.Get("Content-Type")) {
		fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: unsupported content type %q from %s\n", r.Header.Get("Content-Type"), r.RemoteAddr)
		http.Error(w, "unsupported content type: "+r.Header.Get("Content-Type"), http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: read body error from %s: %v\n", r.RemoteAddr, err)
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: empty body from %s\n", r.RemoteAddr)
		http.Error(w, "empty request body", http.StatusBadRequest)
		return
	}
	fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp from %s: %d bytes session=%q\n", r.RemoteAddr, len(body), r.Header.Get("Mcp-Session-Id"))

	messages, err := decodeRequest(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: parse error: %v\n", err)
		http.Error(w, "parse json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate session: initialize is allowed without a session, but
	// after a session is established the client MUST echo the ID.
	// The HTTP+SSE transport carries the session in the URL query
	// string; the Streamable HTTP transport uses the header.
	wantSession := r.Header.Get("Mcp-Session-Id")
	if wantSession == "" {
		wantSession = r.URL.Query().Get("sessionId")
	}
	if wantSession != "" && !h.sessionExists(wantSession) {
		if !h.allowUnknownSessions {
			fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: rejecting unknown session %q from %s\n", wantSession, r.RemoteAddr)
			http.Error(w, "unknown session: "+wantSession, http.StatusNotFound)
			return
		}
		// Auto-registration makes server restarts transparent to the
		// client (no reconnect handshake needed). Only enabled when
		// the operator opts in — sessions are bookkeeping, and this
		// mode trusts the auth token as the real gate.
		h.registerSession(wantSession)
		fmt.Fprintf(os.Stderr, "[yaah-mcp] POST /mcp: auto-registered stale session %s (server restart?)\n", wantSession)
	}
	if wantSession == "" && !h.allowUnknownSessions {
		hasInit := false
		for _, msg := range messages {
			if isInitialize(msg) {
				hasInit = true
				break
			}
		}
		if !hasInit {
			http.Error(w, "missing Mcp-Session-Id", http.StatusNotFound)
			return
		}
	}

	responses := make([]JSONRPCMessage, 0, len(messages))
	for _, msg := range messages {
		if isInitialize(msg) && wantSession == "" {
			sid := newSessionID()
			h.registerSession(sid)
			w.Header().Set("Mcp-Session-Id", sid)
			wantSession = sid
		}

		// `initialize` is a request even when sent with id=0 (some
		// MCP clients do this in the legacy HTTP+SSE transport). All
		// other methods with id=0 are treated as notifications
		// per the JSON-RPC convention.
		if msg.ID == nil && !isInitialize(msg) {
			if _, derr := h.server.dispatch(r.Context(), msg); derr != nil {
				fmt.Fprintf(os.Stderr, "mcp notification dispatch error: %v\n", derr)
			}
			continue
		}

		result, rpcErr := h.server.dispatch(r.Context(), msg)
		resp := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
		}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		responses = append(responses, resp)

		// Also push the response to any open SSE stream for the
		// session, so HTTP+SSE clients receive it via the stream
		// rather than the HTTP body.
		if wantSession != "" {
			if err := h.pushToStream(wantSession, resp); err != nil {
				fmt.Fprintf(os.Stderr, "[yaah-mcp] push to stream sid=%s failed: %v\n", wantSession, err)
			}
		}
	}

	// Determine response shape: single message → single object;
	// multiple → JSON array. Per spec, batch responses use array form.
	if len(responses) == 0 {
		// All notifications; respond 202 Accepted with no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(messages) == 1 {
		_ = json.NewEncoder(w).Encode(responses[0])
		return
	}
	_ = json.NewEncoder(w).Encode(responses)
}

// handleDelete closes the named session.
func (h *HTTPServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		http.Error(w, "missing Mcp-Session-Id header", http.StatusBadRequest)
		return
	}
	if !h.sessionExists(sid) {
		http.Error(w, "unknown session: "+sid, http.StatusNotFound)
		return
	}
	h.unregisterSession(sid)
	w.WriteHeader(http.StatusNoContent)
}

// decodeRequest parses the request body as either a single JSON-RPC
// message or a batch (array) of them.
func decodeRequest(body []byte) ([]JSONRPCMessage, error) {
	trimmed := skipWhitespace(body)
	if len(trimmed) == 0 || trimmed[0] == '{' {
		var msg JSONRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return nil, fmt.Errorf("decode single JSON-RPC message: %w", err)
		}
		return []JSONRPCMessage{msg}, nil
	}
	if trimmed[0] == '[' {
		var batch []JSONRPCMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("decode JSON-RPC batch: %w", err)
		}
		return batch, nil
	}
	return nil, fmt.Errorf("request body must be a JSON object or array")
}

func skipWhitespace(b []byte) []byte {
	for i, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b[i:]
		}
	}
	return nil
}

func isJSONContentType(ct string) bool {
	// Accept "application/json" and "application/json; charset=utf-8".
	for i, c := range ct {
		if c == ';' || c == ' ' {
			return hasJSONPrefix(ct[:i])
		}
	}
	return hasJSONPrefix(ct)
}

func hasJSONPrefix(s string) bool {
	const want = "application/json"
	if len(s) < len(want) {
		return false
	}
	for i := 0; i < len(want); i++ {
		if s[i] != want[i] {
			return false
		}
	}
	return true
}

func isInitialize(msg JSONRPCMessage) bool {
	return msg.Method == "initialize"
}

func (h *HTTPServer) sessionExists(sid string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.sessions[sid]
	return ok
}

func (h *HTTPServer) registerSession(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sid] = struct{}{}
}

func (h *HTTPServer) unregisterSession(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sid)
	if s, ok := h.streams[sid]; ok {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		delete(h.streams, sid)
	}
}

func (h *HTTPServer) registerStream(sid string, stream *sseStream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streams[sid] = stream
}

func (h *HTTPServer) unregisterStream(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.streams[sid]; ok {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		delete(h.streams, sid)
	}
}

// pushToStream writes resp as an SSE "message" event to the named
// session's open SSE stream, if any. It is a no-op when no stream
// is open. Returns an error if the stream is open but the write
// fails; the caller can ignore it.
func (h *HTTPServer) pushToStream(sid string, resp JSONRPCMessage) error {
	h.mu.RLock()
	stream, ok := h.streams[sid]
	h.mu.RUnlock()
	if !ok {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return nil
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal SSE message for session %s: %w", sid, err)
	}
	if _, err := fmt.Fprintf(stream.writer, "event: message\ndata: %s\n\n", body); err != nil {
		stream.closed = true
		return fmt.Errorf("write SSE message for session %s: %w", sid, err)
	}
	stream.flusher.Flush()
	return nil
}

// writeSSEEvent writes a single named SSE event and flushes.
func writeSSEEvent(w io.Writer, flusher http.Flusher, event, data string) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("write SSE event %q: %w", event, err)
	}
	flusher.Flush()
	return nil
}

// containsToken reports whether the comma-separated Accept header
// contains the given token. Case-insensitive per RFC 7231.
func containsToken(accept, token string) bool {
	for _, t := range strings.Split(accept, ",") {
		trimmed := strings.TrimSpace(strings.SplitN(t, ";", 2)[0])
		if strings.EqualFold(trimmed, token) || trimmed == "*/*" {
			return true
		}
	}
	return false
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a deterministic-but-unique ID under extreme
		// entropy exhaustion; collisions would only cause 404s.
		return fmt.Sprintf("yaah-%d", nextSessionFallback())
	}
	return hex.EncodeToString(b[:])
}

var sessionFallbackMu sync.Mutex
var sessionFallbackN uint64

func nextSessionFallback() uint64 {
	sessionFallbackMu.Lock()
	defer sessionFallbackMu.Unlock()
	sessionFallbackN++
	return sessionFallbackN
}
