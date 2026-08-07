// Package jobs provides the background sub-agent job manager and the
// structured I/O contract between sub-agents and their callers: dispatch
// params, parsed output, context-key helpers, and lifecycle tracking.
// It is infrastructure, not a tool, and does not import internal/tools.
package jobs

import "context"

// TaskRunner runs a sub-agent for a given prompt and role configuration
// and returns its final response. The runner is responsible for building
// the role-specific tool registry and Loop.
type TaskRunner func(ctx context.Context, prompt string, params SubAgentParams) (string, error)
