package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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

// classifyDanger returns true if the given tool+args combination requires
// user approval. It first checks whether the tool implements tools.DangerClassifier
// for argument-level classification; tools without that interface are never dangerous.
func (l *Loop) classifyDanger(name, args string) bool {
	if t := l.Registry.Get(name); t != nil {
		if dc, ok := t.(tools.DangerClassifier); ok {
			return dc.IsDangerous(args)
		}
	}
	return false
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
