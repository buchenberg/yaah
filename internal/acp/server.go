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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// ProtocolVersion is the ACP major version this server speaks.
const ProtocolVersion = 1

// ErrInvalidCursor is the sentinel the ListSessions callback returns
// (possibly wrapped) for a malformed pagination cursor. session/list
// surfaces it as invalid params: the spec says agents SHOULD error on an
// invalid cursor rather than silently restart the list.
var ErrInvalidCursor = errors.New("invalid cursor")

// ErrNoSession is the sentinel the DeleteSession callback returns for an
// unknown session id. session/delete succeeds silently for those
// (deleting an already-deleted or never-created session is a no-op);
// every other error is a real store failure and is reported.
var ErrNoSession = errors.New("unknown session")

// defaultPermissionTimeout bounds how long a session/request_permission
// or elicitation/create wait for the client when PermissionTimeout is
// not set (the same bound approval.go and elicitation.go apply).
const defaultPermissionTimeout = 5 * time.Minute

// Session is the narrow capability the ACP server needs from an agent
// session. *agentSession in cmd/yaah satisfies this interface.
type Session interface {
	// RunPrompt runs one turn. The bool reports whether the answer was
	// streamed through the view.
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	// TurnBudgetExhausted reports whether the last Run ended because a
	// request budget (soft tool-turn cap or hard iteration limit) ran
	// out rather than the model choosing to finish. session/prompt maps
	// it to stopReason max_turn_requests.
	TurnBudgetExhausted() bool
	SetView(agent.View)
	SetCtrlCh(chan<- control.Msg)
	SetApproveCallFn(func(callID, name, args string) bool)
	// AgentSessionID returns the id of the underlying persisted
	// conversation (the memory-DB session id). The ACP server exposes
	// it as the ACP session id, so a later session/load in a fresh
	// process names the same conversation.
	AgentSessionID() string
	// History returns the session's conversation state for replay on
	// session/load (the conversation the restored loop carries).
	History() []types.Message
	// SetModel switches the provider and model subsequent turns use.
	// An in-flight turn finishes on the model it started with.
	SetModel(provider, model string)
	// Mode reports the ACP session mode ("build" or "plan").
	Mode() string
	// SetMode switches the ACP session mode. An in-flight turn finishes on
	// the mode it started with; the next turn uses the new mode.
	SetMode(mode string) error
	// ProviderName and ModelName report the current selection, which the
	// ACP server renders as the model picker's currentValue.
	ProviderName() string
	ModelName() string
	// WorldStatus reports the session's workspace/dispatch-world posture;
	// the zero value is omitted from session/new and session/load results.
	WorldStatus() worldinfo.Status
	Close()
	ToolReg() *tools.Registry
}

// BuildOptions are the per-session inputs the ACP server passes to the
// session factory. Every session gets its own cwd (the protocol's
// working directory), its own client-declared MCP servers, and either a
// fresh conversation (empty ResumeID) or the persisted one named by
// ResumeID (unknown ids fail the build).
type BuildOptions struct {
	ResumeID   string
	Cwd        string
	MCPServers []McpServerRef
}

// SessionFactory builds one underlying agent session. It is invoked
// once per session/new or session/load; several sessions may coexist in
// one process, each with its own cwd.
type SessionFactory func(BuildOptions) (Session, error)

// Server is an ACP v1 agent speaking newline-delimited JSON-RPC 2.0
// over stdin/stdout. All diagnostics go to the logf sink so stdout
// stays clean for protocol JSON.
type Server struct {
	version    string
	logf       func(format string, args ...any)
	stdin      io.Reader
	stdout     io.Writer
	newSession SessionFactory

	// catalog, when non-nil, lists the models a client may switch
	// between. It backs the model picker: session/new and session/load
	// advertise its contents as configOptions, and
	// session/set_config_option validates the requested value against it
	// and echoes the recomputed set. Nil or empty means no picker is
	// advertised at all (configOptions is omitted) — session builds never
	// fail because discovery is unavailable. Options is called per
	// request, so a later provider switch is reflected without
	// restarting the server. Wired via NewServer; see ModelCatalog.
	catalog ModelCatalog

	// ListSessions, when non-nil, backs session/list
	// (sessionCapabilities.list). It receives the canonical absolute cwd
	// filter ("" matches any cwd) and the opaque cursor from the request,
	// applies the page size itself, and returns one page plus the
	// next-page cursor ("" = last page). A wrapped ErrInvalidCursor is
	// surfaced as invalid params; other errors as internal errors.
	ListSessions func(cwdFilter, cursor string) ([]SessionInfo, string, error)

	// DeleteSession, when non-nil, backs session/delete
	// (sessionCapabilities.delete). A wrapped ErrNoSession marks an
	// unknown id, which succeeds silently per spec; other errors are
	// internal errors and keep the session listed.
	DeleteSession func(id string) error

	// AutoAnswerQuestions, when true, answers question-tool prompts
	// with the first option. Used as the fallback when the client did
	// not advertise elicitation.form or no connection is live (the
	// connected-client path routes the question modal back
	// interactively). Default true.
	AutoAnswerQuestions bool

	// AutoContinue, when true, answers control.Continue messages with
	// "continue" when max iterations are reached. Default true.
	AutoContinue bool

	// PermissionTimeout bounds how long session/request_permission
	// waits for the client's decision. Default 5 minutes.
	PermissionTimeout time.Duration

	// conn is the live connection the server is serving over. Set once
	// when Run starts; server-initiated requests
	// (session/request_permission, elicitation/create) flow through it.
	// Nil means no routing is possible (never started, or a direct
	// dispatch in tests without a connection).
	conn *connection

	// clientCaps is the capability set the client advertised during
	// initialize; nil until then. It gates the elicitation path.
	clientCaps *ClientCaps

	// mu guards session identity, run state, and the pending-request
	// map. The agent session itself has its own locking.
	mu sync.Mutex

	sessions   map[string]*sessionState
	pendingReq map[string]chan clientResponse
	nextReqID  int
}

// sessionState is one ACP session: the underlying agent session, the
// working directory it was created with, and the single prompt turn it
// may have in flight. The ACP session id is the underlying persisted
// conversation id, so session/load can re-attach in a fresh process.
type sessionState struct {
	id   string
	sess Session
	cwd  string
	run  *runningPrompt
	// ctrl is the control channel of the prompt currently running in
	// this session. The question-tool handler installed on sess reads
	// it so prompts never cross sessions.
	ctrl chan control.Msg
	// view is the prompt's event translator. It remembers which tool
	// calls were already announced by a permission request, so the
	// later execution update reuses the same toolCallId.
	view *ViewWithWrite

	// prunCtx is the context of the prompt currently running in this
	// session (nil outside a turn). Server-initiated requests that must
	// abandon with the turn — elicitation for question prompts — join
	// on it the way the permission bridge does.
	prunCtx context.Context

	// permAllow / permDeny hold this session's allow_always /
	// reject_always permission decisions, keyed by tool name. Per
	// session, never persisted: a fresh session re-asks.
	permAllow map[string]bool
	permDeny  map[string]bool

	// compactionSeq fuels the agent-owned compaction ids (cmp-<n>)
	// handed to each prompt's view. It lives here — session lifetime,
	// like the per-session permission decisions — because a fresh View
	// is built for every prompt turn and the id namespace must not
	// reset (a repeated compactionId patches the earlier card).
	compactionSeq atomic.Int64
}

// runningPrompt tracks the single in-flight turn. cancel aborts it;
// done joins the prompt goroutine — teardown joins (bounded by
// stopRunJoinTimeout) so close() cannot race a still-unwinding run,
// whose unwind touches state that teardown destroys.
type runningPrompt struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewServer creates an ACP server. build constructs one underlying
// agent session per session/new or session/load. logf receives
// diagnostics (may be nil). catalog supplies the models the client may
// pick from; it may be nil, which leaves the model picker off.
func NewServer(build SessionFactory, version string, logf func(format string, args ...any), catalog ModelCatalog) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		version:             version,
		logf:                logf,
		catalog:             catalog,
		stdin:               os.Stdin,
		stdout:              os.Stdout,
		newSession:          build,
		AutoAnswerQuestions: true,
		AutoContinue:        true,
		PermissionTimeout:   5 * time.Minute,
		pendingReq:          make(map[string]chan clientResponse),
		sessions:            make(map[string]*sessionState),
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

func (s *Server) close() {
	s.mu.Lock()
	sessions := make([]*sessionState, 0, len(s.sessions))
	for _, st := range s.sessions {
		sessions = append(sessions, st)
	}
	s.sessions = make(map[string]*sessionState)
	s.mu.Unlock()
	for _, st := range sessions {
		st.sess.Close()
	}
}

// Run starts the dispatch loop and blocks until ctx is cancelled or
// stdin reaches EOF.
func (s *Server) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := &connection{w: s.stdout, logf: s.logf}
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()

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

// stopRun cancels every in-flight prompt and joins its goroutine, so
// shutdown never tears a session down while its run is still
// unwinding — RunPrompt persists state that close() destroys. Each
// join is bounded: a wedged stdout write must not hang shutdown.
func (s *Server) stopRun() {
	s.mu.Lock()
	runs := make([]*runningPrompt, 0, len(s.sessions))
	for _, st := range s.sessions {
		if st.run != nil {
			runs = append(runs, st.run)
		}
	}
	s.mu.Unlock()
	for _, r := range runs {
		if r.cancel != nil {
			r.cancel()
		}
	}
	for _, r := range runs {
		if r.done == nil {
			continue
		}
		timer := time.NewTimer(stopRunJoinTimeout)
		select {
		case <-r.done:
		case <-timer.C:
			s.logf("prompt goroutine did not unwind within %s; proceeding with teardown", stopRunJoinTimeout)
		}
		timer.Stop()
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
			// JSON-RPC 2.0: a parse error is answered with id null.
			respErr(json.RawMessage("null"), codeParseError, "parse error")
			continue
		}
		if msg.JSONRPC != "2.0" {
			s.logf("invalid request: jsonrpc = %q", msg.JSONRPC)
			id := msg.ID
			if len(id) == 0 {
				id = json.RawMessage("null")
			}
			respErr(id, codeInvalidRequest, "invalid request: jsonrpc must be \"2.0\"")
			continue
		}

		// Method empty: a response to one of our requests (routed to the
		// permission bridge) or a stray message to be dropped.
		if msg.Method == "" {
			if len(msg.ID) > 0 && (len(msg.Result) > 0 || msg.Error != nil) {
				// A response to one of our requests.
				s.resolvePending(msg)
			} else {
				id := msg.ID
				if len(id) == 0 {
					id = json.RawMessage("null")
				}
				respErr(id, codeInvalidRequest, "invalid request: method is required")
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
			// Capability-driven behavior is resolved from the stored set:
			// elicitation.form routes question prompts to elicitation/
			// create; nothing else gates yet.
			s.mu.Lock()
			s.clientCaps = params.ClientCapabilities
			s.mu.Unlock()
			// clientInfo is optional in ACP v1: a minimal client sends
			// only protocolVersion, so never dereference it unguarded.
			if params.ClientInfo != nil {
				s.logf("initialize: client %q requested protocol %d", params.ClientInfo.Name, params.ProtocolVersion)
			} else {
				s.logf("initialize: anonymous client requested protocol %d", params.ProtocolVersion)
			}
			respOK(msg.ID, InitializeResult{
				ProtocolVersion:   ProtocolVersion,
				AgentCapabilities: s.capabilities(),
				AgentInfo: ImplementationInfo{
					Name:    "yaah",
					Version: s.version,
				},
				AuthMethods: staticAuthMethods(),
			})

		case "authenticate":
			// Validate the method name against the advertised catalog.
			// Deliberately no credential check and no network call: the
			// client is only declaring which advertised method it uses,
			// and may do so before provisioning a key.
			var params AuthenticateParams
			if len(msg.Params) > 0 {
				if err := json.Unmarshal(msg.Params, &params); err != nil {
					respErr(msg.ID, codeInvalidParams, "authenticate: invalid params: "+err.Error())
					continue
				}
			}
			if !validAuthMethod(params.MethodID) {
				respErr(msg.ID, codeInvalidParams, "authenticate: unknown auth method: "+params.MethodID)
				continue
			}
			// AuthenticateResponse carries only an optional _meta.
			respOK(msg.ID, struct{}{})

		case "session/new":
			s.handleSessionNew(dispatchCtx, msg, respOK, respErr)

		case "session/prompt":
			s.handlePrompt(msg, dispatchCtx, c, sendUpdate, respOK, respErr)

		case "session/load":
			s.handleSessionLoad(dispatchCtx, msg, sendUpdate, respOK, respErr)

		case "session/resume":
			s.handleSessionResume(msg, respOK, respErr)

		case "session/list":
			s.handleSessionList(msg, respOK, respErr)

		case "session/close":
			s.handleSessionClose(msg, respOK, respErr)

		case "session/delete":
			s.handleSessionDelete(msg, respOK, respErr)

		case "session/set_mode":
			s.handleSetMode(msg, sendUpdate, respOK, respErr)

		case "session/set_config_option":
			s.handleSetConfigOption(dispatchCtx, msg, respOK, respErr)

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
