package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
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
	w         http.ResponseWriter
	mu        sync.Mutex
	toolIDGen atomic.Int64
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
		id := v.toolIDGen.Add(1)
		we = sseWireEvent{
			Type: "tool.start", ToolID: id,
			Name: e.Name, Args: e.Args,
			Summary: toolStartSummary(e.Name, e.Args),
		}
	case *agent.ToolEndEvent:
		we = sseWireEvent{
			Type: "tool.end",
			Name: e.Name, Args: e.Args,
			Result: e.Result, Ms: e.Duration.Milliseconds(), Error: e.Error,
			Summary: toolSummary(e.Name, e.Args, e.Result),
		}
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

// --- tool summary helpers (ported from internal/tui/tool_component.go) ---

var (
	grepMatchRe   = regexp.MustCompile(`^(\d+):`)
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
		return shortFile(args)
	case "write":
		return "→ " + shortFile(args)
	case "edit":
		return shortFile(args)
	case "delete":
		return shortFile(args)
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
	case "spawn_subagent":
		role := matchJSONFieldStr(args, "role")
		desc := matchJSONFieldStr(args, "description")
		return subagentSummary(role, desc)
	}
	return ""
}

// toolSummary returns a one-line description of a tool's result,
// following the TUI's toolSummary patterns in internal/tui/tool_component.go.
func toolSummary(name, args, content string) string {
	switch name {
	case "grep":
		return grepSum(content)
	case "glob":
		return globSum(content)
	case "ls":
		return lsSum(content)
	case "bash":
		return bashSum(content)
	case "read":
		return fmt.Sprintf("read %s (%s chars)", shortFile(args), formatNum(len(content)))
	case "write":
		return fmt.Sprintf("wrote %s (%s chars)", shortFile(args), formatNum(len(content)))
	case "edit":
		return "edited " + shortFile(args)
	case "delete":
		return "deleted " + shortFile(args)
	case "http":
		if u := extractURL(args); u != "" {
			return u
		}
	case "webfetch":
		if u := extractURL(args); u != "" {
			return u
		}
	case "git":
		if a := extractAction(args); a != "" {
			return a
		}
	case "replace":
		return "replaced in " + shortFile(args)
	case "spawn_subagent":
		role := matchJSONFieldStr(args, "role")
		desc := matchJSONFieldStr(args, "description")
		return subagentSummary(role, desc)
	default:
		firstLine, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
		if len(firstLine) > 80 {
			return firstLine[:77] + "..."
		}
		return firstLine
	}
	return ""
}

func grepSum(content string) string {
	if content == "" {
		return "0 matches"
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	totalLines := len(lines)
	matches := 0
	matchLines := 0
	for _, line := range lines {
		if grepMatchRe.MatchString(line) {
			matchLines++
			matches += strings.Count(line, "\x1b[31m")
		}
	}
	if matches == 0 {
		matches = matchLines
	}
	files := totalLines - matchLines
	if files < 0 {
		files = 0
	}
	return fmt.Sprintf("%d matches in %d files", matches, files)
}

func globSum(content string) string {
	lines := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
	if content == "" {
		lines = 0
	}
	if lines == 0 {
		return "0 files"
	}
	return fmt.Sprintf("%d files", lines)
}

func lsSum(content string) string {
	lines := strings.Count(content, "\n")
	if content == "" {
		return "0 entries"
	}
	return fmt.Sprintf("%d entries", lines+1)
}

func bashSum(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	firstLine, _, _ := strings.Cut(trimmed, "\n")
	if len(firstLine) > 60 {
		return firstLine[:57] + "..."
	}
	return firstLine
}

func shortFile(args string) string {
	fp := matchJSONFieldStr(args, "filePath")
	if fp == "" {
		fp = matchJSONFieldStr(args, "path")
	}
	if fp == "" {
		return ""
	}
	parts := strings.Split(fp, "/")
	return parts[len(parts)-1]
}

func shortPattern(args string) string {
	if p := matchJSONFieldStr(args, "pattern"); p != "" {
		return p
	}
	if p := matchJSONFieldStr(args, "path"); p != "" {
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

func subagentSummary(role, desc string) string {
	displayName := subagent.RoleDisplayName(subagent.SubAgentRole(role))
	specialty := subagent.RoleSpecialty(subagent.SubAgentRole(role))
	label := displayName
	if specialty != "" {
		label += " — " + specialty
	}
	switch {
	case role != "" && desc != "":
		return "sub-agent: " + label + " · " + desc
	case desc != "":
		return "sub-agent · " + desc
	case role != "":
		return "sub-agent: " + label
	default:
		return "sub-agent"
	}
}

func matchJSONFieldStr(jsonStr, field string) string {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(jsonStr); len(m) > 1 {
		return m[1]
	}
	return ""
}

func formatNum(n int) string {
	s := strconv.Itoa(n)
	var result strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteRune(',')
		}
		result.WriteRune(c)
	}
	return result.String()
}

var _ agent.View = (*sseView)(nil)
