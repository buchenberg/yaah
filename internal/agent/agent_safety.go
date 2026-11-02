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
// names gate even when not registered on this platform. Both resolve
// through dangerousGate, which is where the tool level is honoured.
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
	if l.dangerousCall(name, args) {
		return l.dangerousGate()
	}
	return pipeline.GatePass
}

// dangerousCall reports whether a call would mutate refs, files, or processes,
// using the registry's DangerClassifier when the tool has one and the
// alwaysDangerous table for shell names no platform registry carries. The
// approval middleware and the always-on dispatch gate share this one
// classification, so the optional layer and the required one cannot disagree
// about what is dangerous.
func (l *Loop) dangerousCall(name, args string) bool {
	if l.Registry != nil {
		if t := l.Registry.Get(name); t != nil {
			dc, ok := t.(tools.DangerClassifier)
			return ok && dc.IsDangerous(args)
		}
	}
	return alwaysDangerous[name]
}

// dangerousGate resolves a call that has been classified as dangerous.
//
// Outside plan mode it is GateGlobal: the global ApprovalMode decides what
// "dangerous" means (ask prompts, deny strips, allow passes).
//
// In plan mode it is GateWithheld. Plan mode's promise is that mutating tools
// are withheld, and GateGlobal would break that promise — its default is
// "ask", so the user could approve a mutation the mode said was unavailable.
// Withholding also covers names that plan mode never advertises but that the
// gate still sees: allowsTool runs at dispatch, after this classification, so
// argument-carrying tools admitted by name (git) and alwaysDangerous names
// synthesized by dialect recovery reach here first. A withheld call has no
// override, which is what "withheld" has to mean, and its denial names the
// mode rather than an MCP origin policy (GateDeny).
func (l *Loop) dangerousGate() pipeline.GateDecision {
	if l.Config.ToolsLevel == ReadOnlyTools {
		return pipeline.GateWithheld
	}
	return pipeline.GateGlobal
}

// planModeWithholds reports whether plan mode denies this call outright,
// independent of the approval mode and of whether the approval middleware is
// installed at all.
func (l *Loop) planModeWithholds(name, args string) bool {
	return l.dangerousGate() == pipeline.GateWithheld && l.dangerousCall(name, args)
}

// admitTool is the always-on dispatch gate: the advertised tool level plus plan
// mode's per-call danger check. The approval middleware asks the same questions
// through classifyGate, but that middleware is optional
// (middleware.disabled: [approval]) and a mode whose promise is "mutating
// actions are withheld" must not be switchable off by configuration. Both
// layers run the same predicates — allowsTool, planModeWithholds and
// dangerousCall — so the gate's verdict is a precondition for dispatch rather
// than a substitute for it: a call the gate withholds is never admitted here,
// while a call the gate passes can still be refused on the tool level.
//
// The verdict is shared; the wording is not. This gate runs the tool-level
// check first, so in plan mode a name the level excludes is refused as "not
// available at the current tool level" even when planModeWithholds would deny
// the same call — alwaysDangerous makes `bash` that case. classifyGate never
// runs allowsTool, so for that call it reports GateWithheld with the
// withholding as the reason. Allow/deny agrees either way; only the message a
// user sees differs, which is why reordering these checks to align them would
// change what every excluded tool reports for no security gain.
func (l *Loop) admitTool(name, args string) (bool, error) {
	if !l.allowsTool(name) {
		return false, fmt.Errorf("tool %q is not available at the current tool level", name)
	}
	if l.planModeWithholds(name, args) {
		return false, fmt.Errorf("%s", pipeline.WithheldReason(name))
	}
	return true, nil
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
