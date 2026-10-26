package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// runCtxDone returns the turn context's Done channel; nil (no turn
// known) yields a nil channel, which blocks forever — the timeout is
// then the only bound, matching a headless ask.
func runCtxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// elicitationFormSchema builds the JSON Schema a question entry
// becomes. The top level is an object schema (required by form
// renderers); the single `answer` property is a string enum of the
// option labels, or a string array of the same enum for a
// multiple-answer question. The question text and option descriptions
// travel in the property description so the form is self-describing
// without bespoke rendering.
func elicitationFormSchema(question string, multiple bool, opts []tools.QuestionOption) json.RawMessage {
	var desc strings.Builder
	desc.WriteString(question)
	desc.WriteString("\n\n")
	for i, o := range opts {
		if i > 0 {
			desc.WriteString("\n")
		}
		desc.WriteString(o.Label)
		if o.Description != "" {
			desc.WriteString(" — " + o.Description)
		}
	}
	item := map[string]any{"type": "string"}
	labels := make([]string, 0, len(opts))
	for _, o := range opts {
		labels = append(labels, o.Label)
	}
	if len(labels) > 0 {
		item["enum"] = labels
		item["enumNames"] = labels
	}
	var prop any = item
	if multiple {
		prop = map[string]any{
			"type":        "array",
			"items":       item,
			"description": desc.String(),
		}
	} else {
		item["description"] = desc.String()
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": prop},
		"required":   []string{"answer"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	return raw
}

// elicitAnswer routes one question through elicitation/create and
// returns the chosen option label. asked reports whether the
// elicitation path was even available (no connection, or the client
// did not advertise elicitation.form, keep it false) so the caller's
// legacy fallback — status prose plus AutoAnswerQuestions — applies
// exactly as before. On a decline/cancel/timeout or an unparseable
// answer the elicitation WAS asked but yielded nothing: accepted is
// false and the caller must answer empty rather than auto-answer (a
// user who declined must not be overridden by a default).
func (s *Server) elicitAnswer(
	st *sessionState,
	entry tools.QuestionEntry,
) (answer string, asked, accepted bool) {
	s.mu.Lock()
	c := s.conn
	caps := s.clientCaps
	runCtx := st.prunCtx
	s.mu.Unlock()
	if c == nil || !capsAdvertiseElicitation(caps) {
		return "", false, false
	}

	schema := elicitationFormSchema(entry.Question, entry.Multiple, entry.Options)
	if len(schema) == 0 {
		return "", false, false
	}
	message := fmt.Sprintf("%s: %s", entry.Header, entry.Question)

	s.mu.Lock()
	s.nextReqID++
	reqID := s.nextReqID
	s.mu.Unlock()

	// Answer channel registered before the request goes out, like the
	// permission bridge, so a fast client cannot win the race.
	answerCh := make(chan clientResponse, 1)
	key := permissionID(reqID)
	s.mu.Lock()
	s.pendingReq[key] = answerCh
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingReq, key)
		s.mu.Unlock()
	}()

	c.sendRequest(reqID, "elicitation/create", ElicitationParams{
		Message:         message,
		Mode:            ElicitationModeForm,
		SessionID:       st.id,
		RequestedSchema: schema,
	})

	timeout := s.PermissionTimeout
	if timeout <= 0 {
		timeout = defaultPermissionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var resp clientResponse
	select {
	case resp = <-answerCh:
	case <-timer.C:
		s.logf("elicitation for session %s timed out", st.id)
		return "", true, false
	case <-runCtxDone(runCtx):
		s.logf("elicitation for session %s abandoned: turn ended", st.id)
		return "", true, false
	}
	if resp.err != nil {
		s.logf("elicitation error: %v", resp.err)
		return "", true, false
	}
	var out ElicitationResult
	if err := json.Unmarshal(resp.result, &out); err != nil {
		s.logf("unparseable elicitation response: %v", err)
		return "", true, false
	}
	if out.Action != ElicitationAccept {
		s.logf("elicitation %s: %s", st.id, orEmpty(out.Action, "no action"))
		return "", true, false
	}
	labels := chosenLabels(out.Content, entry.Multiple)
	if len(labels) == 0 {
		return "", true, false
	}
	return strings.Join(labels, ", "), true, true
}

// chosenLabels maps the elicitation content back onto the answer
// value(s). The schema declares a single-string or string-array
// answer, but a client may echo back something less tidy (null
// content, one option chosen as a plain string): the tolerant shapes
// are matched, and unknown strings pass through verbatim so the agent
// still sees the user's intent.
func chosenLabels(content json.RawMessage, multiple bool) []string {
	if len(content) == 0 || string(content) == "null" {
		return nil
	}
	var fields struct {
		Answer json.RawMessage `json:"answer"`
	}
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil
	}
	var labels []string
	if multiple {
		var arr []string
		if err := json.Unmarshal(fields.Answer, &arr); err != nil {
			// Maybe one option was chosen as a plain string.
			var one string
			if err2 := json.Unmarshal(fields.Answer, &one); err2 == nil {
				arr = []string{one}
			}
		}
		for _, a := range arr {
			if a == "" {
				continue
			}
			labels = append(labels, a)
		}
		return labels
	}
	var one string
	if err := json.Unmarshal(fields.Answer, &one); err != nil {
		return nil
	}
	if one != "" {
		labels = append(labels, one)
	}
	return labels
}

func capsAdvertiseElicitation(caps *ClientCaps) bool {
	return caps != nil && caps.Elicitation != nil && bool(caps.Elicitation.Form)
}

func orEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
