package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// StaticcheckTool runs go vet and/or staticcheck, returning structured diagnostics.
type StaticcheckTool struct{}

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

	// go vet
	if runVet {
		vetArgs := append([]string{"vet", params.Packages}, params.Flags...)
		vetCmd := exec.CommandContext(ctx, "go", vetArgs...)
		vetOut, err := vetCmd.CombinedOutput()
		vetStr := string(vetOut)
		result.VetAvailable = true

		if err != nil {
			// go vet exits non-zero when it finds issues — parse anyway
			if _, ok := err.(*exec.ExitError); !ok {
				result.VetAvailable = false
				result.Stderr += fmt.Sprintf("go vet error: %v\n", err)
			}
		}
		parseDiagnostics(vetStr, "vet", result)
	}

	// staticcheck
	if runSC {
		scBin, lookErr := exec.LookPath("staticcheck")
		if lookErr != nil {
			result.StaticcheckAvailable = false
			result.Stderr += "staticcheck not found on PATH — install with: go install honnef.co/go/tools/cmd/staticcheck@latest\n"
		} else {
			scArgs := append([]string{params.Packages}, params.Flags...)
			scCmd := exec.CommandContext(ctx, scBin, scArgs...)
			scOut, err := scCmd.CombinedOutput()
			scStr := string(scOut)
			result.StaticcheckAvailable = true

			if err != nil {
				if _, ok := err.(*exec.ExitError); !ok {
					result.StaticcheckAvailable = false
					result.Stderr += fmt.Sprintf("staticcheck error: %v\n", err)
				}
			}
			parseDiagnostics(scStr, "staticcheck", result)
		}
	}

	result.Count = len(result.Diagnostics)
	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
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
