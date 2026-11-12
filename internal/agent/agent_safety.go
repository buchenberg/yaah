package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/permissions"
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
	meta    map[string]any
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

// dangerousCall reports whether a call would mutate refs, files, or processes,
// using the registry's DangerClassifier when the tool has one and the
// alwaysDangerous table for shell names no platform registry carries. It is
// the danger vocabulary the Authorize gate stages classify with, so the
// optional approval layer and the required dispatch gate cannot disagree about
// what is dangerous.
func (l *Loop) dangerousCall(name, args string) bool {
	if l.Registry != nil {
		if t := l.Registry.Get(name); t != nil {
			dc, ok := t.(tools.DangerClassifier)
			return ok && dc.IsDangerous(args)
		}
	}
	return alwaysDangerous[name]
}

// approveTool prompts the user on stderr/stdin to approve a tool call.
// Returns true if the user approves. If ApproveFn is set, it delegates
// to that instead.
//
// Without ApproveFn the fallback reads os.Stdin — but only when stdin
// is an interactive character device. Headless drivers (MCP serve,
// ACP, sandboxed sub-agents) abuse stdin as a protocol stream or a
// closed pipe; scanning it would swallow live protocol bytes as the
// y/N answer (review: stdin-steal on protocol streams). Heads-off
// approval is therefore denied.
func (l *Loop) approveTool(name, args string) bool {
	abbr := abbreviateArgs(args, 120)
	if l.ApproveFn != nil {
		return l.ApproveFn(name, abbr)
	}
	if fi, err := os.Stdin.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(os.Stderr, "\n  ⚠ Denying %s(%s): approval required but stdin is not interactive\n", name, abbr)
		return false
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

// inheritedCeiling is the ruleset slice a dispatch of this loop hands to
// its sub-agents (parity W5.7): the effective ruleset filtered to deny
// rules on mutating file tools. Computed per dispatch — not captured at
// wiring — so a persona switch that rewrote the ruleset governs the next
// dispatch. Sub-agent loops satisfy the same contract from their own
// Config when they dispatch (multi-hop): their inherited ceiling is
// already deny+mutating-only, so re-filtering a merged set is a no-op
// that composes.
func (l *Loop) inheritedCeiling() permissions.Rules {
	return permissions.InheritedCeiling(l.Config.Permissions)
}
