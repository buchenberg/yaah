package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

// Session is the narrow capability the ACP server needs from an agent
// session. *agentSession in cmd/yaah satisfies this interface.
type Session interface {
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	SetView(agent.View)
	SetCtrlCh(chan<- control.Msg)
	GetCtrlCh() chan<- control.Msg
	Close()
	ToolReg() *tools.Registry
}

// Server is an ACP (Agent Communication Protocol) server speaking
// newline-delimited JSON-RPC 2.0 over stdin/stdout. All diagnostics go to
// the logf sink so stdout stays clean for protocol JSON.
type Server struct {
	sess    Session
	version string
	logf    func(format string, args ...any)
	stdin   io.Reader
	stdout  io.Writer

	// AutoAnswerQuestions, when true, answers CtrlQuestion control
	// messages with the first option (ACP is a machine-to-machine
	// protocol without interactive users). Default true.
	AutoAnswerQuestions bool

	// AutoContinue, when true, answers CtrlContinue control messages
	// with "continue" when max iterations are reached. Default true.
	AutoContinue bool
}

// NewServer creates an ACP server bound to sess, reading from os.Stdin
// and writing protocol JSON to os.Stdout. logf receives diagnostic
// messages (may be nil).
func NewServer(sess Session, version string, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{
		sess:                sess,
		version:             version,
		logf:                logf,
		stdin:               os.Stdin,
		stdout:              os.Stdout,
		AutoAnswerQuestions: true,
		AutoContinue:        true,
	}
}

// Run starts the dispatch loop and blocks until ctx is cancelled or stdin
// reaches EOF.
func (s *Server) Run(ctx context.Context) error {
	// Wire question tool handler for ACP. Questions are formatted as
	// agent_message_chunk text and auto-answered with the first option
	// (ACP is a machine-to-machine protocol without interactive users).
	if reg := s.sess.ToolReg(); reg != nil {
		if qt := reg.Get("question"); qt != nil {
			if qtp, ok := qt.(*tools.QuestionTool); ok {
				qtp.Handler = func(entries []tools.QuestionEntry) []string {
					var answers []string
					for _, e := range entries {
						// Format question text for the ACP client.
						msg := fmt.Sprintf("❓ %s\n\n%s\n\n", e.Header, e.Question)
						for i, o := range e.Options {
							msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
						}
						// Send as a status message so the client sees it.
						if ch := s.sess.GetCtrlCh(); ch != nil {
							ch <- &control.Status{Text: msg}
						}
						// Auto-answer: pick the first option.
						if s.AutoAnswerQuestions && len(e.Options) > 0 {
							answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
						} else {
							answers = append(answers, e.Header+": ")
						}
					}
					return answers
				}
			}
		}
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	reader := bufio.NewReaderSize(s.stdin, 1<<20)

	// writeLock serializes writes to stdout so JSON-RPC messages don't
	// interleave from multiple goroutines.
	var writeLock sync.Mutex
	writeMsg := func(msg Message) {
		data, _ := json.Marshal(msg)
		writeLock.Lock()
		fmt.Fprintln(s.stdout, string(data))
		writeLock.Unlock()
	}

	writeRaw := func(data []byte) {
		writeLock.Lock()
		fmt.Fprintln(s.stdout, string(data))
		writeLock.Unlock()
	}

	sendUpdate := func(sessionID string, update Update) {
		msg := SessionUpdateMsg{
			JSONRPC: "2.0",
			Method:  "session/update",
			Params: SessionUpdate{
				SessionID: sessionID,
				Update:    update,
			},
		}
		data, _ := json.Marshal(msg)
		writeRaw(data)
	}

	var currentPromptCancel context.CancelFunc
	var currentPromptDone chan struct{}
	var promptMu sync.Mutex

	// promptResults carries results from completed prompt runs
	type promptResult struct {
		sessionID string
		response  string
		err       error
	}
	promptResults := make(chan promptResult, 4)

	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					s.logf("read error: %v", err)
				}
				return
			}

			var msg Message
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				s.logf("invalid JSON: %v", err)
				continue
			}

			switch msg.Method {
			case "initialize":
				result := InitializeResult{
					ProtocolVersion: "2024-11-05",
					Capabilities: ServerCaps{
						Tools: &ToolsCaps{ListChanged: false},
					},
					ServerInfo: ServerInfo{
						Name:    "yaah",
						Version: s.version,
					},
				}
				respData, _ := json.Marshal(result)
				writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "notifications/initialized":

			case "session/new":
				sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
				result := SessionNewResult{SessionID: sessionID}
				respData, _ := json.Marshal(result)
				writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "session/prompt":
				var params PromptParams
				if len(msg.Params) > 0 {
					json.Unmarshal(msg.Params, &params)
				}

				promptText := ""
				for _, b := range params.Prompt {
					if b.Type == "text" {
						promptText += b.Text
					}
				}
				if promptText == "" {
					writeMsg(Message{
						JSONRPC: "2.0", ID: msg.ID,
						Error: &Error{Code: -32602, Message: "prompt must contain at least one text block"},
					})
					continue
				}

				// Acknowledge the prompt immediately
				writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)})

				sessionID := params.SessionID

				promptCtx, promptCancel := context.WithCancel(ctx)

				promptMu.Lock()
				if currentPromptCancel != nil {
					currentPromptCancel()
				}
				done := currentPromptDone
				currentPromptDone = make(chan struct{}, 1)
				currentPromptCancel = promptCancel
				promptMu.Unlock()

				if done != nil {
					<-done
				}

				wrapped := NewViewWithWrite(sendUpdate, sessionID)

				ctrlCh := make(chan control.Msg, 64)
				s.sess.SetView(wrapped)
				s.sess.SetCtrlCh(ctrlCh)

				go s.forwardCtrl(promptCtx, ctrlCh, sessionID, sendUpdate)

				go func(sID string) {
					defer func() { currentPromptDone <- struct{}{} }()
					resp, _, runErr := s.sess.RunPrompt(promptCtx, promptText)
					promptResults <- promptResult{sessionID: sID, response: resp, err: runErr}
				}(sessionID)

			case "session/cancel":
				promptMu.Lock()
				if currentPromptCancel != nil {
					currentPromptCancel()
				}
				promptMu.Unlock()

			case "session/set_mode":
				writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)})

			case "tools/list":
				entries := make([]ToolListEntry, 0)
				if s.sess != nil {
					reg := s.sess.ToolReg()
					if reg != nil {
						for _, name := range reg.List() {
							t := reg.Get(name)
							if t == nil {
								continue
							}
							schema := t.Schema()
							var schemaRaw json.RawMessage
							if len(schema) > 0 {
								schemaRaw = schema
							} else {
								schemaRaw = json.RawMessage(`{"type":"object","properties":{}}`)
							}
							entries = append(entries, ToolListEntry{
								Name:        t.Name(),
								Description: t.Description(),
								InputSchema: schemaRaw,
							})
						}
					}
				}
				result := map[string]any{
					"tools":      entries,
					"nextCursor": "",
				}
				respData, _ := json.Marshal(result)
				writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "tools/call":
				var params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if len(msg.Params) > 0 {
					if err := json.Unmarshal(msg.Params, &params); err != nil {
						writeMsg(Message{
							JSONRPC: "2.0", ID: msg.ID,
							Error: &Error{Code: -32602, Message: "invalid params: " + err.Error()},
						})
						continue
					}
				}

				reg := s.sess.ToolReg()
				if reg == nil {
					resultData := json.RawMessage(`{"content":[{"type":"text","text":"session not ready"}],"isError":true}`)
					writeMsg(Message{JSONRPC: "2.0", ID: msg.ID, Result: resultData})
					continue
				}

				go func(id any, name string, args map[string]any) {
					callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
					defer cancel()

					var resultData json.RawMessage
					argsJSON, _ := json.Marshal(args)
					r, execErr := reg.Execute(callCtx, name, string(argsJSON))
					blocks := []map[string]any{
						{"type": "text", "text": r},
					}
					isErr := execErr != nil
					if isErr {
						blocks[0]["text"] = execErr.Error()
					}
					result := map[string]any{
						"content": blocks,
						"isError": isErr,
					}
					resultData, _ = json.Marshal(result)
					writeMsg(Message{JSONRPC: "2.0", ID: id, Result: resultData})
				}(msg.ID, params.Name, params.Arguments)
			}
		}
	}()

	// Background goroutine to clean up after completed prompts
	go func() {
		for {
			select {
			case pr := <-promptResults:
				if pr.err != nil {
					s.logf("prompt error: %v", pr.err)
				} else {
					s.logf("prompt completed")
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}

	s.logf("shutting down")
	return nil
}
