package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

func registerServeTools(
	srv *mcp.Server,
	mu *sync.Mutex,
	totalTokens *types.Usage,
	promptCount *int,
	buf *observability.BufferingSpanProcessor,
	sessPtr **agentSession,
	sessErrPtr *error,
	ensureSession func() error,
) {
	srv.AddTool(mcp.ServerToolDef{
		Name:        "prompt",
		Description: "Run an agent prompt. Conversation state persists across calls, enabling multi-turn dialogue. Returns the assistant's final response text.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","description":"The user prompt to send to the agent"}},"required":["message"]}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var p struct {
				Message string `json:"message"`
			}
			if len(rawArgs) > 0 {
				if err := json.Unmarshal(rawArgs, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}
			if p.Message == "" {
				return "", fmt.Errorf("message is required")
			}

			mu.Lock()
			if ensureSession != nil {
				if err := ensureSession(); err != nil {
					mu.Unlock()
					return "", fmt.Errorf("session init: %w", err)
				}
			}
			sess := *sessPtr
			mu.Unlock()
			if sess == nil {
				return "", fmt.Errorf("session not ready")
			}
			start := time.Now()
			resp, usage, err := sess.runHeadless(ctx, p.Message)
			mu.Lock()
			totalTokens.PromptTokens += usage.PromptTokens
			totalTokens.CompletionTokens += usage.CompletionTokens
			totalTokens.TotalTokens += usage.TotalTokens
			*promptCount++
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "  %s prompt #%d (%s)\n", Dim("yaah serve:"), *promptCount, formatDuration(time.Since(start)))
			if err != nil {
				return "", err
			}
			return resp, nil
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "traces",
		Description: "Query in-process OpenTelemetry spans captured during prompt execution. Optionally filter by trace_id and render as a parent-child tree.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"trace_id":{"type":"string","description":"Return only spans belonging to this trace ID"},"tree":{"type":"boolean","description":"When true (with trace_id), render spans as a nested parent-child tree"},"limit":{"type":"integer","description":"Return only the most recent N spans (flat mode)"}}}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var p struct {
				TraceID string `json:"trace_id"`
				Tree    bool   `json:"tree"`
				Limit   int    `json:"limit"`
			}
			if len(rawArgs) > 0 {
				if err := json.Unmarshal(rawArgs, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}

			if p.TraceID != "" && p.Tree {
				nodes := buf.TraceTree(p.TraceID)
				return marshalJSON(nodes)
			}

			spans := buf.Traces()
			if p.TraceID != "" {
				filtered := spans[:0]
				for _, s := range spans {
					if s.TraceID == p.TraceID {
						filtered = append(filtered, s)
					}
				}
				spans = filtered
			}
			if p.Limit > 0 && len(spans) > p.Limit {
				spans = spans[len(spans)-p.Limit:]
			}
			return marshalJSON(spans)
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "status",
		Description: "Report the server's provider, model, session ID, conversation length, cumulative token usage, and buffered span count.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			status := map[string]any{
				"prompts":           *promptCount,
				"prompt_tokens":     totalTokens.PromptTokens,
				"completion_tokens": totalTokens.CompletionTokens,
				"total_tokens":      totalTokens.TotalTokens,
				"spans_buffered":    len(buf.Traces()),
				"session_ready":     *sessPtr != nil,
				"pid":               os.Getpid(),
			}
			sess := *sessPtr
			if sess != nil {
				status["provider"] = sess.providerName
				status["model"] = sess.modelName
				status["session_id"] = sess.sessionID
				status["messages"] = len(sess.messages)
			}
			return marshalJSON(status)
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "steer",
		Description: "Inject a high-priority steering message into the next agent turn.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"Steering text to inject into the next LLM request"}},"required":["text"]}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var p struct {
				Text string `json:"text"`
			}
			if len(rawArgs) > 0 {
				if err := json.Unmarshal(rawArgs, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}
			if p.Text == "" {
				return "", fmt.Errorf("text is required")
			}
			mu.Lock()
			sess := *sessPtr
			mu.Unlock()
			if sess == nil {
				return "", fmt.Errorf("session not ready")
			}
			sess.Steer(p.Text)
			return marshalJSON(map[string]string{"result": "steered"})
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "compact",
		Description: "Trigger context compaction on the running session.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			mu.Lock()
			sess := *sessPtr
			mu.Unlock()
			if sess == nil {
				return "", fmt.Errorf("session not ready")
			}
			sess.Compact()
			return marshalJSON(map[string]string{"result": "compacted"})
		},
	})
}
