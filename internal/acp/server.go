package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

// ProtocolVersion is the ACP major version this server speaks.
const ProtocolVersion = 1

// Session is the narrow capability the ACP server needs from an agent
// session. *agentSession in cmd/yaah satisfies this interface.
type Session interface {
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	SetView(agent.View)
	SetCtrlCh(chan<- control.Msg)
	GetCtrlCh() chan<- control.Msg
	SetApproveFn(func(name, args string) bool)
	Close()
	ToolReg() *tools.Registry
}

// SessionFactory lazily builds the underlying agent session. It is
// invoked once, on the first session/new, so the client's cwd can take
// effect before construction (spec: the session cwd MUST be used for
// the session regardless of where the subprocess was spawned).
type SessionFactory func() (Session, error)

// Server is an ACP v1 agent speaking newline-delimited JSON-RPC 2.0
// over stdin/stdout. All diagnostics go to the logf sink so stdout
// stays clean for protocol JSON.
type Server struct {
	version    string
	logf       func(format string, args ...any)
	stdin      io.Reader
	stdout     io.Writer
	newSession SessionFactory

	// AutoAnswerQuestions, when true, answers question-tool prompts
	// with the first option. ACP has no interactive question surface in
	// Tier 1 (elicitation is a future capability). Default true.
	AutoAnswerQuestions bool

	// AutoContinue, when true, answers control.Continue messages with
	// "continue" when max iterations are reached. Default true.
	AutoContinue bool

	// PermissionTimeout bounds how long session/request_permission
	// waits for the client's decision. Default 5 minutes.
	PermissionTimeout time.Duration

	// mu guards session identity, run state, and the pending-request
	// map. The agent session itself has its own locking.
	mu sync.Mutex

	sess       Session
	sessionID  string
	run        *runningPrompt
	pendingReq map[string]chan clientResponse
	nextReqID  int
}

// runningPrompt tracks the single in-flight turn. cancel aborts it;
// done joins the prompt goroutine — teardown joins (bounded by
// stopRunJoinTimeout) so close() cannot race a still-unwinding run,
// whose unwind touches state that teardown destroys.
type runningPrompt struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewServer creates an ACP server. build constructs the underlying
// agent session lazily, once, after session/new has applied the
// client's cwd. logf receives diagnostics (may be nil).
func NewServer(build SessionFactory, version string, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		version:             version,
		logf:                logf,
		stdin:               os.Stdin,
		stdout:              os.Stdout,
		newSession:          build,
		AutoAnswerQuestions: true,
		AutoContinue:        true,
		PermissionTimeout:   5 * time.Minute,
		pendingReq:          make(map[string]chan clientResponse),
		run:                 &runningPrompt{},
	}
}

// connection serializes writes to stdout and issues server-initiated
// requests (session/request_permission).
type connection struct {
	mu   sync.Mutex
	w    io.Writer
	logf func(format string, args ...any)
}

func (c *connection) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		c.logf("marshal error: %v", err)
		return
	}
	c.mu.Lock()
	_, _ = fmt.Fprintln(c.w, string(data))
	c.mu.Unlock()
}

// sendRequest writes a JSON-RPC request with the given numeric ID.
func (c *connection) sendRequest(id int, method string, params any) {
	data, err := json.Marshal(params)
	if err != nil {
		c.logf("marshal params: %v", err)
		return
	}
	c.write(Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
		Params:  data,
	})
}

// handleSessionNew applies the client's cwd once, builds the
// underlying session on first use, and issues a fresh session ID.
// A cwd that is not a usable directory rejects the request.
func (s *Server) handleSessionNew(params json.RawMessage) (Session, error) {
	var p SessionNewParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Cwd != "" {
		if info, statErr := os.Stat(p.Cwd); statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("cwd %q is not a directory", p.Cwd)
		}
		if s.sessionID != "" {
			// One underlying conversation per process: chdir mid-session
			// would silently move an established workspace. Docs record
			// the single-conversation limitation; the first cwd wins.
			s.logf("session/new cwd %q ignored: session already established", p.Cwd)
		} else if err := os.Chdir(p.Cwd); err != nil {
			return nil, fmt.Errorf("chdir %q: %w", p.Cwd, err)
		} else {
			s.logf("session cwd applied: %s", p.Cwd)
		}
	}
	if s.sess == nil {
		sess, err := s.newSession()
		if err != nil {
			return nil, err
		}
		s.sess = sess
		s.installQuestionHandler()
		s.logf("agent session built")
	}
	// One underlying conversation per process: a repeat session/new is
	// idempotent — issuing a second ID would invalidate the client's
	// existing handle while the same conversation still answers it.
	if s.sessionID == "" {
		s.sessionID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
		s.logf("session/new: %s", s.sessionID)
	} else {
		s.logf("session/new repeated: returning %s", s.sessionID)
	}
	return s.sess, nil
}

// installQuestionHandler routes question-tool prompts to the client as
// status text and auto-answers with the first option when enabled.
// Called once after the underlying session is built.
func (s *Server) installQuestionHandler() {
	if s.sess == nil {
		return
	}
	reg := s.sess.ToolReg()
	if reg == nil {
		return
	}
	qt, ok := reg.Get("question").(*tools.QuestionTool)
	if !ok {
		return
	}
	qt.Handler = func(entries []tools.QuestionEntry) []string {
		var answers []string
		for _, e := range entries {
			msg := fmt.Sprintf("❓ %s\n\n%s\n\n", e.Header, e.Question)
			for i, o := range e.Options {
				msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
			}
			if ch := s.ctrlCh(); ch != nil {
				select {
				case ch <- &control.Status{Text: msg}:
				default:
				}
			}
			if s.AutoAnswerQuestions && len(e.Options) > 0 {
				answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
			} else {
				answers = append(answers, e.Header+": ")
			}
		}
		return answers
	}
}

// ctrlCh returns the session's current control channel, or nil.
func (s *Server) ctrlCh() chan<- control.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess == nil {
		return nil
	}
	return s.sess.GetCtrlCh()
}

// close tears the underlying session down with the server.
func (s *Server) close() {
	s.mu.Lock()
	sess := s.sess
	s.sess = nil
	s.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

// Run starts the dispatch loop and blocks until ctx is cancelled or
// stdin reaches EOF.
func (s *Server) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := &connection{w: s.stdout, logf: s.logf}

	sendUpdate := func(sessionID string, update any) {
		raw, err := json.Marshal(update)
		if err != nil {
			s.logf("marshal update: %v", err)
			return
		}
		c.write(Message{
			JSONRPC: "2.0",
			Method:  "session/update",
			Params:  rawOf(updateEnvelope{SessionID: sessionID, Update: raw}),
		})
	}

	respErr := func(id json.RawMessage, code int, message string) {
		c.write(Message{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &RPCError{Code: code, Message: message},
		})
	}
	respOK := func(id json.RawMessage, result any) {
		if result == nil {
			result = json.RawMessage("{}")
		}
		c.write(Message{
			JSONRPC: "2.0",
			ID:      id,
			Result:  rawOf(result),
		})
	}

	reader := bufio.NewReaderSize(s.stdin, 1<<20)

	// done closes when the reader loop exits (EOF).
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.dispatch(ctx, reader, c, sendUpdate, respOK, respErr)
	}()

	select {
	case <-ctx.Done():
		// Signal or external cancel: join the run, tear down the
		// session, and return. The reader goroutine exits with EOF when
		// the client side closes.
		s.logf("context cancelled")
		s.stopRun()
		s.close()
		return nil
	case <-done:
		return nil
	}
}

// stopRunJoinTimeout bounds how long shutdown waits for the prompt
// goroutine. A client that stops draining stdout can wedge the last
// update writes (the run goroutine blocks joining forwardCtrl, which
// is blocked in a write); when the bound expires, teardown proceeds
// anyway — matching the pre-join behavior of cancelling and exiting
// instead of hanging the process on SIGTERM.
const stopRunJoinTimeout = 2 * time.Second

// stopRun cancels the in-flight prompt (if any) and joins its
// goroutine, so shutdown never tears the session down while the run is
// still unwinding — RunPrompt persists state that close() destroys.
// The join is bounded: a wedged stdout write must not hang shutdown.
func (s *Server) stopRun() {
	s.mu.Lock()
	cancel := s.run.cancel
	done := s.run.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		timer := time.NewTimer(stopRunJoinTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			s.logf("prompt goroutine did not unwind within %s; proceeding with teardown", stopRunJoinTimeout)
		}
	}
}

// dispatch reads and executes protocol messages until EOF.
func (s *Server) dispatch(
	dispatchCtx context.Context,
	reader *bufio.Reader,
	c *connection,
	sendUpdate updateSender,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				s.logf("read error: %v", err)
			}
			s.logf("client closed stdin")
			// Join the run before teardown so close() cannot race the
			// prompt goroutine's unwind (which persists state that
			// close() destroys).
			s.stopRun()
			s.close()
			return
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			s.logf("invalid JSON line: %v", err)
			continue
		}

		// Method empty: a response to one of our requests (routed to the
		// permission bridge) or a stray message to be dropped.
		if msg.Method == "" {
			if len(msg.ID) > 0 {
				s.resolvePending(msg)
			}
			continue
		}

		// ID empty: notification. Initialized and cancel are handled;
		// the unknown ones are dropped per spec.
		if len(msg.ID) == 0 {
			switch msg.Method {
			case "notifications/initialized":
			case "session/cancel":
				s.handleCancel(msg)
			default:
				s.logf("dropped notification %s", msg.Method)
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			var params InitializeParams
			if len(msg.Params) > 0 {
				_ = json.Unmarshal(msg.Params, &params)
			}
			s.logf("initialize: client %q requested protocol %d", params.ClientInfo.Name, params.ProtocolVersion)
			respOK(msg.ID, InitializeResult{
				ProtocolVersion:   ProtocolVersion,
				AgentCapabilities: AgentCaps{},
				AgentInfo: ImplementationInfo{
					Name:    "yaah",
					Version: s.version,
				},
				AuthMethods: []AuthMethod{},
			})

		case "authenticate":
			// Never offered (authMethods is empty); reject for clarity.
			respErr(msg.ID, codeMethodNotFound, "authenticate is not offered")

		case "session/new":
			if _, err := s.handleSessionNew(msg.Params); err != nil {
				respErr(msg.ID, codeInvalidParams, err.Error())
				continue
			}
			s.mu.Lock()
			id := s.sessionID
			s.mu.Unlock()
			respOK(msg.ID, SessionNewResult{SessionID: id})

		case "session/prompt":
			s.handlePrompt(msg, dispatchCtx, c, sendUpdate, respOK, respErr)

		case "session/load":
			respErr(msg.ID, codeInvalidParams, "loadSession is not supported: start a new session instead")

		case "session/set_mode":
			// Mode switching is not modeled yet (Tier 3); acknowledge so
			// clients that surface a mode picker do not hang.
			respOK(msg.ID, nil)

		default:
			respErr(msg.ID, codeMethodNotFound, "method not found: "+msg.Method)
		}
	}
}

// canonicalID canonicalizes a JSON-RPC id (number or string) into a
// stable map key for pending-request routing.
func (s *Server) canonicalID(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return string(id)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(id)
	}
	return string(b)
}

// handleCancel cancels the in-flight turn if it belongs to the given
// session, ignoring malformed params — cancel is idempotent.
func (s *Server) handleCancel(msg Message) {
	var p CancelParams
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.cancel == nil {
		s.logf("cancel ignored: no prompt in flight")
		return
	}
	if p.SessionID != "" && p.SessionID != s.sessionID {
		s.logf("cancel ignored: unknown session %q", p.SessionID)
		return
	}
	s.logf("cancelling prompt turn")
	s.run.cancel()
}

// ensureIdle rejects overlapping prompts. Caller holds s.mu.
func (s *Server) ensureIdle() bool {
	return s.run.cancel == nil
}

// handlePrompt runs one prompt turn. Per ACP v1, the request stays
// open until the turn ends and is then answered with a stopReason;
// session/update notifications stream before the response.
func (s *Server) handlePrompt(
	msg Message,
	rootCtx context.Context,
	c *connection,
	sendUpdate updateSender,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p PromptParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			respErr(msg.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	if p.SessionID == "" {
		respErr(msg.ID, codeInvalidParams, "sessionId is required")
		return
	}

	s.mu.Lock()
	if s.sessionID == "" || p.SessionID != s.sessionID {
		s.mu.Unlock()
		respErr(msg.ID, codeInvalidParams, "unknown session id (call session/new first)")
		return
	}
	if !s.ensureIdle() {
		s.mu.Unlock()
		respErr(msg.ID, codeBusy, "a prompt is already in flight for this session")
		return
	}
	sess := s.sess
	promptText, renderErr := renderPrompt(p.Prompt)
	if renderErr != nil {
		s.mu.Unlock()
		respErr(msg.ID, codeInvalidParams, renderErr.Error())
		return
	}
	if promptText == "" {
		s.mu.Unlock()
		respErr(msg.ID, codeInvalidParams, "prompt must contain at least one non-empty content block")
		return
	}

	promptCtx, cancel := context.WithCancel(rootCtx)
	runDone := make(chan struct{})
	s.run = &runningPrompt{cancel: cancel, done: runDone}
	s.mu.Unlock()

	sess.SetView(NewViewWithWrite(sendUpdate, p.SessionID))
	ctrlCh := make(chan control.Msg, 64)
	sess.SetCtrlCh(ctrlCh)
	sess.SetApproveFn(func(name, args string) bool {
		return s.requestPermission(p.SessionID, name, args, promptCtx, c)
	})

	// forwardCtrl consumes control-plane sends during the run (todo
	// writes use blocking sends; the channel must always drain). After
	// the run returns, close(flush) makes it deliver one final batch of
	// queued messages; the run goroutine then joins on ctrlDone so the
	// session/prompt response can never overtake the last update.
	// ctrlDone is joined before the response; runDone is joined by
	// teardown (stopRun) so shutdown never tears the session down under
	// a still-unwinding prompt.
	ctrlDone := make(chan struct{})
	flush := make(chan struct{})
	go s.forwardCtrl(promptCtx, ctrlCh, p.SessionID, sendUpdate, flush, ctrlDone)

	go func() {
		defer close(runDone)
		_, _, runErr := sess.RunPrompt(promptCtx, promptText)

		stopReason := StopEndTurn
		switch {
		case runErr == nil:
			s.logf("prompt completed")
		case errors.Is(runErr, context.Canceled):
			stopReason = StopCancelled
			s.logf("prompt cancelled")
		default:
			s.logf("prompt error: %v", runErr)
			stopReason = StopRefusal
		}

		// Free the single-flight slot. All control producers that run
		// inside RunPrompt have returned, so anything already queued is
		// deliverable: closing flush makes forwardCtrl drain the rest of
		// its queue without regard to cancellation, and joining on
		// ctrlDone guarantees every queued update is on the wire before
		// the response. Only then is the prompt context cancelled. The
		// join handle stays in s.run so teardown can still join.
		s.mu.Lock()
		s.run = &runningPrompt{done: runDone}
		s.mu.Unlock()
		close(flush)
		<-ctrlDone
		cancel()

		respOK(msg.ID, PromptResult{StopReason: stopReason})
	}()
}

// renderPrompt folds ACP content blocks into the flat text prompt the
// underlying agent session speaks. resource and resource_link blocks
// are preserved as inline text so nothing is silently dropped; image
// and audio (which we do not advertise) degrade to a visible note.
func renderPrompt(blocks []ContentBlock) (string, error) {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "resource_link":
			link := b.URI
			if b.Name != "" {
				link += " (" + b.Name + ")"
			}
			parts = append(parts, "[resource link: "+link+"]")
		case "resource":
			if b.Resource != nil {
				if b.Resource.Text != "" {
					parts = append(parts, b.Resource.Text)
				} else {
					parts = append(parts, "[resource: "+b.Resource.URI+"]")
				}
			} else {
				parts = append(parts, "[embedded resource]")
			}
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s content not supported by this agent]", b.Type))
		default:
			parts = append(parts, fmt.Sprintf("[unsupported content block: %s]", b.Type))
		}
	}
	var out strings.Builder
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(p)
	}
	return out.String(), nil
}
