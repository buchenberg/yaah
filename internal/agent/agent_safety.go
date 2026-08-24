package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
)

// toolExecResult holds the outcome of a single tool execution.
type toolExecResult struct {
	idx     int
	callID  string
	name    string
	args    string
	content string
	dur     time.Duration
	err     error
}

// parseTaskArgs extracts a display-friendly role and prompt (and whether
// the call requested background dispatch) from a task tool's JSON
// arguments. Returns ("default", "", false) when the JSON is unparseable.
func parseTaskArgs(args string) (role, prompt string, background bool) {
	var p struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Role        string `json:"role"`
		Background  bool   `json:"background"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "default", "", false
	}
	role = p.Role
	if role == "" {
		role = "default"
	}
	return role, abbreviateArgs(p.Description, 60), p.Background
}

// abbreviateArgs truncates JSON args to maxLen characters with ellipsis.
// Handles multi-byte UTF-8 by counting runes, not bytes.
func abbreviateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen-3]) + "..."
}

// alwaysDangerous lists tool names that require approval even when they
// are absent from the registry. Shell tools are platform-gated out of the
// registry (bash on Windows, powershell elsewhere), and classifying them
// via registry lookup alone made the approval gate platform-dependent.
// Unknown names stay ungated here; MCP-tool policy is handled separately.
var alwaysDangerous = map[string]bool{
	"bash":       true,
	"powershell": true,
}

// classifyGate returns the approval gate decision for a tool call.
//
// MCP-served tools are checked FIRST: they are registered in the
// registry as *mcp.MCPTool wrappers that cannot implement
// tools.DangerClassifier (they are remote), so the registry path below
// would return GatePass for them and fail open. The mcp_approval policy
// therefore applies regardless of registry presence and independently of
// the global approval mode (review finding S3).
//
// Registered tools with a DangerClassifier defer to it; alwaysDangerous
// names gate even when not registered on this platform. Both map to
// GateGlobal — the global ApprovalMode decides what "dangerous" means
// (ask prompts, deny strips, allow passes).
func (l *Loop) classifyGate(name, args string) pipeline.GateDecision {
	if l.Config.MCPToolNames[name] {
		switch l.Config.MCPApproval {
		case "allow":
			return pipeline.GatePass
		case "deny":
			return pipeline.GateDeny
		default: // "ask" and unset
			return pipeline.GateAsk
		}
	}
	if t := l.Registry.Get(name); t != nil {
		if dc, ok := t.(tools.DangerClassifier); ok {
			if dc.IsDangerous(args) {
				return pipeline.GateGlobal
			}
			return pipeline.GatePass
		}
		return pipeline.GatePass
	}
	if alwaysDangerous[name] {
		return pipeline.GateGlobal
	}
	return pipeline.GatePass
}

// approveTool prompts the user on stderr/stdin to approve a tool call.
// Returns true if the user approves. If ApproveFn is set, it delegates to that instead.
func (l *Loop) approveTool(name, args string) bool {
	abbr := abbreviateArgs(args, 120)
	if l.ApproveFn != nil {
		return l.ApproveFn(name, abbr)
	}
	fmt.Fprintf(os.Stderr, "\n  ⚠ Approve %s(%s)? [y/N]: ", name, abbr)
	os.Stderr.Sync()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return input == "y" || input == "yes"
	}
	return false
}
