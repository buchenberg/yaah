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
	"github.com/buchenberg/yaah/internal/worldinfo"
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

	// World posture, flat on the header frame so the Alpine client can read
	// it without a nested shape. Every field is omitted when the session
	// never derived a status (the zero value), which hides the badge.
	Workspace     string `json:"workspace,omitempty"`
	Isolated      bool   `json:"isolated"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Containment   string `json:"containment,omitempty"`
	Jail          bool   `json:"jail"`
	Network       string `json:"network,omitempty"`
	World         string `json:"world,omitempty"`
	WorldResolved string `json:"worldResolved,omitempty"`
	WorldReport   string `json:"worldReport,omitempty"`
	WorldDegraded bool   `json:"worldDegraded,omitempty"`
	WorldReason   string `json:"worldReason,omitempty"`
}

// wireWorldMeta is the world subset the view caches so a model switch
// (SetHeader) re-sends the posture unchanged. It flattens onto
// wireHeaderMeta when the frame is built.
type wireWorldMeta struct {
	Workspace     string
	Isolated      bool
	WorkspaceRoot string
	Mode          string
	Containment   string
	Jail          bool
	Network       string
	World         string
	WorldResolved string
	WorldReport   string
	WorldDegraded bool
	WorldReason   string
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

	World         string `json:"world,omitempty"`
	WorldDegraded bool   `json:"world_degraded,omitempty"`
	WorldReason   string `json:"world_reason,omitempty"`

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
	v.writeLocked(marshalWire(sseWireEvent{Type: "ctrl.header", Meta: v.headerLocked()}))
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
	v.writeLocked(marshalWire(sseWireEvent{Type: "ctrl.header", Meta: v.headerLocked()}))
}

type sseView struct {
	w  http.ResponseWriter
	mu sync.Mutex

	// header info set from the session on SSE connect
	provider   string
	model      string
	mcpServers []wireMCPServer
	// world is the session posture, populated once when the view is built and
	// re-sent unchanged by SetHeader.
	world wireWorldMeta

	// current todo state — updated by forwardCtrl; sent on SSE connect
	todos []wireTodo
}

// AcceptsEventsBetweenRuns reports that writes are mutex-guarded and can be
// delivered from the background-job goroutine outside a run.
func (v *sseView) AcceptsEventsBetweenRuns() bool { return true }

// headerLocked builds the header frame from cached state, assuming v.mu is
// held. Keeping one builder is what makes SetHeader preserve the world.
func (v *sseView) headerLocked() *wireHeaderMeta {
	h := &wireHeaderMeta{
		Provider:   v.provider,
		Model:      v.model,
		MCPServers: v.mcpServers,
	}
	h.Workspace = v.world.Workspace
	h.Isolated = v.world.Isolated
	h.WorkspaceRoot = v.world.WorkspaceRoot
	h.Mode = v.world.Mode
	h.Containment = v.world.Containment
	h.Jail = v.world.Jail
	h.Network = v.world.Network
	h.World = v.world.World
	h.WorldResolved = v.world.WorldResolved
	h.WorldReport = v.world.WorldReport
	h.WorldDegraded = v.world.WorldDegraded
	h.WorldReason = v.world.WorldReason
	return h
}

// worldHeaderOf flattens a derived status onto the cached header shape. The
// zero status maps to the zero value, so the client's badge is hidden.
func worldHeaderOf(s worldinfo.Status) wireWorldMeta {
	if s.IsZero() {
		return wireWorldMeta{}
	}
	return wireWorldMeta{
		Workspace:     s.Backend,
		Isolated:      s.Isolated,
		WorkspaceRoot: s.Root,
		Mode:          s.Mode,
		Containment:   s.Containment,
		Jail:          s.JailOK,
		Network:       s.Network,
		World:         s.WorldReq,
		WorldResolved: s.ProposalWorld,
		WorldReport:   reportLane(s),
		WorldDegraded: s.Degraded,
		WorldReason:   s.Reason,
	}
}

// reportLane is the report-lane resolution when it differs from the proposal
// lane; empty otherwise, so the header stays flat and quiet by default.
func reportLane(s worldinfo.Status) string {
	if s.ReportWorld == s.ProposalWorld {
		return ""
	}
	return s.ReportWorld
}

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
		we = sseWireEvent{
			Type: "subagent.start", Role: e.Role, Model: e.Model, Prompt: e.Prompt,
			World: e.World, WorldDegraded: e.WorldDegraded, WorldReason: e.WorldReason,
		}
	case *agent.SubAgentEndEvent:
		we = sseWireEvent{Type: "subagent.end", Role: e.Role, Model: e.Model,
			Prompt: e.Prompt, Ms: e.Duration.Milliseconds(), Error: e.Error,
			World: e.World, WorldDegraded: e.WorldDegraded, WorldReason: e.WorldReason}
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
	// This loop must never park on a pending answer. Every frame is delivered
	// straight through and each prompt waits for its answer in its own
	// goroutine: blocking here would stall later control frames and, once the
	// channel filled, the producers feeding it — turning an unanswered dialog
	// into a stalled agent.
	//
	// The cost is that two prompts arriving together both emit, and the
	// browser's single dialog shows the later one. That is a pre-existing
	// limitation of the client, not something this loop can fix; queueing
	// modals client-side is the follow-up. What this loop does guarantee is
	// that no answer id outlives its prompt (see awaitModal).
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *control.Status:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.status", Text: m.Text}))
			case *control.CompactionSummary:
				block := m.Header()
				if m.Summary != "" {
					block += "\n\n" + m.Summary
				} else {
					block += " — no summary produced"
				}
				v.write(marshalWire(sseWireEvent{Type: "ctrl.compaction", Text: block}))
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
				// Detached so the loop keeps draining: holding it here
				// would stall delivery of every later control frame (and
				// the producers feeding the channel) until this prompt was
				// answered. awaitModal still releases the id on every exit
				// path, so the map cannot grow without bound.
				answerCh := m.AnswerCh
				go func() {
					if ans, ok := awaitModal(ctx, am, id, ansCh); ok {
						answerCh <- ans
					}
					// Stream gone: the question tool's own timeout
					// supplies the fallback.
				}()
			case *control.Approval:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				v.write(marshalWire(sseWireEvent{
					Type: "ctrl.approval", ID: id,
					Name: m.Name, Args: m.Args,
				}))
				approveCh := m.ApproveCh
				go func() {
					if ans, ok := awaitModal(ctx, am, id, ansCh); ok {
						approveCh <- (ans == "true")
					}
				}()
			case *control.Continue:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				v.write(marshalWire(sseWireEvent{
					Type:    "ctrl.continue",
					ID:      id,
					MaxIter: m.MaxIter,
				}))
				contCh := m.AnswerCh
				go func() {
					ans, ok := awaitModal(ctx, am, id, ansCh)
					if !ok {
						// Stream torn down unanswered: tell the agent to
						// stop rather than leave it waiting on an answer
						// that can no longer arrive (the caller buffers
						// this channel, so the send cannot block).
						contCh <- false
						return
					}
					contCh <- (ans == "yes")
				}()
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
