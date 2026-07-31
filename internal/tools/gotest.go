package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// GoTestTool runs `go test -json` and returns structured results.
type GoTestTool struct{}

func NewGoTestTool() *GoTestTool { return &GoTestTool{} }

func (t *GoTestTool) Name() string        { return "go_test" }
func (t *GoTestTool) Description() string { return prompts.ToolDescription("go_test") }

func (t *GoTestTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"packages": {
				"type": "string",
				"description": "Go package pattern like './...'"
			},
			"timeout_seconds": {
				"type": "integer",
				"description": "Timeout in seconds for the test run (default 120)"
			},
			"flags": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Extra go test flags, e.g. ['-v', '-count=1', '-run', 'TestFoo']"
			},
			"coverprofile": {
				"type": "boolean",
				"description": "If true, run with -coverprofile and include coverage summary"
			}
		},
		"required": ["packages"]
	}`)
}

type goTestParams struct {
	Packages       string   `json:"packages"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Flags          []string `json:"flags"`
	Coverprofile   bool     `json:"coverprofile"`
}

// testEvent is a single JSON line from "go test -json".
type testEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

type goTestPkgResult struct {
	Package string  `json:"package"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Elapsed float64 `json:"elapsed_seconds"`
}

type goTestResult struct {
	Passed      int               `json:"passed"`
	Failed      int               `json:"failed"`
	Skipped     int               `json:"skipped"`
	Total       int               `json:"total"`
	Elapsed     float64           `json:"elapsed_seconds"`
	Packages    []goTestPkgResult `json:"packages"`
	Coverage    string            `json:"coverage,omitempty"`
	FailedTests []string          `json:"failed_tests,omitempty"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
	ExitCode    int               `json:"exit_code"`
}

func (t *GoTestTool) Execute(ctx context.Context, args string) (string, error) {
	var params goTestParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("go_test: invalid arguments: %w", err)
	}

	timeout := params.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}

	coverFile := ""
	if params.Coverprofile {
		coverFile = fmt.Sprintf("coverage-%d.out", os.Getpid())
	}

	cmdArgs := []string{"test", "-json", "-timeout", fmt.Sprintf("%ds", timeout)}
	if coverFile != "" {
		cmdArgs = append(cmdArgs, "-coverprofile="+coverFile)
	}
	cmdArgs = append(cmdArgs, params.Packages)
	cmdArgs = append(cmdArgs, params.Flags...)

	cmd := exec.CommandContext(ctx, "go", cmdArgs...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		exitCode = -1
	}

	result := &goTestResult{
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}

	// Parse JSON line-stream from stdout
	pkgMap := map[string]*goTestPkgResult{}
	var failedTests []string
	var totalElapsed float64

	scanner := bufio.NewScanner(strings.NewReader(stdoutBuf.String()))
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		pkg, ok := pkgMap[ev.Package]
		if !ok {
			pkg = &goTestPkgResult{Package: ev.Package}
			pkgMap[ev.Package] = pkg
		}

		switch ev.Action {
		case "pass":
			if ev.Test != "" {
				pkg.Passed++
				result.Passed++
			}
			pkg.Elapsed = ev.Elapsed
		case "fail":
			if ev.Test != "" {
				pkg.Failed++
				result.Failed++
				failedTests = append(failedTests, fmt.Sprintf("%s/%s", ev.Package, ev.Test))
			}
			pkg.Elapsed = ev.Elapsed
		case "skip":
			if ev.Test != "" {
				pkg.Skipped++
				result.Skipped++
			}
		}
		totalElapsed += ev.Elapsed
	}

	// Build package list
	for _, pkg := range pkgMap {
		result.Packages = append(result.Packages, *pkg)
	}
	result.Total = result.Passed + result.Failed + result.Skipped
	result.Elapsed = totalElapsed
	result.FailedTests = failedTests

	// Coverage summary
	if coverFile != "" {
		coverCmd := exec.CommandContext(ctx, "go", "tool", "cover", "-func="+coverFile)
		coverOut, err := coverCmd.Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(coverOut)), "\n")
			if len(lines) > 0 {
				result.Coverage = strings.TrimSpace(lines[len(lines)-1])
			}
		}
		os.Remove(coverFile)
	}

	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
}
