package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// StaticcheckTool runs go vet and/or staticcheck, returning structured diagnostics.
type StaticcheckTool struct {
	WS Workspace
}

var _ WorkspaceSetter = (*StaticcheckTool)(nil)

func (t *StaticcheckTool) SetWorkspace(ws Workspace) { t.WS = ws }

func NewStaticcheckTool() *StaticcheckTool { return &StaticcheckTool{} }

func (t *StaticcheckTool) Name() string        { return "staticcheck" }
func (t *StaticcheckTool) Description() string { return prompts.ToolDescription("staticcheck") }

func (t *StaticcheckTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"packages": {
				"type": "string",
				"description": "Go package pattern like './...'"
			},
			"analyzers": {
				"type": "string",
				"enum": ["vet", "staticcheck", "both"],
				"description": "Which analyzer(s) to run (default 'vet')"
			},
			"flags": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Extra flags to pass through to the analyzer"
			}
		},
		"required": ["packages"]
	}`)
}

type staticcheckParams struct {
	Packages  string   `json:"packages"`
	Analyzers string   `json:"analyzers"`
	Flags     []string `json:"flags"`
}

type diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Analyzer string `json:"analyzer"`
	Severity string `json:"severity"`
}

type staticcheckResult struct {
	// Status is "ok" when every requested analyzer actually ran, and "error"
	// when one did not (missing binary, unusable toolchain, or an analyzer that
	// exited non-zero without producing a single diagnostic). Count alone
	// cannot express that: 0 diagnostics from a tool that never ran looks
	// exactly like clean code.
	Status               string         `json:"status"`
	Diagnostics          []diagnostic   `json:"diagnostics"`
	Count                int            `json:"count"`
	ByFile               map[string]int `json:"by_file"`
	VetAvailable         bool           `json:"vet_available"`
	StaticcheckAvailable bool           `json:"staticcheck_available"`
	Stdout               string         `json:"stdout"`
	Stderr               string         `json:"stderr"`
}

var diagRe = regexp.MustCompile(`^(.+?):(\d+):(\d+):\s*(.*)$`)

func (t *StaticcheckTool) Execute(ctx context.Context, args string) (string, error) {
	var params staticcheckParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("staticcheck: invalid arguments: %w", err)
	}

	analyzers := params.Analyzers
	if analyzers == "" {
		analyzers = "vet"
	}

	result := &staticcheckResult{
		Diagnostics: []diagnostic{},
		ByFile:      map[string]int{},
	}

	runVet := analyzers == "vet" || analyzers == "both"
	runSC := analyzers == "staticcheck" || analyzers == "both"
	if !runVet && !runSC {
		return "", fmt.Errorf("staticcheck: analyzers must be one of vet, staticcheck, both")
	}

	ws := workspaceOf(t.WS, nil)
	status := staticcheckOK

	// go vet
	if runVet {
		vetArgs := append([]string{"vet", params.Packages}, params.Flags...)
		res, execErr := ws.Exec(ctx, ExecRequest{Command: "go", Args: vetArgs})
		// A missing go exits 127 from the shell, and a host exec failure
		// reports -1; both mean vet never ran, unlike exit 1 ("issues found").
		result.VetAvailable = executableExit(res.ExitCode)
		if execErr != nil && res.ExitCode < 0 {
			result.Stderr += fmt.Sprintf("go vet could not run: %v\n", execErr)
		}
		if !result.VetAvailable {
			result.Stderr += "go vet is not available in this workspace\n"
		}
		before := len(result.Diagnostics)
		buildFailed := analyzerBuildFailure(res.Stdout)
		if buildFailed {
			// Compiler errors, not findings: `go vet` prints a `# pkg` header and
			// then `vet: x.go:3:9: undefined: x`, which even matches diagRe with a
			// bogus file name. Keep the output verbatim instead of dressing a
			// build failure up as a warning-level diagnostic.
			result.Stdout += strings.TrimRight(res.Stdout, "\n") + "\n"
		} else {
			parseDiagnostics(res.Stdout, "vet", result)
		}
		if !result.VetAvailable || buildFailed || (res.ExitCode != 0 && len(result.Diagnostics) == before) {
			status = staticcheckError
		}
	}

	// staticcheck
	if runSC {
		scArgs := append([]string{params.Packages}, params.Flags...)
		res, execErr := ws.Exec(ctx, ExecRequest{Command: "staticcheck", Args: scArgs})
		// Resolving the binary with LookPath first would check the *host* PATH,
		// which is the wrong machine once a workspace can be isolated. Attempting
		// the run and inspecting the exit code asks the right machine instead.
		result.StaticcheckAvailable = executableExit(res.ExitCode)
		if execErr != nil && res.ExitCode < 0 {
			result.Stderr += fmt.Sprintf("staticcheck could not run: %v\n", execErr)
		}
		if !result.StaticcheckAvailable {
			result.Stderr += "staticcheck not found in this workspace — install with: go install honnef.co/go/tools/cmd/staticcheck@latest\n"
			status = staticcheckError
		} else {
			before := len(result.Diagnostics)
			buildFailed := analyzerBuildFailure(res.Stdout)
			if buildFailed {
				// staticcheck prefixes its load failure with `-: # pkg`; the
				// position lines that follow are compiler messages, not checks.
				result.Stdout += strings.TrimRight(res.Stdout, "\n") + "\n"
			} else {
				parseDiagnostics(res.Stdout, "staticcheck", result)
			}
			if buildFailed || (res.ExitCode != 0 && len(result.Diagnostics) == before) {
				status = staticcheckError
				// A stale analyzer is the one failure a caller can actually
				// fix, and its output says so only obliquely.
				if hint := staleAnalyzerHint(res.Stdout + "\n" + res.Stderr); hint != "" {
					result.Stderr += hint
				}
			}
		}
	}

	result.Count = len(result.Diagnostics)
	result.Status = status
	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
}

const (
	staticcheckOK    = "ok"
	staticcheckError = "error"
)

// exportDataVersionMismatch matches the exact line a stale analyzer produces
// when it cannot read a newer toolchain's export data, e.g.
// "version 5 > maximum supported 4". Compiled here rather than stored in a
// package-level var so the failure path stays self-contained.
var exportDataVersionMismatch = regexp.MustCompile(`version \d+ > maximum supported \d+`)

// staleAnalyzerHint turns an out-of-date staticcheck into something a caller
// can act on.
//
// A binary built against an older Go cannot read a newer toolchain's export
// data: it exits non-zero, prints no diagnostics, and reports only "version N >
// maximum supported M". The result then lands in the generic failure path —
// status:error with an empty diagnostics list — which is indistinguishable from
// a package that genuinely failed to analyse. Two reviewers hit exactly this
// and could only report "source-reading only"; the workaround lived in
// AGENTS.md rather than in the tool's own output (yaah-2vc).
func staleAnalyzerHint(output string) string {
	// Require the concrete version comparison. Matching a phrase is not
	// enough: a corrupt build cache, a test fixture, or a user's own error
	// text could otherwise be diagnosed as a stale analyzer, and confidently
	// wrong advice is worse than none.
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// Anchor to the concrete comparison, not just the phrase. A message
		// that merely contains "maximum supported" — a docs string in a test
		// fixture, a user's own error text — must not be diagnosed as a stale
		// analyzer.
		if !exportDataVersionMismatch.MatchString(t) {
			continue
		}
		return fmt.Sprintf(
			"-- your staticcheck is too old for this Go toolchain (%s) — refresh it with: go install honnef.co/go/tools/cmd/staticcheck@latest --\n",
			t)
	}
	return ""
}

// analyzerBuildFailure reports whether the analyzer output carries Go's build
// header (`# pkg`, or staticcheck's `-: # pkg`), which both tools print only
// when the package could not be compiled. Those runs analyzed nothing, and
// their position-prefixed lines must not be reported as findings.
func analyzerBuildFailure(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "-: # ") {
			return true
		}
	}
	return false
}

func parseDiagnostics(output string, analyzer string, result *staticcheckResult) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matches := diagRe.FindStringSubmatch(line)
		if matches == nil {
			// Not a standard file:line:col: message line — treat as raw output
			result.Stdout += line + "\n"
			continue
		}
		file := matches[1]
		lineNum := 0
		fmt.Sscanf(matches[2], "%d", &lineNum)
		colNum := 0
		fmt.Sscanf(matches[3], "%d", &colNum)
		msg := matches[4]

		d := diagnostic{
			File:     file,
			Line:     lineNum,
			Column:   colNum,
			Message:  msg,
			Analyzer: analyzer,
			Severity: classifySeverity(msg),
		}
		result.Diagnostics = append(result.Diagnostics, d)
		result.ByFile[file]++
	}
}

// classifySeverity guesses severity from the diagnostic message.
func classifySeverity(msg string) string {
	lower := strings.ToLower(msg)
	if strings.HasPrefix(lower, "error") ||
		strings.Contains(lower, "assignment to nil") ||
		strings.Contains(lower, "nil pointer") {
		return "error"
	}
	return "warning"
}

// executableExit reports whether an exit code means the analyzer itself ran, as
// opposed to the shell failing to find or invoke it. A missing binary in a
// sandbox exits 127 (126 when present but not executable, 125 when the shell
// itself fails), while a failed host exec reports -1. Treating those as
// available would report zero diagnostics and a clean bill of health for a
// tool that never ran.
func executableExit(code int) bool {
	if code < 0 {
		return false
	}
	switch code {
	case 125, 126, 127:
		return false
	}
	return true
}
