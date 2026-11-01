package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
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

// handleSetMode switches the Plan/Build session mode. The id must be one of
// the modes advertised on session/new; unknown ids fail closed instead of
// being silently accepted. The change is emitted as current_mode_update and
// applies to the next prompt (an in-flight turn keeps the mode it started
// with).
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
	if err := st.sess.SetMode(p.ModeID); err != nil {
		respErr(msg.ID, codeInvalidParams, err.Error())
		return
	}
	sendUpdate(st.id, CurrentModeUpdate{
		SessionUpdate: "current_mode_update",
		CurrentModeID: p.ModeID,
	})
	sendUpdate(st.id, ConfigOptionUpdate{
		SessionUpdate: UpdateConfigOption,
		ConfigOptions: buildSessionConfigOptions(s.modelCatalog(ctx), st.sess.ProviderName(), st.sess.ModelName(), p.ModeID),
	})
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
	switch p.ConfigID {
	case ConfigOptionModeID:
		if !validSessionMode(p.Value) {
			respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unknown mode %q (available: %s, %s)", p.Value, SessionModeBuild, SessionModePlan))
			return
		}
		if st.sess.Mode() != p.Value {
			if err := st.sess.SetMode(p.Value); err != nil {
				respErr(msg.ID, codeInvalidParams, err.Error())
				return
			}
			sendUpdate(st.id, CurrentModeUpdate{SessionUpdate: "current_mode_update", CurrentModeID: p.Value})
			sendUpdate(st.id, ConfigOptionUpdate{
				SessionUpdate: UpdateConfigOption,
				ConfigOptions: buildSessionConfigOptions(catalog, st.sess.ProviderName(), st.sess.ModelName(), p.Value),
			})
		}
		respOK(msg.ID, SetConfigOptionResult{
			ConfigOptions: buildSessionConfigOptions(catalog, st.sess.ProviderName(), st.sess.ModelName(), p.Value),
		})
	case ConfigOptionModelID:
		// Validate against the options the session advertises right now,
		// not against the raw catalog: the picker also lists a current model
		// discovery no longer reports, and re-selecting it must stay valid.
		advertised := buildSessionConfigOptions(catalog, st.sess.ProviderName(), st.sess.ModelName(), st.sess.Mode())
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
			ConfigOptions: buildSessionConfigOptions(catalog, provider, model, st.sess.Mode()),
		})
	default:
		respErr(msg.ID, codeInvalidParams, fmt.Sprintf("unsupported configId %q (supported: %s, %s)", p.ConfigID, ConfigOptionModeID, ConfigOptionModelID))
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
