package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/control"
)

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

// handleSetMode sets the session's mode axis through the ACP mode
// vocabulary: the spec's plan/build ids map onto read-only/full. The id
// must be one of the modes advertised on session/new; unknown ids fail
// closed instead of being silently accepted. The canonical notification
// is config_option_update; the change applies to the next prompt (an
// in-flight turn keeps the mode it started with). The persona is
// untouched — the axes are independent.
func (s *Server) handleSetMode(
	ctx context.Context,
	msg Message,
	sendUpdate updateSender,
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
	if p.SessionID == "" {
		respErr(msg.ID, codeInvalidParams, "sessionId is required")
		return
	}
	if !validSessionMode(p.ModeID) {
		respErr(msg.ID, codeInvalidParams,
			fmt.Sprintf("unknown modeId %q (available: %s, %s)", p.ModeID, SessionModeBuild, SessionModePlan))
		return
	}
	s.mu.Lock()
	st, ok := s.sessions[p.SessionID]
	s.mu.Unlock()
	if !ok {
		respErr(msg.ID, codeInvalidParams, "unknown session id")
		return
	}
	if st.sess.Mode() == p.ModeID {
		respOK(msg.ID, nil)
		return
	}
	// The mode id maps onto the mode axis, not a persona: "plan" makes
	// the session read-only, "build" gives it full access. The persona is
	// untouched.
	st.sess.SetReadOnly(p.ModeID == SessionModePlan)
	sendUpdate(st.id, ConfigOptionUpdate{
		SessionUpdate: UpdateConfigOption,
		ConfigOptions: sessionConfigOptions(s.modelCatalog(ctx), st.sess),
	})
	respOK(msg.ID, nil)
}

// handleSetAgent switches the persona of the ADDRESSED session
// (yaah-21uk.12): SetPersona rebinds the persona body and re-resolves
// the loop budget at the next loop build, and the switch announces
// in-band like every other pick surface. The mode axis is untouched —
// read-only-ness is session state, set through session/set_mode or the
// mode config option. The name must
// resolve primary-capable — a miss is invalid params with the persona
// registry's reason wording (typo vs delegate-only). The result reports
// the session's effective persona and its current mode-axis posture.
func (s *Server) handleSetAgent(
	ctx context.Context,
	msg Message,
	sendUpdate updateSender,
	respOK func(id json.RawMessage, result any),
	respErr func(id json.RawMessage, code int, message string),
) {
	var p SetAgentParams
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
	name := strings.TrimSpace(p.Agent)
	if name == "" {
		respErr(msg.ID, codeInvalidParams, "agent is required")
		return
	}
	s.mu.Lock()
	st, ok := s.sessions[p.SessionID]
	s.mu.Unlock()
	if !ok {
		respErr(msg.ID, codeInvalidParams, "unknown session id")
		return
	}
	if _, resolvable := subagent.ResolvePersona(name); !resolvable {
		respErr(msg.ID, codeInvalidParams,
			fmt.Sprintf("%q is not a primary-capable agent (%s)", name, subagent.PersonaMissReason(name)))
		return
	}
	if err := st.sess.SetPersona(name); err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	// A client that changed the persona must hear the config selector
	// moved: the agent axis has its own selector now, and the mode axis
	// is untouched by an agent switch.
	sendUpdate(st.id, ConfigOptionUpdate{
		SessionUpdate: UpdateConfigOption,
		ConfigOptions: sessionConfigOptions(s.modelCatalog(ctx), st.sess),
	})
	// The result reports the session's mode axis, not a persona property:
	// read-only-ness is session state, independent of which persona runs.
	respOK(msg.ID, SetAgentResult{Persona: st.sess.PersonaName(), ReadOnly: st.sess.IsReadOnly()})
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

// handleSetConfigOption switches one session config option: the three
// session axes (agent / mode / approval) and the model picker. Any other
// configId is rejected rather than silently accepted. A value must be one
// of the values the session currently advertises (a client echoes back
// exactly what it was given); an unknown value leaves the session
// untouched and is invalid params. The response echoes the whole
// configOptions set recomputed from the new selection, as the spec
// requires. ctx bounds the model-catalog wait as in session/new.
func (s *Server) handleSetConfigOption(
	ctx context.Context,
	msg Message,
	sendUpdate updateSender,
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
	catalog := s.modelCatalog(ctx)
	// echo recomputes the full selector set from the session's CURRENT
	// state, so every response describes the axes as they now stand.
	echo := func() SetConfigOptionResult {
		return SetConfigOptionResult{ConfigOptions: sessionConfigOptions(catalog, st.sess)}
	}
	pushUpdate := func() {
		sendUpdate(st.id, ConfigOptionUpdate{SessionUpdate: UpdateConfigOption, ConfigOptions: echo().ConfigOptions})
	}
	switch p.ConfigID {
	case ConfigOptionAgentID:
		// The agent axis: switch the persona the session runs as. The
		// value must be one the session advertises (same contract as the
		// model picker), which also keeps session/set_agent and this
		// path agreeing on the refusal wording.
		advertised := sessionConfigOptions(catalog, st.sess)
		if !configOptionHasValue(advertised, ConfigOptionAgentID, p.Value) {
			respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unknown value %q for configId %q", p.Value, p.ConfigID))
			return
		}
		if err := st.sess.SetPersona(p.Value); err != nil {
			respErr(msg.ID, codeInvalidParams, err.Error())
			return
		}
		pushUpdate()
		respOK(msg.ID, echo())
	case ConfigOptionModeID:
		if !validSessionMode(p.Value) {
			respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unknown mode %q (available: %s, %s)", p.Value, SessionModeBuild, SessionModePlan))
			return
		}
		// The mode axis: read-only vs full, independent of the persona
		// (the same seam session/set_mode drives).
		if st.sess.Mode() != p.Value {
			st.sess.SetReadOnly(p.Value == SessionModePlan)
			pushUpdate()
		}
		respOK(msg.ID, echo())
	case ConfigOptionApprovalID:
		// The approval axis: the posture dangerous calls meet. Validate
		// against the advertised options so an unknown value never lands.
		if !configOptionHasValue(sessionConfigOptions(catalog, st.sess), ConfigOptionApprovalID, p.Value) {
			respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unknown value %q for configId %q", p.Value, p.ConfigID))
			return
		}
		if err := st.sess.SetApprovalMode(p.Value); err != nil {
			respErr(msg.ID, codeInvalidParams, err.Error())
			return
		}
		s.logf("session %s: approval set to %s", st.id, p.Value)
		pushUpdate()
		respOK(msg.ID, echo())
	case ConfigOptionModelID:
		// Validate against the options the session advertises right now,
		// not against the raw catalog: the picker also lists a current model
		// discovery no longer reports, and re-selecting it must stay valid.
		advertised := sessionConfigOptions(catalog, st.sess)
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
		respOK(msg.ID, echo())
	default:
		respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unsupported configId %q (supported: %s, %s, %s, %s)", p.ConfigID, ConfigOptionAgentID, ConfigOptionModeID, ConfigOptionApprovalID, ConfigOptionModelID))
	}
}

// translateCommandPrompt turns a palette selection into a normal prompt. The
// advertised commands are skills; the skill tool is the executor, so the model
// receives an explicit load instruction instead of an opaque slash token.
func translateCommandPrompt(st *sessionState, prompt string) string {
	if st == nil || !strings.HasPrefix(prompt, "/") {
		return prompt
	}
	trimmed := strings.TrimPrefix(prompt, "/")
	name, args := trimmed, ""
	if i := strings.IndexAny(trimmed, " \t\r\n"); i >= 0 {
		name, args = trimmed[:i], trimmed[i:]
	}
	for _, command := range st.commands {
		if command.Name != name {
			continue
		}
		args = strings.TrimSpace(args)
		if args == "" {
			return fmt.Sprintf("Use the skill tool with action \"load\" and name %q, then follow its instructions.", name)
		}
		return fmt.Sprintf("Use the skill tool with action \"load\" and name %q, then follow its instructions to handle this request: %s", name, args)
	}
	return prompt
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
	view.EnableEscalations(func() string {
		return "escalation-" + strconv.FormatInt(st.escalationSeq.Add(1), 10)
	})
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
	promptText = translateCommandPrompt(st, promptText)

	sess.SetView(view)
	sess.SetCtrlCh(ctrlCh)
	sess.SetApproveCallFn(func(ctx context.Context, callID, name, args string) bool {
		return s.requestPermission(p.SessionID, callID, name, args, ctx, c)
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
// underlying agent session speaks. Text, embedded resources, and resource
// links are accepted; image and audio blocks are rejected with a clear error
// because yaah's provider message model has no multimodal content parts.
// Silently substituting a placeholder would tell the caller the image/audio
// was seen when it was not.
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
			return "", fmt.Errorf("%s content blocks are not supported by this agent", b.Type)
		default:
			// Unknown block types are preserved as text for forward
			// compatibility: a newer ACP client may add a type this server
			// does not model, and dropping or rejecting it would be worse
			// than showing the reference.
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
