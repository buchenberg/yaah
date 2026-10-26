package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// clientResponse is the routed outcome of one server-initiated request.
type clientResponse struct {
	err    error // non-nil when the client answered with an error object
	result json.RawMessage
}

// permissionID canonicalizes a numeric request id for the routing map.
func permissionID(n int) string { return fmt.Sprintf("%d", n) }

// resolvePending routes a client response to the waiting request by
// id, if any is pending. Responses for unknown ids are dropped.
func (s *Server) resolvePending(msg Message) {
	key := s.canonicalID(msg.ID)
	s.mu.Lock()
	ch, ok := s.pendingReq[key]
	delete(s.pendingReq, key)
	s.mu.Unlock()
	if !ok {
		s.logf("response for unknown request id %s", key)
		return
	}
	out := clientResponse{}
	if msg.Error != nil {
		out.err = fmt.Errorf("client error %d: %s", msg.Error.Code, msg.Error.Message)
	} else {
		out.result = msg.Result
	}
	select {
	case ch <- out:
	default:
	}
}

// toolKind maps a yaah tool name onto an ACP tool kind for the client's
// icon and presentation treatment. Unlisted tools are "other".
func toolKind(name string) string {
	switch name {
	case "read", "grep", "glob", "ls", "memory", "prompt":
		return KindRead
	case "write", "edit", "replace":
		return KindEdit
	case "delete":
		return KindDelete
	case "bash", "powershell", "git", "process", "background":
		return KindExecute
	case "webfetch", "http", "web_search", "fetch":
		return KindFetch
	case "question", "think":
		return KindThink
	case "todo", "todowrite", "plan":
		return KindThink
	default:
		return KindOther
	}
}

// requestPermission implements the agent loop's ApproveFn for ACP: it
// reports the pending tool call, sends session/request_permission to
// the client, and blocks (bounded) for the decision.
//
// The approval middleware runs BEFORE the tool's own ToolStartEvent,
// so the pending call is carried entirely by the permission request;
// a denial is rendered as a failed tool_call_update so the user sees
// the outcome, and an approval is followed by the real tool_call from
// the dispatch pipeline.
func (s *Server) requestPermission(
	sessionID, callID, name, args string,
	runCtx context.Context,
	c *connection,
) bool {
	// A previous allow_always/reject_always decision short-circuits the
	// client round-trip: an always-allowed tool proceeds, an
	// always-rejected one is denied, both without re-prompting for the
	// rest of the session. No client row is created in either case —
	// an approval is followed by the pipeline's real tool_call, and a
	// denial surfaces through the agent's own failed tool result.
	s.mu.Lock()
	st := s.sessions[sessionID]
	var remembered string
	if st != nil {
		if st.permAllow[name] {
			remembered = PermAllowAlways
		} else if st.permDeny[name] {
			remembered = PermRejectAlways
		}
	}
	s.mu.Unlock()
	switch remembered {
	case PermAllowAlways:
		s.logf("permission auto-allowed for session %s: %s (always)", sessionID, name)
		return true
	case PermRejectAlways:
		s.logf("permission auto-denied for session %s: %s (always)", sessionID, name)
		return false
	}

	s.mu.Lock()
	s.nextReqID++
	reqID := s.nextReqID
	s.mu.Unlock()
	if c == nil {
		// Run has not started (or already ended) — deny, never block.
		return false
	}

	toolCallID := callID
	if toolCallID == "" {
		// A model that supplies no call id cannot be correlated; keep
		// the old synthetic id so the prompt still works.
		toolCallID = fmt.Sprintf("perm_%d", reqID)
	}
	title := permissionTitle(name, args)
	if title == "" {
		title = name
	}
	send := func(sessionID string, u any) {
		raw, err := json.Marshal(u)
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

	// Answer channel registered BEFORE the request goes out so a fast
	// client cannot arrive ahead of the routing entry.
	answer := make(chan clientResponse, 1)
	key := permissionID(reqID)
	s.mu.Lock()
	s.pendingReq[key] = answer
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingReq, key)
		s.mu.Unlock()
	}()

	// Create the tool call the client will show in its permission
	// dialog, and remember it so the execution event below updates the
	// same row instead of creating a second one.
	announced := callID != ""
	if announced {
		s.mu.Lock()
		st := s.sessions[sessionID]
		if st != nil && st.view != nil {
			st.view.Announce(toolCallID)
		}
		s.mu.Unlock()
		send(sessionID, ToolCallUpdate{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    toolCallID,
			Title:         title,
			Name:          name,
			Kind:          toolKind(name),
			Status:        StatusPending,
		})
	}

	c.sendRequest(reqID, "session/request_permission", RequestPermissionParams{
		SessionID: sessionID,
		ToolCall: ToolCallUpdate{
			SessionUpdate: UpdateToolCallUpdate,
			ToolCallID:    toolCallID,
			Title:         title,
			Name:          name,
			Kind:          toolKind(name),
			Status:        StatusPending,
		},
		Options: permissionOptions(name),
	})

	timeout := s.PermissionTimeout
	if timeout <= 0 {
		timeout = defaultPermissionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	deny := func(reason string) {
		s.logf("permission %s denied: %s", toolCallID, reason)
		// Render the denial so a row the client created from the
		// permission request does not hang in a pending state. The
		// id-less fallback (no announced row) renders nothing: a failed
		// update for a synthetic perm_N id the client never saw would
		// create an orphan row instead.
		if !announced {
			return
		}
		send(sessionID, ToolCallUpdate{
			SessionUpdate: UpdateToolCallUpdate,
			ToolCallID:    toolCallID,
			Title:         title,
			Name:          name,
			Kind:          toolKind(name),
			Status:        StatusFailed,
			Content: []UpdateContent{{
				Type:    "content",
				Content: &ContentBlock{Type: "text", Text: "Denied: " + reason},
			}},
		})
	}

	var resp clientResponse
	select {
	case resp = <-answer:
	case <-timer.C:
		deny("no answer from client within " + timeout.String())
		return false
	case <-runCtx.Done():
		// The turn is over (cancel or shutdown); the spec requires a
		// cancelled outcome from the client in this state, but a local
		// deny is the safe interpretation either way. No failed-row
		// render here: the client is unwinding the same turn.
		s.logf("permission request %s abandoned: turn ended", toolCallID)
		return false
	}

	if resp.err != nil {
		deny(resp.err.Error())
		return false
	}
	if resp.result == nil {
		deny("empty response from client")
		return false
	}
	var out RequestPermissionResult
	if err := json.Unmarshal(resp.result, &out); err != nil {
		deny("unparseable response: " + err.Error())
		return false
	}
	if out.Outcome.Outcome != "selected" {
		deny("not selected")
		return false
	}
	switch out.Outcome.OptionID {
	case "allow-once":
		return true
	case "allow-always":
		s.rememberPermission(sessionID, name, PermAllowAlways)
		return true
	case "reject-once":
		deny("rejected by user")
		return false
	case "reject-always":
		s.rememberPermission(sessionID, name, PermRejectAlways)
		deny("always rejected for this session")
		return false
	default:
		deny("unknown option " + out.Outcome.OptionID)
		return false
	}
}

// permissionOptions is the full decision menu Zed renders as "Allow
// once / Always for <tool> / Reject / Always for <pattern>"-style
// buttons; the always kinds only enable those buttons when present.
func permissionOptions(name string) []PermissionOption {
	return []PermissionOption{
		{OptionID: "allow-once", Name: "Allow once", Kind: PermAllowOnce},
		{OptionID: "allow-always", Name: "Always allow " + name, Kind: PermAllowAlways},
		{OptionID: "reject-once", Name: "Reject", Kind: PermRejectOnce},
		{OptionID: "reject-always", Name: "Always reject " + name, Kind: PermRejectAlways},
	}
}

// rememberPermission records an always-kind decision for the rest of
// the session; the entry dies with the session (retireSession), never
// in the store.
func (s *Server) rememberPermission(sessionID, name, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	switch kind {
	case PermAllowAlways:
		if st.permAllow == nil {
			st.permAllow = make(map[string]bool)
		}
		st.permAllow[name] = true
	case PermRejectAlways:
		if st.permDeny == nil {
			st.permDeny = make(map[string]bool)
		}
		st.permDeny[name] = true
	}
}

// permissionTitle renders a short human-readable title for a pending
// call: the tool name plus a compacted argument hint.
func permissionTitle(name, args string) string {
	hint := strings.Join(strings.Fields(args), " ")
	if hint == "" {
		return name
	}
	return fmt.Sprintf("%s: %s", name, truncate(hint, 60))
}
