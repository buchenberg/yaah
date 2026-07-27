package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/types"
)

type wireOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type wireTodo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

type sseWireEvent struct {
	Type string `json:"type"`

	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`

	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
	Error  string `json:"error,omitempty"`

	Role   string `json:"role,omitempty"`
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt,omitempty"`

	Response      string `json:"response,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`

	ID       string       `json:"id,omitempty"`
	Header   string       `json:"header,omitempty"`
	Question string       `json:"question,omitempty"`
	Options  []wireOption `json:"options,omitempty"`
	Multi    bool         `json:"multi,omitempty"`

	Items []wireTodo `json:"items,omitempty"`

	Tokens int `json:"tokens,omitempty"`
	Window int `json:"window,omitempty"`

	Models    []string          `json:"models,omitempty"`
	Providers map[string]string `json:"providers,omitempty"`
}

func marshalWire(we sseWireEvent) []byte {
	data, _ := json.Marshal(we)
	return data
}

type sseView struct {
	w  http.ResponseWriter
	mu sync.Mutex
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
		we = sseWireEvent{Type: "tool.start", Name: e.Name, Args: e.Args}
	case *agent.ToolEndEvent:
		we = sseWireEvent{Type: "tool.end", Name: e.Name, Args: e.Args,
			Result: e.Result, Ms: e.Duration.Milliseconds(), Error: e.Error}
	case *agent.SubAgentStartEvent:
		we = sseWireEvent{Type: "subagent.start", Role: e.Role, Model: e.Model, Prompt: e.Prompt}
	case *agent.SubAgentEndEvent:
		we = sseWireEvent{Type: "subagent.end", Role: e.Role, Model: e.Model,
			Prompt: e.Prompt, Ms: e.Duration.Milliseconds(), Error: e.Error}
	case *agent.DoneEvent:
		we = sseWireEvent{Type: "done", Response: e.Response, Error: e.Error,
			ContextTokens: e.ContextTokens, ContextWindow: e.ContextWindow}
	case *agent.CompactionStartedEvent:
		we = sseWireEvent{Type: "ctrl.status", Text: fmt.Sprintf("compacting (%d→%d tokens, %s)", e.BeforeTokens, e.TargetTokens, e.Reason)}
	case *agent.CompactionDoneEvent:
		we = sseWireEvent{Type: "ctrl.status", Text: fmt.Sprintf("compacted %.0f%% (%d→%d, %s, %.1fs)",
			e.SavingsPct*100, e.BeforeTokens, e.AfterTokens, e.Method, e.ElapsedSeconds)}
	}
	v.write(marshalWire(we))
}

func (v *sseView) write(data []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
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

func forwardCtrl(ctx context.Context, ch <-chan types.CtrlMsg, v *sseView, am *answerMap, idGen *atomic.Int64) {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *types.CtrlStatus:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.status", Text: m.Text}))
			case *types.CtrlError:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.error", Error: m.Err.Error()}))
			case *types.CtrlQuestion:
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
				go func() {
					select {
					case ans := <-ansCh:
						m.AnswerCh <- ans
					case <-ctx.Done():
					}
				}()
			case *types.CtrlApproval:
				id := fmt.Sprintf("q%d", idGen.Add(1))
				ansCh := make(chan string, 1)
				am.register(id, ansCh)
				v.write(marshalWire(sseWireEvent{
					Type: "ctrl.approval", ID: id,
					Name: m.Name, Args: m.Args,
				}))
				go func() {
					select {
					case ans := <-ansCh:
						m.ApproveCh <- (ans == "true")
					case <-ctx.Done():
					}
				}()
			case *types.CtrlTodos:
				items := make([]wireTodo, len(m.Items))
				for i, item := range m.Items {
					items[i] = wireTodo{Content: item.Content, Status: item.Status, Priority: item.Priority}
				}
				v.write(marshalWire(sseWireEvent{Type: "ctrl.todos", Items: items}))
			case *types.CtrlContextInfo:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.context", Tokens: m.Tokens, Window: m.Window}))
			case *types.CtrlModelList:
				v.write(marshalWire(sseWireEvent{Type: "ctrl.models", Models: m.Models, Providers: m.ProviderNames}))
			case *types.CtrlDone:
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

var _ agent.View = (*sseView)(nil)
