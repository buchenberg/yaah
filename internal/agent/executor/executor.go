package executor

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Executor runs the tool-execution inner loop for delegated directives.
type Executor struct {
	Provider         llm.Provider
	FallbackProvider llm.Provider
	Model            string
	FallbackModel    string
	MaxIterations    int

	SystemPrompt string

	Registry *tools.Registry

	OnTool     func(name string, args string, dur time.Duration, errMsg string)
	OnUsage    func(usage types.Usage)
	OnFallback func(reason string, model string)
	EmitHook   func(eventName string, model string, extra map[string]interface{})

	OtelEnabled bool
	OtelVerbose bool
	OuterModel  string

	fallbackEmitted bool
}

// Run executes the tool-execution loop for a single delegated directive.
// Returns the final summary, whether the budget was exhausted, whether
// fallback was used, and any error.
func (e *Executor) Run(ctx context.Context, directive, originalIntent, executorType string) (summary string, exhausted bool, fellBack bool, err error) {
	var span trace.Span
	if e.OtelEnabled {
		ctx, span = observability.StartInnerLoop(ctx, directive)
		defer span.End()
	}

	provider, model := e.resolveExecutor(executorType)
	if span != nil {
		span.SetAttributes(
			attribute.String("inner.model", model),
			attribute.Bool("inner.dedicated_provider", e.Provider != nil),
			attribute.String("inner.executor_type", executorType),
		)
		if e.OuterModel != "" {
			span.SetAttributes(attribute.String("outer.model", e.OuterModel))
		}
	}

	payload := directive
	if originalIntent != "" {
		payload += "\n\n## Original user request\n" + originalIntent
	}
	if wd, err := os.Getwd(); err == nil {
		payload += "\n\n## Working directory\n" + wd
	}
	payload += "\n\n## Runtime environment\n" + detectRuntimeEnv()
	messages := []types.Message{
		types.SystemMsg(e.executorPrompt()),
		types.UserMsg(payload),
	}
	if e.OtelVerbose && span != nil {
		observability.RecordInnerTask(span, payload)
		observability.RecordConversation(span, messages)
	}

	tokensBefore := &types.Usage{}
	e.OnUsage(*tokensBefore)

	executorTools := e.buildExecutorToolDefs()
	fellBack = false
	e.fallbackEmitted = false

	for iter := 0; iter < e.MaxIterations; iter++ {
		req := types.ChatRequest{Model: model, Messages: messages, Tools: executorTools}
		msg, err := e.getMessage(ctx, provider, req)
		if err != nil {
			if !fellBack && e.FallbackProvider != nil && e.Provider != nil {
				fellBack = true
				if span != nil {
					span.SetAttributes(
						attribute.Bool("inner.fallback_to_main", true),
						attribute.String("inner.fallback_reason", err.Error()),
					)
					span.AddEvent("fallback.to_main", trace.WithAttributes(
						attribute.String("inner.fallback_reason", err.Error()),
					))
				}
				e.emitFallback(err.Error())
				provider = e.FallbackProvider
				model = e.FallbackModel
				if model == "" {
					model = e.Model
				}
				iter--
				continue
			}
			if span != nil {
				observability.FinishInnerLoop(span, iter+1, false, err)
			}
			return "", false, fellBack, fmt.Errorf("executor: %w", err)
		}
		messages = append(messages, msg)
		if e.OtelVerbose && span != nil {
			observability.RecordAssistantResponse(span, msg, "")
		}

		if len(msg.ToolCalls) == 0 {
			if span != nil {
				observability.FinishInnerLoop(span, iter+1, false, nil)
			}
			return msg.Content, false, fellBack, nil
		}

		for _, tc := range msg.ToolCalls {
			abbr := abbrev(tc.Function.Arguments, 60)
			if e.OnTool != nil {
				e.OnTool(tc.Function.Name, abbr, 0, "")
			}
			content, dur, toolErr := e.executeOneTool(ctx, tc)
			errMsg := ""
			if toolErr != nil {
				errMsg = toolErr.Error()
			}
			if e.OnTool != nil {
				e.OnTool(tc.Function.Name, abbr, dur, errMsg)
			}
			messages = append(messages, types.Message{
				Role:       "tool",
				Content:    content,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
			if toolErr != nil {
				continue
			}
		}
	}

	if span != nil {
		observability.FinishInnerLoop(span, e.MaxIterations, true, nil)
	}
	return "", true, fellBack, fmt.Errorf("executor exhausted after %d iterations", e.MaxIterations)
}

func (e *Executor) resolveExecutor(executorType string) (llm.Provider, string) {
	provider := e.Provider
	if provider == nil {
		provider = e.FallbackProvider
	}
	model := e.Model
	if model == "" {
		model = e.FallbackModel
	}
	return provider, model
}

func (e *Executor) getMessage(ctx context.Context, p llm.Provider, req types.ChatRequest) (types.Message, error) {
	resp, err := p.Send(ctx, req)
	if err != nil {
		return types.Message{}, err
	}
	e.OnUsage(resp.Usage)
	if len(resp.Choices) == 0 {
		return types.Message{}, fmt.Errorf("executor: no choices in response")
	}
	return resp.Choices[0].Message, nil
}

func (e *Executor) executeOneTool(ctx context.Context, tc types.ToolCall) (content string, dur time.Duration, err error) {
	t := e.Registry.Get(tc.Function.Name)
	if t == nil {
		return "", 0, fmt.Errorf("tool %q not found", tc.Function.Name)
	}

	var toolSpan trace.Span
	if e.OtelEnabled {
		ctx, toolSpan = observability.StartTool(ctx, tc.Function.Name, tc.Function.Arguments)
		defer toolSpan.End()
	}

	start := time.Now()
	result, err := t.Execute(ctx, tc.Function.Arguments)
	dur = time.Since(start)

	if toolSpan != nil {
		observability.FinishTool(toolSpan, result, err)
	}

	return result, dur, err
}

func (e *Executor) buildExecutorToolDefs() []types.ToolDef {
	toolNames := e.Registry.List()
	defs := make([]types.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		if name == "task" {
			continue
		}
		t := e.Registry.Get(name)
		if t == nil {
			continue
		}
		defs = append(defs, types.ToolDef{
			Type: "function",
			Function: types.ToolFn{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  []byte(t.Schema()),
			},
		})
	}
	return defs
}

func (e *Executor) executorPrompt() string {
	if e.SystemPrompt != "" {
		return e.SystemPrompt
	}
	return prompts.ExecutorIdentityPrompt
}

func (e *Executor) emitFallback(reason string) {
	if e.fallbackEmitted {
		return
	}
	e.fallbackEmitted = true
	if e.OnFallback != nil {
		e.OnFallback(reason, e.OuterModel)
	}
	if e.EmitHook != nil {
		e.EmitHook("executor.fallback", e.OuterModel, map[string]interface{}{
			"fallback_reason": reason,
		})
	}
}

func detectRuntimeEnv() string {
	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = "powershell (pwsh 7+ or Windows PowerShell)"
	}
	return fmt.Sprintf("OS: %s/%s. Default shell: %s. Prefer the default shell for commands.", runtime.GOOS, runtime.GOARCH, shell)
}

func abbrev(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen-3]) + "..."
}
