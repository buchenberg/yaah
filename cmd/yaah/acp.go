package yaah

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
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/spf13/cobra"
)

// === JSON-RPC types for ACP ===

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpInitializeResult struct {
	ProtocolVersion string        `json:"protocol_version"`
	Capabilities    acpServerCaps `json:"capabilities"`
	ServerInfo      acpServerInfo `json:"server_info"`
	Instructions    string        `json:"instructions,omitempty"`
}

type acpServerCaps struct {
	Tools     *acpToolsCaps `json:"tools,omitempty"`
	Resources *struct{}     `json:"resources,omitempty"`
}

type acpToolsCaps struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type acpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type acpSessionNewResult struct {
	SessionID string        `json:"sessionId"`
	Modes     *acpModeState `json:"modes,omitempty"`
}

type acpModeState struct {
	CurrentModeID  string    `json:"currentModeId"`
	AvailableModes []acpMode `json:"availableModes"`
}

type acpMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type acpPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []acpContentBlock `json:"prompt"`
}

type acpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type acpToolListEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// === ACP serve command ===

var acpServeCmd = &cobra.Command{
	Use:   "acp-serve",
	Short: "Expose yaah as an ACP (Agent Communication Protocol) server over stdio",
	Long: `acp-serve starts yaah as an ACP protocol server over stdio,
implementing the Agent Communication Protocol used by Gas Town and other
orchestrators.

The protocol uses newline-delimited JSON-RPC 2.0 on stdin/stdout. All
diagnostics go to stderr so stdout stays clean for protocol JSON.

Methods implemented:
  initialize                  Handshake with client
  notifications/initialized   Client confirms init complete
  session/new                 Create a conversation session
  session/prompt              Send a prompt, receive streaming updates
  session/cancel              Cancel the current turn
  session/set_mode            Change agent mode (also used as heartbeat)
  tools/list                  List available tools
  tools/call                  Execute a tool`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runACPServe,
}

func init() {
	rootCmd.AddCommand(acpServeCmd)
}

// acpViewWithWrite wraps acpView and sends session/update notifications
// for each event, tagging them with the active session ID.
type acpViewWithWrite struct {
	av        *acpView
	send      func(sessionID string, update acpUpdate)
	sessionID string
}

func (v *acpViewWithWrite) HandleEvent(evt agent.Event) {
	v.av.sendTo(v.sessionID, v.send, evt)
}

func runACPServe(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(os.Stderr, "%s starting ACP server (stdio)...\n", Dim("yaah acp-serve:"))

	sess, err := newAgentSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()

	// Wire question tool handler for ACP. Questions are formatted as
	// agent_message_chunk text and auto-answered with the first option
	// (ACP is a machine-to-machine protocol without interactive users).
	if qt := sess.toolReg.Get("question"); qt != nil {
		qtp := qt.(*tools.QuestionTool)
		qtp.Handler = func(entries []tools.QuestionEntry) []string {
			var answers []string
			for _, e := range entries {
				// Format question text for the ACP client.
				msg := fmt.Sprintf("❓ %s\n\n%s\n\n", e.Header, e.Question)
				for i, o := range e.Options {
					msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
				}
				// Send as a status message so the client sees it.
				if ch := sess.GetCtrlCh(); ch != nil {
					ch <- &types.CtrlStatus{Text: msg}
				}
				// Auto-answer: pick the first option.
				if len(e.Options) > 0 {
					answers = append(answers, fmt.Sprintf("%s: %s", e.Header, e.Options[0].Label))
				} else {
					answers = append(answers, e.Header+": ")
				}
			}
			return answers
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reader := bufio.NewReaderSize(os.Stdin, 1<<20)

	// writeLock serializes writes to stdout so JSON-RPC messages don't
	// interleave from multiple goroutines.
	var writeLock sync.Mutex
	writeMsg := func(msg acpMessage) {
		data, _ := json.Marshal(msg)
		writeLock.Lock()
		fmt.Fprintln(os.Stdout, string(data))
		writeLock.Unlock()
	}

	writeRaw := func(data []byte) {
		writeLock.Lock()
		fmt.Fprintln(os.Stdout, string(data))
		writeLock.Unlock()
	}

	sendUpdate := func(sessionID string, update acpUpdate) {
		msg := acpSessionUpdateMsg{
			JSONRPC: "2.0",
			Method:  "session/update",
			Params: acpSessionUpdate{
				SessionID: sessionID,
				Update:    update,
			},
		}
		data, _ := json.Marshal(msg)
		writeRaw(data)
	}

	var currentPromptCancel context.CancelFunc
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
					fmt.Fprintf(os.Stderr, "%s read error: %v\n", Dim("yaah acp-serve:"), err)
				}
				return
			}

			var msg acpMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				fmt.Fprintf(os.Stderr, "%s invalid JSON: %v\n", Dim("yaah acp-serve:"), err)
				continue
			}

			switch msg.Method {
			case "initialize":
				result := acpInitializeResult{
					ProtocolVersion: "2024-11-05",
					Capabilities: acpServerCaps{
						Tools: &acpToolsCaps{ListChanged: false},
					},
					ServerInfo: acpServerInfo{
						Name:    "yaah",
						Version: version,
					},
				}
				respData, _ := json.Marshal(result)
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "notifications/initialized":

			case "session/new":
				sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
				result := acpSessionNewResult{SessionID: sessionID}
				respData, _ := json.Marshal(result)
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "session/prompt":
				var params acpPromptParams
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
					writeMsg(acpMessage{
						JSONRPC: "2.0", ID: msg.ID,
						Error: &acpError{Code: -32602, Message: "prompt must contain at least one text block"},
					})
					continue
				}

				// Acknowledge the prompt immediately
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)})

				sessionID := params.SessionID

				promptCtx, promptCancel := context.WithCancel(ctx)

				promptMu.Lock()
				if currentPromptCancel != nil {
					currentPromptCancel()
				}
				currentPromptCancel = promptCancel
				promptMu.Unlock()

				av := newACPView()
				wrapped := &acpViewWithWrite{
					av:        av,
					send:      sendUpdate,
					sessionID: sessionID,
				}

				ctrlCh := make(chan types.CtrlMsg, 64)
				sess.SetView(wrapped)
				sess.SetCtrlCh(ctrlCh)

				go forwardACPCtrl(promptCtx, ctrlCh, sessionID, sendUpdate)

				go func(sID string) {
					resp, _, runErr := sess.RunPrompt(promptCtx, promptText)
					promptResults <- promptResult{sessionID: sID, response: resp, err: runErr}
				}(sessionID)

			case "session/cancel":
				promptMu.Lock()
				if currentPromptCancel != nil {
					currentPromptCancel()
				}
				promptMu.Unlock()

			case "session/set_mode":
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)})

			case "tools/list":
				entries := make([]acpToolListEntry, 0)
				if sess != nil {
					reg := sess.toolReg
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
							entries = append(entries, acpToolListEntry{
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
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: respData})

			case "tools/call":
				var params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if len(msg.Params) > 0 {
					json.Unmarshal(msg.Params, &params)
				}

				var resultData json.RawMessage
				if sess != nil && sess.toolReg != nil {
					argsJSON, _ := json.Marshal(params.Arguments)
					r, err := sess.toolReg.Execute(ctx, params.Name, string(argsJSON))
					blocks := []map[string]any{
						{"type": "text", "text": r},
					}
					isErr := err != nil
					if isErr {
						blocks[0]["text"] = err.Error()
					}
					result := map[string]any{
						"content": blocks,
						"isError": isErr,
					}
					resultData, _ = json.Marshal(result)
				} else {
					resultData = json.RawMessage(`{"content":[{"type":"text","text":"session not ready"}],"isError":true}`)
				}
				writeMsg(acpMessage{JSONRPC: "2.0", ID: msg.ID, Result: resultData})
			}
		}
	}()

	// Background goroutine to clean up after completed prompts
	go func() {
		for {
			select {
			case pr := <-promptResults:
				if pr.err != nil {
					fmt.Fprintf(os.Stderr, "%s prompt error: %v\n", Dim("yaah acp-serve:"), pr.err)
				} else {
					fmt.Fprintf(os.Stderr, "%s prompt completed\n", Dim("yaah acp-serve:"))
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

	fmt.Fprintf(os.Stderr, "%s shutting down\n", Dim("yaah acp-serve:"))
	return nil
}

func forwardACPCtrl(ctx context.Context, ch <-chan types.CtrlMsg, sessionID string, send func(string, acpUpdate)) {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *types.CtrlStatus:
				send(sessionID, acpUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acpContent{Type: "text", Text: m.Text},
				})
			case *types.CtrlError:
				send(sessionID, acpUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acpContent{Type: "text", Text: fmt.Sprintf("error: %v", m.Err)},
				})
			case *types.CtrlContinue:
				// Inform the client and auto-continue.
				msg := fmt.Sprintf("Max iterations (%d) reached — continuing.", m.MaxIter)
				send(sessionID, acpUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acpContent{Type: "text", Text: msg},
				})
				if m.AnswerCh != nil {
					select {
					case m.AnswerCh <- true:
					default:
					}
				}
			case *types.CtrlDone:
				return
			case *types.CtrlContextInfo:
				send(sessionID, acpUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acpContent{Type: "text", Text: fmt.Sprintf("[context: %d/%d tokens]", m.Tokens, m.Window)},
				})
			case *types.CtrlQuestion:
				// Format and send the question text.
				msg := fmt.Sprintf("❓ %s\n\n%s\n\n", m.Header, m.Question)
				for i, o := range m.Options {
					msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
				}
				send(sessionID, acpUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acpContent{Type: "text", Text: msg},
				})
				// Auto-answer with the first option.
				if m.AnswerCh != nil {
					if len(m.Options) > 0 {
						select {
						case m.AnswerCh <- m.Options[0].Label:
						default:
						}
					} else {
						select {
						case m.AnswerCh <- "":
						default:
						}
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// compile-time check
var _ agent.View = (*acpViewWithWrite)(nil)
