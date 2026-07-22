package pipeline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// PermissionRule matches a tool name and a path glob pattern against a permission mode.
type PermissionRule struct {
	Tool string `json:"tool"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// PermissionMiddleware filters tool calls based on path-pattern rules.
type PermissionMiddleware struct {
	rules []PermissionRule
}

func (m *PermissionMiddleware) Name() string { return "permission" }

func (m *PermissionMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *PermissionMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if len(m.rules) == 0 || len(msg.ToolCalls) == 0 {
		return step, nil
	}

	filtered := make([]types.ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		paths := extractPaths(tc.Function.Name, tc.Function.Arguments)
		if m.matchRules(tc.Function.Name, paths) == "deny" {
			continue
		}
		filtered = append(filtered, tc)
	}
	msg.ToolCalls = filtered
	return step, nil
}

func (m *PermissionMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}

func (m *PermissionMiddleware) matchRules(toolName string, paths []string) string {
	for _, rule := range m.rules {
		if rule.Tool != "" && rule.Tool != toolName {
			continue
		}
		if rule.Path != "" && len(paths) > 0 {
			for _, p := range paths {
				matched, _ := filepath.Match(rule.Path, p)
				if matched {
					return rule.Mode
				}
			}
		} else if rule.Path == "" {
			return rule.Mode
		}
	}
	return "allow"
}

func extractPaths(toolName, argsJSON string) []string {
	var paths []string
	switch toolName {
	case "read", "ls":
		if p := jsonGet(argsJSON, "path"); p != "" {
			paths = append(paths, p)
		}
	case "write", "edit", "delete":
		if p := jsonGet(argsJSON, "filePath"); p != "" {
			paths = append(paths, p)
		}
	case "grep", "glob":
		if p := jsonGet(argsJSON, "path"); p != "" {
			paths = append(paths, p)
		}
	case "bash", "powershell":
		paths = extractCommandPaths(jsonGet(argsJSON, "command"))
	}
	return paths
}

func jsonGet(raw, key string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	rawVal, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(rawVal, &s); err != nil {
		return ""
	}
	return s
}

func extractCommandPaths(cmd string) []string {
	if cmd == "" {
		return nil
	}
	var paths []string
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		if i > 0 && strings.HasPrefix(fields[i-1], "-") {
			continue
		}
		if looksLikePath(f) {
			paths = append(paths, f)
		}
	}
	return paths
}

func looksLikePath(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return true
	}
	if strings.HasPrefix(s, "~/") {
		return true
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	return false
}
