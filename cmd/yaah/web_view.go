package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/toolfmt"
)

type wireOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type wireMCPServer struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"toolCount"`
	Error     string `json:"error,omitempty"`
}

type wireHeaderMeta struct {
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	MCPServers []wireMCPServer `json:"mcpServers"`
}

type wireTodo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
	// Priority is always serialized: the absence of a priority is meaningful
	// (the item simply has none) and omitempty made the field vanish from the
	// frame, so the client saw two different row shapes.
	Priority string `json:"priority"`
}

// wireTodoOf maps a todo-store item onto the SSE wire shape. ID is the item's
// stable identity: the client keys per-row state (expansion, animation,
// scroll anchoring) off it and only falls back to the array index when it is
// empty, so dropping it reattached that state to the wrong todo after a
// reorder (yaah-ljk).
func wireTodoOf(item todo.Item) wireTodo {
	return wireTodo{
		ID:       item.ID,
		Content:  item.Content,
		Status:   item.Status,
		Priority: item.Priority,
	}
}

type sseWireEvent struct {
	Type string `json:"type"`

	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`

	Name    string `json:"name,omitempty"`
	Args    string `json:"args,omitempty"`
	Result  string `json:"result,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Error   string `json:"error,omitempty"`
	ToolID  int64  `json:"tool_id,omitempty"`
	Summary string `json:"summary,omitempty"`

	Role   string `json:"role,omitempty"`
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt,omitempty"`

	Response      string `json:"response,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`

	// Running appears only on the ctrl.state catch-up frame sent when a
	// client connects, so it is a pointer: absent everywhere else.
	Running *bool `json:"running,omitempty"`

	ID       string       `json:"id,omitempty"`
	Header   string       `json:"header,omitempty"`
	Question string       `json:"question,omitempty"`
	Options  []wireOption `json:"options,omitempty"`
	Multi    bool         `json:"multi,omitempty"`

	Meta *wireHeaderMeta `json:"meta,omitempty"`

	Items   []wireTodo `json:"items,omitempty"`
	MaxIter int        `json:"max_iter,omitempty"`

	Tokens int `json:"tokens,omitempty"`
	Window int `json:"window,omitempty"`

	Models    []string          `json:"models,omitempty"`
	Providers map[string]string `json:"providers,omitempty"`
}

func marshalWire(we sseWireEvent) []byte {
	data, _ := json.Marshal(we)
	return data
}

// SendConnect sends the header info and current todo state to a newly
// connected SSE client, catching it up to the current session state.
func (v *sseView) SendConnect() {
	v.mu.Lock()
	defer v.mu.Unlock()
	h := &wireHeaderMeta{
		Provider:   v.provider,
		Model:      v.model,
		MCPServers: v.mcpServers,
	}
	v.writeLocked(marshalWire(sseWireEvent{Type: "ctrl.header", Meta: h}))
	if len(v.todos) > 0 {
		v.writeLocked(marshalWire(sseWireEvent{Type: "ctrl.todos", Items: v.todos}))
	}
}

// SetHeader updates the cached header meta and re-sends it to the
// connected client, e.g. after a model switch.
func (v *sseView) SetHeader(provider, model string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.provider = provider
	v.model = model
	v.writeLocked(marshalWire(sseWireEvent{Type: "ctrl.header", Meta: &wireHeaderMeta{
		Provider:   provider,
		Model:      model,
		MCPServers: v.mcpServers,
	}}))
}

type sseView struct {
	w  http.ResponseWriter
	mu sync.Mutex

	// header info set from the session on SSE connect
	provider   string
	model      string
	mcpServers []wireMCPServer

	// current todo state — updated by forwardCtrl; sent on SSE connect
	todos []wireTodo
}

// AcceptsEventsBetweenRuns reports that writes are mutex-guarded and can be
// delivered from the background-job goroutine outside a run.
func (v *sseView) AcceptsEventsBetweenRuns() bool { return true }

func (v *sseView) HandleEvent(evt agent.Event) {
	var we sseWireEvent
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		we = sseWireEvent{Type: "token", Text: e.Text}
	case *agent.ThinkingEvent:
		we = sseWireEvent{Type: "thinking", Text: e.Text}
	case *agent.FlushEvent:
		we = sseWireEvent{Type: "flush", Content: e.Content}
	case *agent.ToolStartEvent:
		we = sseWireEvent{
			Type: "tool.start", ToolID: e.ID,
			Name: e.Name, Args: e.Args,
			Summary: toolStartSummary(e.Name, e.Args),
		}
	case *agent.ToolEndEvent:
		we = sseWireEvent{
			Type: "tool.end", ToolID: e.ID,
			Name: e.Name, Args: e.Args,
			Result: e.Result, Ms: e.Duration.Milliseconds(), Error: e.Error,
			Summary: toolfmt.Summary(e.Name, e.Args, e.Result),
		}
	case *agent.SubAgentStartEvent:
		we = sseWireEvent{Type: "subagent.start", Role: e.Role, Model: e.Model, Prompt: e.Prompt}
	case *agent.SubAgentEndEvent:
		we = sseWireEvent{Type: "subagent.end", Role: e.Role, Model: e.Model,
			Prompt: e.Prompt, Ms: e.Duration.Milliseconds(), Error: e.Error}
	case *agent.EscalationEvent:
		we = sseWireEvent{Type: "ctrl.status",
			Text: fmt.Sprintf("ESCALATION [%s] %s: %s", e.Severity, e.SubAgentRole, e.Summary)}
	case *agent.DoneEvent:
		// Prefer the real provider-reported token count over the
		// char/4 estimate (which inflates with reasoning content).
		ct := e.ContextTokens
		if e.LastPromptTokensInclusive > 0 {
			ct = e.LastPromptTokensInclusive
		} else if e.LastPromptTokens > 0 {
			ct = e.LastPromptTokens
		}
		we = sseWireEvent{Type: "done", Response: e.Response, Error: e.Error,
			ContextTokens: ct, ContextWindow: e.ContextWindow}
	case *agent.CompactionStartedEvent:
		we = sseWireEvent{Type: "ctrl.status", Text: e.StartedSummary()}
	case *agent.CompactionDoneEvent:
		we = sseWireEvent{Type: "ctrl.status", Text: e.DoneSummary()}
	case *agent.TitleSetEvent:
		// Intentionally ignored: the web UI has no session-title display
		// yet; the /api/sessions endpoint serves titles from the store.
		return
	default:
		// Deliberately unmatched events emit no wire frame — the
		// previous fallthrough wrote an empty {"type":""} SSE event
		// for anything new (review B12).
		return
	}
	v.write(marshalWire(we))
}

func (v *sseView) write(data []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.writeLocked(data)
}

// writeLocked writes SSE data assuming v.mu is already held.
func (v *sseView) writeLocked(data []byte) {
	fmt.Fprintf(v.w, "data: %s\n\n", data)
	if f, ok := v.w.(http.Flusher); ok {
		f.Flush()
	}
}

type answerMap struct {
	mu      sync.Mutex
	pending map[string]chan<- string
}

func newAnswerMap() *answerMap {
	return &answerMap{pending: make(map[string]chan<- string)}
}

func (am *answerMap) register(id string, ch chan<- string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.pending[id] = ch
}

func (am *answerMap) deliver(id, value string) bool {
	am.mu.Lock()
	defer am.mu.Unlock()
	ch, ok := am.pending[id]
	if !ok {
		return false
	}
	ch <- value
	delete(am.pending, id)
	return true
}

func (am *answerMap) cancel(id string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	delete(am.pending, id)
}

// awaitModal blocks until the browser answers the outstanding prompt or the
// stream is torn down, and always releases the answer slot. Registering an id
// without releasing it leaked one map entry per question/approval/continue for
// the lifetime of the server (yaah-fcg); every exit path — answered,
// abandoned, superseded stream — releases it here.
func awaitModal(ctx context.Context, am *answerMap, id string, ansCh <-chan string) (string, bool) {
	defer am.cancel(id)
	select {
	case ans := <-ansCh:
		return ans, true
	case <-ctx.Done():
		return "", false
	}
}

func forwardCtrl(ctx context.Context, ch <-chan control.Msg, v *sseView, am *answerMap, idGen *atomic.Int64) {
	// One modal at a time. The browser renders a single dialog, so a control
	// item that needs an answer is held in this loop until the open one is
	// resolved: emitting the next frame immediately overwrote the dialog the
	// user was looking at and the first answer was lost forever. Holding the
	// loop is safe — the control channel is buffered and its senders already
	// bound their wait (ctrlSendTimeout for questions, a non-blocking send for
	// max-iteration continuation) — and it orders modals exactly as the user
	// will see them.
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *control.Status:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.status", Text: m.Text}))
			case *control.Error:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.error", Error: m.Err.Error()}))
			case *control.Question:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				opts := make([]wireOption, len(m.Options))
				for i, o := range m.Options {
					opts[i] = wireOption{Label: o.Label, Description: o.Description}
				}
				v.write(marshalWire(sseWireEvent{
					Type: "ctrl.question", ID: id,
					Header: m.Header, Question: m.Question,
					Options: opts, Multi: m.Multiple,
				}))
				ans, ok := awaitModal(ctx, am, id, ansCh)
				if !ok {
					// Stream gone: the question tool's own answer
					// timeout supplies the fallback.
					return
				}
				m.AnswerCh <- ans
			case *control.Approval:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				v.write(marshalWire(sseWireEvent{
					Type: "ctrl.approval", ID: id,
					Name: m.Name, Args: m.Args,
				}))
				ans, ok := awaitModal(ctx, am, id, ansCh)
				if !ok {
					return
				}
				m.ApproveCh <- (ans == "true")
			case *control.Continue:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				v.write(marshalWire(sseWireEvent{
					Type:    "ctrl.continue",
					ID:      id,
					MaxIter: m.MaxIter,
				}))
				ans, ok := awaitModal(ctx, am, id, ansCh)
				if !ok {
					// Stream torn down with the prompt unanswered: tell the
					// agent to stop instead of leaving it waiting on an answer
					// that can no longer arrive (AnswerCh is buffered by the
					// caller, so this cannot block).
					m.AnswerCh <- false
					return
				}
				m.AnswerCh <- (ans == "yes")
			case *control.Todos:
				items := make([]wireTodo, len(m.Items))
				for i, item := range m.Items {
					items[i] = wireTodoOf(item)
				}
				v.mu.Lock()
				v.todos = items
				v.mu.Unlock()
				v.write(marshalWire(sseWireEvent{Type: "ctrl.todos", Items: items}))
			case *control.ContextInfo:
				ct := m.Tokens
				if m.LastPromptTokensInclusive > 0 {
					ct = m.LastPromptTokensInclusive
				} else if m.LastPromptTokens > 0 {
					ct = m.LastPromptTokens
				}
				v.write(marshalWire(sseWireEvent{Type: "ctrl.context", Tokens: ct, Window: m.Window}))
			case *control.ModelList:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.models", Models: m.Models, Providers: m.ProviderNames}))
			case *control.Done:
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// --- tool summary helpers (shared helpers in internal/toolfmt) ---

var (
	urlFieldRe    = regexp.MustCompile(`"url"\s*:\s*"([^"]*)"`)
	urlsFieldRe   = regexp.MustCompile(`"urls"\s*:\s*\["([^"]*)"`)
	actionFieldRe = regexp.MustCompile(`"action"\s*:\s*"([^"]*)"`)
)

// toolStartSummary returns a brief description of what a tool does,
// based only on its name and arguments (before execution).
func toolStartSummary(name, args string) string {
	switch name {
	case "bash":
		return args
	case "read":
		return toolfmt.FilePath(args)
	case "write":
		return "→ " + toolfmt.FilePath(args)
	case "edit":
		return toolfmt.FilePath(args)
	case "delete":
		return toolfmt.FilePath(args)
	case "grep":
		return shortPattern(args)
	case "glob":
		return shortPattern(args)
	case "http", "webfetch":
		if u := extractURL(args); u != "" {
			return u
		}
	case "git":
		if a := extractAction(args); a != "" {
			return a
		}
	case "delegate":
		role := toolfmt.MatchJSONField(args, "role")
		desc := toolfmt.MatchJSONField(args, "description")
		return toolfmt.SubagentLabel(role, desc)
	}
	return ""
}

func shortPattern(args string) string {
	if p := toolfmt.MatchJSONField(args, "pattern"); p != "" {
		return p
	}
	if p := toolfmt.MatchJSONField(args, "path"); p != "" {
		return p
	}
	return ""
}

func extractURL(args string) string {
	if m := urlFieldRe.FindStringSubmatch(args); len(m) > 1 && m[1] != "" {
		return m[1]
	}
	if m := urlsFieldRe.FindStringSubmatch(args); len(m) > 1 && m[1] != "" {
		return m[1]
	}
	return ""
}

func extractAction(args string) string {
	if m := actionFieldRe.FindStringSubmatch(args); len(m) > 1 && m[1] != "" {
		return m[1]
	}
	return ""
}

var _ agent.View = (*sseView)(nil)
