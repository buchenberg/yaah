package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/cwdpath"
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

// canonicalCwd validates the client's working directory per ACP: it
// MUST be absolute, and it MUST be used for the session. The canonical
// form (absolute, symlink-resolved) is what the session factory gets and
// what a later session/load's cwd is compared against.
func canonicalCwd(cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("cwd is required")
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd %q must be an absolute path", cwd)
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", cwd)
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %w", cwd, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
}

// resolveMCPRefs checks the client-declared MCP servers and turns each
// command into an absolute path. Only stdio is supported (the spec
// requires it of every agent; HTTP/SSE would need mcpCapabilities
// first). ACP asks clients for an absolute command, but real clients
// commonly send a bare executable name ("npx", "uvx") or a path
// relative to the session cwd; resolve those here instead of rejecting
// an otherwise usable server.
func resolveMCPRefs(refs []McpServerRef, cwd string) ([]McpServerRef, error) {
	out := make([]McpServerRef, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for i, ref := range refs {
		if ref.Name == "" {
			return nil, fmt.Errorf("mcpServers[%d].name is required", i)
		}
		if seen[ref.Name] {
			return nil, fmt.Errorf("mcpServers: duplicate name %q", ref.Name)
		}
		seen[ref.Name] = true
		if ref.Type != "" && ref.Type != "stdio" {
			return nil, fmt.Errorf("mcpServers[%d]: unsupported transport %q", i, ref.Type)
		}
		if ref.Command == "" {
			return nil, fmt.Errorf("mcpServers[%d].command is required", i)
		}
		command, err := resolveMCPCommand(ref.Command, cwd)
		if err != nil {
			return nil, fmt.Errorf("mcpServers[%d].command: %w", i, err)
		}
		ref.Command = command
		out = append(out, ref)
	}
	return out, nil
}

// resolveMCPCommand resolves one stdio command. Absolute paths pass
// through (cleaned); a name is looked up on PATH; a relative path with
// a separator is resolved against the session cwd.
func resolveMCPCommand(command, cwd string) (string, error) {
	resolved := command
	if !filepath.IsAbs(resolved) {
		if strings.ContainsAny(resolved, `/\`) {
			resolved = filepath.Join(cwd, resolved)
		} else if p, err := exec.LookPath(resolved); err == nil {
			resolved = p
			// A relative PATH entry (e.g. ".") can yield a relative
			// result, which filepath.Abs below would then resolve
			// against the process cwd — when the session cwd differs,
			// silently the wrong file. Resolve it against the session
			// cwd like the other relative forms.
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(cwd, resolved)
			}
		} else {
			return "", fmt.Errorf("%q is not on PATH", command)
		}
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("%q: %w", command, err)
	}
	return filepath.Clean(abs), nil
}

// buildSession builds one underlying session and registers it under
// its persisted conversation id. expectedID, when non-empty, pins the
// id the factory must resume; a mismatch closes the half-built session
// instead of exposing it under an id the client never negotiated.
func (s *Server) buildSession(opts BuildOptions, expectedID string) (*sessionState, error) {
	sess, err := s.newSession(opts)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("session factory returned a nil session")
	}
	id := sess.AgentSessionID()
	if id == "" {
		sess.Close()
		return nil, fmt.Errorf("session factory returned an empty session id")
	}
	if expectedID != "" && id != expectedID {
		sess.Close()
		return nil, fmt.Errorf("resumed session %q does not match requested %q", id, expectedID)
	}
	st := &sessionState{id: id, sess: sess, cwd: opts.Cwd}
	s.mu.Lock()
	if _, exists := s.sessions[id]; exists {
		s.mu.Unlock()
		sess.Close()
		return nil, fmt.Errorf("session id %q already exists", id)
	}
	s.sessions[id] = st
	s.mu.Unlock()
	s.installQuestionHandler(st)
	s.logf("agent session built: %s (cwd %s)", id, opts.Cwd)
	return st, nil
}

// handleSessionNew creates a fresh conversation for the requested cwd
// and returns its ACP id. Every call builds a new session: session ids
// are unique per created session, and independent sessions may coexist
// in one process. ctx bounds the model-catalog wait so a slow provider
// discovery is abandoned when the server shuts down.
func (s *Server) handleSessionNew(ctx context.Context, msg Message, respOK func(id json.RawMessage, result any), respErr func(id json.RawMessage, code int, message string)) {
	var p SessionNewParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			respErr(msg.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	cwd, err := canonicalCwd(p.Cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	mcpRefs, err := resolveMCPRefs(p.McpServers, cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	st, err := s.buildSession(BuildOptions{Cwd: cwd, MCPServers: mcpRefs}, "")
	if err != nil {
		respErr(msg.ID, codeInvalidParams, "create session: "+err.Error())
		return
	}
	respOK(msg.ID, SessionNewResult{
		SessionID:     st.id,
		ConfigOptions: buildModelConfigOptions(s.modelCatalog(ctx), st.sess.ProviderName(), st.sess.ModelName()),
		Meta:          worldMetaOf(st.sess.WorldStatus()),
	})
}

// syntheticAssistantText is the content the loop persister stores for
// an assistant message that carried only tool calls: no real text
// existed, so the row's content is a falsified tool-call summary.
// Replay skips those rows — they are conversation internals, not
// user-visible agent text.
const syntheticAssistantText = "[tool:"

// internalUserMarkers are the content prefixes of persisted user-role
// rows that carry agent-internal guidance rather than things the user
// typed or said in a follow-up: steering and loop-detection nudges
// (`[STEER] `, internal/agent/pipeline/steer.go and loopdetect.go),
// checkpoint recovery guidance after a failed or budget-exhausted turn
// (`SUPERVISOR: `, internal/agent/turn_checkpoint_loop.go), sub-agent
// conflict reports (`CONFLICT: `,
// internal/tools/conflict_tracker.go), and background sub-agent
// results delivered as follow-up rows (`[BACKGROUND <role>]`,
// cmd/yaah/wiring.go). Replay skips them like the other conversation
// internals — none of them are streamed to the client during a live
// run (they enter the model conversation, not the agent's output).
var internalUserMarkers = []string{"[STEER] ", "SUPERVISOR: ", "CONFLICT: ", "[BACKGROUND"}

func isInternalUserContent(content string) bool {
	for _, marker := range internalUserMarkers {
		if strings.HasPrefix(content, marker) {
			return true
		}
	}
	return false
}

// replayable folds one persisted conversation message into the ACP
// update variant it replays as, with the text to stream. Empty text
// means "do not replay": system rows (compaction summaries,
// post-interrupt continuations) and tool-result rows are conversation
// internals; user rows carrying agent-internal guidance (the markers
// above) never reached the client as stream content either;
// assistant rows that carry no real text are the tool-call-only
// records above.
func replayable(m types.Message) (updateKind, text string) {
	switch m.Role {
	case "user":
		if m.Content == "" || isInternalUserContent(m.Content) {
			return "", ""
		}
		return UpdateUserMessageChunk, m.Content
	case "assistant":
		if len(m.ToolCalls) > 0 && strings.HasPrefix(m.Content, syntheticAssistantText) {
			return "", ""
		}
		if m.Content == "" {
			return "", ""
		}
		return UpdateAgentMessageChunk, m.Content
	default:
		return "", ""
	}
}

// replayConversation streams one session's history to the client as
// session/update notifications. ACP requires the entire conversation
// before the session/load response; text is replayed as message chunks
// and tool activity as tool_call / tool_call_update pairs.
func (s *Server) replayConversation(st *sessionState, sendUpdate updateSender) {
	history := st.sess.History()
	replayed := 0
	for _, m := range history {
		updateKind, text := replayable(m)
		if text != "" {
			sendUpdate(st.id, ChunkUpdate{
				SessionUpdate: updateKind,
				Content:       ContentBlock{Type: "text", Text: text},
			})
			replayed++
		}
		for _, call := range m.ToolCalls {
			if call.ID == "" {
				// Older persisted rows may not carry a provider call
				// id; a tool call without one cannot be replayed.
				continue
			}
			name := call.Function.Name
			title := name
			update := ToolCallUpdate{
				SessionUpdate: UpdateToolCall,
				ToolCallID:    call.ID,
				Title:         title,
				Name:          name,
				Kind:          toolKind(name),
				Status:        StatusCompleted,
			}
			if json.Valid([]byte(call.Function.Arguments)) {
				update.RawInput = json.RawMessage(call.Function.Arguments)
			}
			sendUpdate(st.id, update)
			replayed++
		}
	}
	s.logf("session/load: %s replayed %d update(s)", st.id, replayed)
}

// handleSessionLoad resumes a persisted conversation: it builds the
// underlying session bound to the requested id (unknown ids fail the
// build and are rejected), replays the conversation as session/update
// notifications, and then answers. Loading an id that is already live
// in this process is idempotent; it replays the live conversation
// again, as a load request requires.
func (s *Server) handleSessionLoad(
	ctx context.Context,
	msg Message,
	sendUpdate updateSender,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SessionLoadParams
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
	cwd, err := canonicalCwd(p.Cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	mcpRefs, err := resolveMCPRefs(p.McpServers, cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}

	s.mu.Lock()
	st, live := s.sessions[p.SessionID]
	var busy bool
	if live {
		busy = st.run != nil && st.run.cancel != nil
	}
	s.mu.Unlock()
	if live {
		if busy {
			respErr(msg.ID, codeBusy, "a prompt is in flight for this session")
			return
		}
		if st.cwd != "" && !sameDir(st.cwd, cwd) {
			respErr(msg.ID, codeInvalidParams,
				fmt.Sprintf("session %q was created with cwd %q, requested %q", st.id, st.cwd, cwd))
			return
		}
		s.replayConversation(st, sendUpdate)
		respOK(msg.ID, SessionLoadResult{
			ConfigOptions: s.loadConfigOptions(ctx, st),
			Meta:          worldMetaOf(st.sess.WorldStatus()),
		})
		return
	}

	st, err = s.buildSession(BuildOptions{
		ResumeID:   p.SessionID,
		Cwd:        cwd,
		MCPServers: mcpRefs,
	}, p.SessionID)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	s.replayConversation(st, sendUpdate)
	respOK(msg.ID, SessionLoadResult{
		ConfigOptions: s.loadConfigOptions(ctx, st),
		Meta:          worldMetaOf(st.sess.WorldStatus()),
	})
}

// loadConfigOptions renders the config options a session/load response
// re-advertises: the same model picker session/new reports, built from
// the session's current selection. An unwired catalog yields nil, which
// omits the field entirely.
func (s *Server) loadConfigOptions(ctx context.Context, st *sessionState) []SessionConfigOption {
	return buildModelConfigOptions(s.modelCatalog(ctx), st.sess.ProviderName(), st.sess.ModelName())
}

// worldMetaOf renders the ACP `_meta` block for a derived posture. The zero
// status yields nil, which omits `_meta` entirely rather than sending an
// empty block.
func worldMetaOf(s worldinfo.Status) *SessionMeta {
	if s.IsZero() {
		return nil
	}
	return &SessionMeta{World: &WorldMeta{
		Backend:     s.Backend,
		Isolated:    s.Isolated,
		Mode:        s.Mode,
		Containment: s.Containment,
		Network:     s.Network,
		World:       s.WorldReq,
		Resolved:    s.ProposalWorld,
		Degraded:    s.Degraded,
		Reason:      s.Reason,
	}}
}

// sameDir compares two canonical cwds case-insensitively on Windows,
// where the client may spell the same directory differently.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return false
}

// capabilities builds the initialize advertisement: a capability is
// advertised only when this server instance was wired for it, so a
// client never calls a method the server cannot execute.
func (s *Server) capabilities() AgentCaps {
	sc := &SessionCaps{Resume: &SessionResumeCaps{}, Close: &SessionCloseCaps{}}
	if s.ListSessions != nil {
		sc.List = &SessionListCaps{}
	}
	if s.DeleteSession != nil {
		sc.Delete = &SessionDeleteCaps{}
	}
	return AgentCaps{LoadSession: true, Session: sc}
}

// CanonicalFilterCwd validates a session-list cwd filter: it must be
// absolute per the schema and is canonicalized exactly like a session
// cwd is on intake (absolute, symlink-resolved), so a filter matches the
// stored canonical session cwds. A non-existent directory is allowed —
// it simply matches nothing (an unmatched filter yields an empty page,
// not an error).
//
// Deprecated: use cwdpath.Canonical. The canonicalization lives in the
// leaf package internal/cwdpath so the engine-level session registry
// (internal/sessions) and every view can share it without importing the
// protocol package; this shim keeps the existing callers and this
// package's own test working.
func CanonicalFilterCwd(cwd string) (string, error) {
	return cwdpath.Canonical(cwd)
}

// handleSessionList implements session/list (sessionCapabilities.list):
// discovery of persisted sessions for a client's picker. The store
// callback supplies pages; the cwd filter and pagination cursor are
// validated here so malformed input surfaces as invalid params.
func (s *Server) handleSessionList(
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	if s.ListSessions == nil {
		respErr(msg.ID, codeMethodNotFound, "session/list is not wired")
		return
	}
	var p ListSessionsParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			respErr(msg.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	filter, err := cwdpath.Canonical(p.Cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	sessions, next, err := s.ListSessions(filter, p.Cursor)
	if err != nil {
		if errors.Is(err, ErrInvalidCursor) {
			respErr(msg.ID, codeInvalidParams, "invalid cursor")
			return
		}
		respErr(msg.ID, codeInternal, "list sessions: "+err.Error())
		return
	}
	if sessions == nil {
		// An empty page is an empty array: `sessions: null` is not
		// schema-valid (ListSessionsResponse requires the array).
		sessions = []SessionInfo{}
	}
	respOK(msg.ID, ListSessionsResult{Sessions: sessions, NextCursor: next})
}

// handleSessionResume implements session/resume (sessionCapabilities.resume):
// re-attach to a persisted conversation WITHOUT replaying its history —
// unlike session/load, no session/update notifications precede the
// response, and the session is ready for new prompts immediately. A live
// session re-attaches idempotently (result {}, exactly like the
// not-yet-attached case); a persisted id is rebuilt through the same
// factory session/load uses, so foreign ids are rejected with invalid
// params.
func (s *Server) handleSessionResume(
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SessionResumeParams
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
	cwd, err := canonicalCwd(p.Cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	mcpRefs, err := resolveMCPRefs(p.McpServers, cwd)
	if err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}

	s.mu.Lock()
	st, live := s.sessions[p.SessionID]
	var busy bool
	if live {
		busy = st.run != nil && st.run.cancel != nil
	}
	s.mu.Unlock()
	if live {
		if busy {
			respErr(msg.ID, codeBusy, "a prompt is in flight for this session")
			return
		}
		if st.cwd != "" && !sameDir(st.cwd, cwd) {
			respErr(msg.ID, codeInvalidParams,
				fmt.Sprintf("session %q was created with cwd %q, requested %q", st.id, st.cwd, cwd))
			return
		}
		respOK(msg.ID, nil)
		return
	}

	if _, err := s.buildSession(BuildOptions{
		ResumeID:   p.SessionID,
		Cwd:        cwd,
		MCPServers: mcpRefs,
	}, p.SessionID); err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	respOK(msg.ID, nil)
}

// retireSession is the shared teardown of session/close and session/
// delete: cancel any in-flight turn exactly like session/cancel, join
// the prompt goroutine (bounded — a wedged stdout write must not hang
// the request), drop the session from the live set, and free the
// underlying agent session. The dropped id stays resumable in a later
// process; the conversation itself is persisted store data.
func (s *Server) retireSession(st *sessionState) {
	// Snapshot the run under the server lock: the prompt goroutine
	// reassigns st.run (finished turns keep only the join handle) and
	// reads must not race that write.
	s.mu.Lock()
	run := st.run
	s.mu.Unlock()
	if run != nil && run.cancel != nil {
		run.cancel()
		if run.done != nil {
			timer := time.NewTimer(stopRunJoinTimeout)
			select {
			case <-run.done:
			case <-timer.C:
				s.logf("prompt goroutine did not unwind within %s; proceeding with teardown", stopRunJoinTimeout)
			}
			timer.Stop()
		}
	}
	s.mu.Lock()
	delete(s.sessions, st.id)
	s.mu.Unlock()
	st.sess.Close()
}

// handleSessionClose implements session/close (sessionCapabilities.close):
// cancel any ongoing work as if session/cancel had been called, then
// free the session. Only live sessions are closable — the spec allows an
// error when the session does not exist or is not active in this agent.
func (s *Server) handleSessionClose(
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SessionTargetParams
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
	st, ok := s.sessions[p.SessionID]
	s.mu.Unlock()
	if !ok {
		respErr(msg.ID, codeInvalidParams, "unknown session id")
		return
	}
	s.retireSession(st)
	respOK(msg.ID, nil)
}

// handleSessionDelete implements session/delete (sessionCapabilities.delete):
// remove the conversation from the session store so session/list stops
// reporting it. A still-live session is torn down first (the close
// contract). Deleting an already-deleted or never-created id succeeds
// silently per spec; real store failures are internal errors.
func (s *Server) handleSessionDelete(
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SessionTargetParams
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
	if s.DeleteSession == nil {
		// Defensively typed before any side effect: capabilities never
		// advertise delete without wiring, but a client may still ask —
		// and that must not have already torn down a live session.
		respErr(msg.ID, codeMethodNotFound, "session/delete is not wired")
		return
	}
	s.mu.Lock()
	st, live := s.sessions[p.SessionID]
	s.mu.Unlock()
	if live {
		s.retireSession(st)
	}
	if err := s.DeleteSession(p.SessionID); err != nil && !errors.Is(err, ErrNoSession) {
		respErr(msg.ID, codeInternal, "delete session: "+err.Error())
		return
	}
	respOK(msg.ID, nil)
}

// installQuestionHandler routes question-tool prompts to the client.
// When the client advertised elicitation.form and a connection is
// live, each question becomes a form elicitation and the chosen
// answer(s) reach the agent (withheld answers stay empty — never
// auto-answered over the user's decision); otherwise the question
// surfaces as status text and auto-answers with the first option when
// enabled. Called once after the underlying session is built; the
// closure captures its session so a prompt can never cross sessions.
func (s *Server) installQuestionHandler(st *sessionState) {
	if st == nil || st.sess == nil {
		return
	}
	reg := st.sess.ToolReg()
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
			answer, asked, accepted := s.elicitAnswer(st, e)
			switch {
			case asked && accepted:
				answers = append(answers, fmt.Sprintf("%s: %s", e.Header, answer))
				continue
			case asked && !accepted:
				// The user was offered the modal and withheld an answer
				// (decline, cancel, timeout): record the empty choice, do
				// not auto-answer over their decision.
				answers = append(answers, e.Header+": ")
				continue
			}
			// Un-available path: legacy fallback — status prose and
			// answering per AutoAnswerQuestions (headless/no-capability).
			msg := fmt.Sprintf("❓ %s\n\n%s\n\n", e.Header, e.Question)
			for i, o := range e.Options {
				msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
			}
			if ch := s.ctrlChFor(st); ch != nil {
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

// ctrlChFor returns the control channel of the prompt currently running
// in st, or nil.
func (s *Server) ctrlChFor(st *sessionState) chan<- control.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.ctrl
}

// close tears every underlying session down with the server.
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
			s.handleSetMode(msg, respOK, respErr)

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

// handleCancel cancels the in-flight turn if it belongs to the given
// session, ignoring malformed params — cancel is idempotent.
func (s *Server) handleCancel(msg Message) {
	var p CancelParams
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if p.SessionID == "" {
		s.logf("cancel ignored: sessionId is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[p.SessionID]
	if !ok {
		s.logf("cancel ignored: unknown session %q", p.SessionID)
		return
	}
	if st.run == nil || st.run.cancel == nil {
		s.logf("cancel ignored: no prompt in flight for %s", p.SessionID)
		return
	}
	s.logf("cancelling prompt turn: %s", p.SessionID)
	st.run.cancel()
}

// handleSetMode acknowledges mode switches. Modes are not modeled, so
// none are advertised; the request is validated against the live
// session set and then accepted so a client that surfaces a mode
// picker does not hang.
func (s *Server) handleSetMode(
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SetModeParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			respErr(msg.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	s.mu.Lock()
	_, ok := s.sessions[p.SessionID]
	s.mu.Unlock()
	if !ok {
		respErr(msg.ID, codeInvalidParams, "unknown session id")
		return
	}
	respOK(msg.ID, nil)
}

// modelCatalog returns the models discovery reports for the picker, or
// nil when no catalog is wired (configOptions is then omitted entirely).
// ctx bounds the wait for a catalog that discovers models over the
// network, so shutdown does not wait out a slow provider.
func (s *Server) modelCatalog(ctx context.Context) []ModelOption {
	if s.catalog == nil {
		return nil
	}
	return s.catalog.Options(ctx)
}

// handleSetConfigOption switches one session config option, which is
// the model picker and nothing else: any other configId is rejected
// rather than silently accepted. The value must be one of the values
// the session currently advertises (a client echoes back exactly what
// it was given); an unknown value leaves the session untouched and is
// invalid params. The response echoes the whole configOptions set
// recomputed from the new selection, as the spec requires. ctx bounds the
// model-catalog wait as in session/new.
func (s *Server) handleSetConfigOption(
	ctx context.Context,
	msg Message,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SetConfigOptionParams
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
	st, ok := s.sessions[p.SessionID]
	s.mu.Unlock()
	if !ok {
		respErr(msg.ID, codeInvalidParams, "unknown session id (call session/new first)")
		return
	}
	if p.ConfigID != ConfigOptionModelID {
		respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unsupported configId %q (supported: %s)", p.ConfigID, ConfigOptionModelID))
		return
	}
	catalog := s.modelCatalog(ctx)
	// Validate against the options the session advertises right now,
	// not against the raw catalog: the picker also lists a current model
	// discovery no longer reports, and re-selecting it must stay valid.
	advertised := buildModelConfigOptions(catalog, st.sess.ProviderName(), st.sess.ModelName())
	if advertised == nil {
		respErr(msg.ID, codeInvalidParams, "the model picker is not available: no models are configured")
		return
	}
	if !configOptionHasValue(advertised, ConfigOptionModelID, p.Value) {
		respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unknown value %q for configId %q", p.Value, p.ConfigID))
		return
	}
	provider, model, split := splitModelValue(p.Value)
	if !split {
		respErr(msg.ID, codeInvalidParams, fmt.Sprintf("invalid value %q: want \"<provider>/<model>\"", p.Value))
		return
	}
	st.sess.SetModel(provider, model)
	s.logf("session %s: model set to %s/%s", st.id, provider, model)
	respOK(msg.ID, SetConfigOptionResult{
		ConfigOptions: buildModelConfigOptions(catalog, provider, model),
	})
}

// handlePrompt runs one prompt turn. Per ACP v1, the request stays
// open until the turn ends and is then answered with a stopReason;
// session/update notifications stream before the response. Prompts on
// different sessions may run concurrently; a second prompt on the same
// session is rejected as busy.
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
	promptText, renderErr := renderPrompt(p.Prompt)
	if renderErr != nil {
		respErr(msg.ID, codeInvalidParams, renderErr.Error())
		return
	}
	if promptText == "" {
		respErr(msg.ID, codeInvalidParams, "prompt must contain at least one non-empty content block")
		return
	}

	ctrlCh := make(chan control.Msg, 64)
	promptCtx, cancel := context.WithCancel(rootCtx)
	runDone := make(chan struct{})

	s.mu.Lock()
	st, ok := s.sessions[p.SessionID]
	if !ok {
		s.mu.Unlock()
		cancel()
		respErr(msg.ID, codeInvalidParams, "unknown session id (call session/new first)")
		return
	}
	if st.run != nil && st.run.cancel != nil {
		s.mu.Unlock()
		cancel()
		respErr(msg.ID, codeBusy, "a prompt is already in flight for this session")
		return
	}
	// Snapshot the caps under the lock like the other readers, and
	// hand the view a session-lifetime compaction id allocator (a
	// fresh view is built per turn; the id namespace must not reset).
	caps := s.clientCaps
	view := NewViewWithWrite(sendUpdate, p.SessionID)
	if capsSupportsCompaction(caps) {
		view.EnableCompaction(func() string {
			return "cmp-" + strconv.FormatInt(st.compactionSeq.Add(1), 10)
		})
	}
	st.run = &runningPrompt{cancel: cancel, done: runDone}
	st.ctrl = ctrlCh
	st.view = view
	st.prunCtx = promptCtx
	sess := st.sess
	s.mu.Unlock()

	sess.SetView(view)
	sess.SetCtrlCh(ctrlCh)
	sess.SetApproveCallFn(func(callID, name, args string) bool {
		return s.requestPermission(p.SessionID, callID, name, args, promptCtx, c)
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
			if sess.TurnBudgetExhausted() {
				// The turn produced text, but only after the soft
				// tool-turn cap stripped tools: the agent stopped
				// because it could not act, not because it was done.
				stopReason = StopMaxTurnRequests
				s.logf("prompt hit turn budget: %s", p.SessionID)
			} else {
				s.logf("prompt completed: %s", p.SessionID)
			}
		case errors.Is(runErr, context.Canceled):
			stopReason = StopCancelled
			s.logf("prompt cancelled: %s", p.SessionID)
		case errors.Is(runErr, agent.MaxIterationsError{}):
			stopReason = StopMaxTurnRequests
			s.logf("prompt exhausted iteration budget: %s: %v", p.SessionID, runErr)
		default:
			// Keep the documented surface: a failed turn answers
			// stopReason "refusal" like any other end-of-turn state, and
			// the error detail lives in the log — the session/prompt
			// response promises a stopReason and clients key on it.
			s.logf("prompt error: %s: %v", p.SessionID, runErr)
			stopReason = StopRefusal
		}

		// Free the single-flight slot. All control producers that run
		// inside RunPrompt have returned, so anything already queued is
		// deliverable: closing flush makes forwardCtrl drain the rest of
		// its queue without regard to cancellation, and joining on
		// ctrlDone guarantees every queued update is on the wire before
		// the response. Only then is the prompt context cancelled. The
		// join handle stays in st.run so teardown can still join.
		s.mu.Lock()
		st.run = &runningPrompt{done: runDone}
		st.ctrl = nil
		st.view = nil
		st.prunCtx = nil
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
