package agent

import (
	"os"
	"sort"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
)

// traceSessionContext builds the immutable session-start context from the
// turn's resolved step. The system prompt is the assembled one the provider
// saw, not the raw config field; the tool list is the effective belt; the
// pipeline names are the exact middleware composition this loop runs.
func (l *Loop) traceSessionContext(step *pipeline.Step, pipe *pipeline.Pipeline) pipeline.TraceSessionContext {
	cwd := l.Config.TraceCWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	names := make([]string, 0, len(step.Tools))
	for _, tool := range step.Tools {
		if tool.Function.Name != "" {
			names = append(names, tool.Function.Name)
		}
	}
	sort.Strings(names)
	var pipelineNames []string
	if pipe != nil {
		pipelineNames = pipe.MiddlewareNames()
	}
	return pipeline.TraceSessionContext{
		SystemPrompt:  step.SystemPrompt,
		CWD:           cwd,
		Model:         step.Model,
		Provider:      l.Config.ProviderName,
		ToolNames:     names,
		PipelineNames: pipelineNames,
	}
}
