package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

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

// refreshAvailableCommands discovers the session's command palette and emits
// it as available_commands_update. It is called after new/load/resume so a
// client can build its picker without a separate request.
func (s *Server) refreshAvailableCommands(st *sessionState, sendUpdate updateSender) {
	if s.AvailableCommands == nil || st == nil || sendUpdate == nil {
		return
	}
	commands := s.AvailableCommands(st.cwd)
	st.commands = commands
	if len(commands) == 0 {
		return
	}
	sendUpdate(st.id, AvailableCommandsUpdate{
		SessionUpdate:     UpdateAvailableCommands,
		AvailableCommands: commands,
	})
}

// handleSessionNew creates a fresh conversation for the requested cwd
// and returns its ACP id. Every call builds a new session: session ids
// are unique per created session, and independent sessions may coexist
// in one process. ctx bounds the model-catalog wait so a slow provider
// discovery is abandoned when the server shuts down.
func (s *Server) handleSessionNew(ctx context.Context, msg Message, sendUpdate updateSender, respOK func(id json.RawMessage, result any), respErr func(id json.RawMessage, code int, message string)) {
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
		Modes:         sessionModeState(st.sess.Mode()),
		Meta:          worldMetaOf(st.sess.WorldStatus()),
	})
	s.refreshAvailableCommands(st, sendUpdate)
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
			Modes:         sessionModeState(st.sess.Mode()),
			Meta:          worldMetaOf(st.sess.WorldStatus()),
		})
		s.refreshAvailableCommands(st, sendUpdate)
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
		Modes:         sessionModeState(st.sess.Mode()),
		Meta:          worldMetaOf(st.sess.WorldStatus()),
	})
	s.refreshAvailableCommands(st, sendUpdate)
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
	w := &WorldMeta{
		Backend:     s.Backend,
		Isolated:    s.Isolated,
		Mode:        s.Mode,
		Containment: s.Containment,
		Network:     s.Network,
		World:       s.WorldReq,
		Resolved:    s.ProposalWorld,
		Degraded:    s.Degraded,
		Reason:      s.Reason,
	}
	if s.ReportWorld != s.ProposalWorld {
		w.Report = s.ReportWorld
	}
	return &SessionMeta{World: w}
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
	caps := AgentCaps{
		LoadSession:        true,
		PromptCapabilities: &PromptCaps{EmbeddedContext: true},
		Session:            sc,
	}
	if s.Logout != nil {
		caps.Auth = &AgentAuthCaps{Logout: &LogoutCaps{}}
	}
	return caps
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
		respErr(msg.ID, codeInternal, "session/list is not available in this server configuration")
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
	sendUpdate updateSender,
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
		respOK(msg.ID, SessionResumeResult{Modes: sessionModeState(st.sess.Mode())})
		s.refreshAvailableCommands(st, sendUpdate)
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
	respOK(msg.ID, SessionResumeResult{Modes: sessionModeState(st.sess.Mode())})
	s.refreshAvailableCommands(st, sendUpdate)
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
		respErr(msg.ID, codeInternal, "session/delete is not available in this server configuration")
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
