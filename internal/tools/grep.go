package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// grepMaxResultLen caps the total grep output length.
const grepMaxResultLen = 8192

// GrepTool searches file contents using ripgrep with a Go-native fallback.
type GrepTool struct{}

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Searches file contents using ripgrep. Returns file paths, line numbers, and matching lines."
}

func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "The regular expression pattern to search for"},
			"path": {"type": "string", "description": "Directory or file to search in (default current directory)"},
			"include": {"type": "string", "description": "File pattern to include (e.g. \"*.go\", \"*.{ts,tsx}\")"}
		},
		"required": ["pattern"]
	}`)
}

// grepParams holds the parameters for grep execution.
type grepParams struct {
	Pattern string
	Path    string
	Include string
}

func (t *GrepTool) Execute(ctx context.Context, args string) (string, error) {
	var params grepParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("grep: invalid arguments: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}

	if rgAvailable() {
		return t.execRipgrep(ctx, params)
	}
	return t.grepNative(ctx, params)
}

// rgAvailable checks if ripgrep is on PATH.
func rgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

func (t *GrepTool) execRipgrep(ctx context.Context, params grepParams) (string, error) {
	rgArgs := []string{"--no-heading", "--line-number", "--color", "never", "--no-messages", "-e", params.Pattern}
	if params.Include != "" {
		rgArgs = append(rgArgs, "--glob", params.Include)
	}
	rgArgs = append(rgArgs, "--", params.Path)

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return "", fmt.Errorf("grep: %w\n%s", err, string(output))
	}

	result := string(output)
	if result == "" {
		return "No matches found.", nil
	}
	result = string(truncateOutput([]byte(result)))
	if len(result) > grepMaxResultLen {
		result = result[:grepMaxResultLen] + "\n...[truncated]..."
	}
	return result, nil
}

// commonIgnoreDirs lists directories that are nearly always safe to skip.
var commonIgnoreDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true,
	".tox": true, ".eggs": true, "dist": true, "build": true,
	".next": true, ".nuxt": true, ".output": true,
	"vendor": true, ".idea": true, ".vscode": true,
	"target": true, ".dart_tool": true, ".turbo": true,
	"bin": true, "obj": true,
}

// binaryExtensions is a rough set of extensions that should be skipped.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".bz2": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".class": true, ".pyc": true, ".pyo": true, ".o": true, ".obj": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
	".min.js": true, ".min.css": true, ".map": true,
}

func (t *GrepTool) grepNative(ctx context.Context, params grepParams) (string, error) {
	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: invalid regex: %w", err)
	}

	var includeRe *regexp.Regexp
	if params.Include != "" {
		pat := globToRegex(params.Include)
		includeRe, err = regexp.Compile(pat)
		if err != nil {
			return "", fmt.Errorf("grep: invalid include pattern: %w", err)
		}
	}

	var buf bytes.Buffer
	matchCount := 0

	walkErr := filepath.Walk(params.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if commonIgnoreDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if binaryExtensions[filepath.Ext(path)] {
			return nil
		}
		if includeRe != nil && !includeRe.MatchString(filepath.Base(path)) {
			return nil
		}

		file, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if re.Match(scanner.Bytes()) {
				matchCount++
				fmt.Fprintf(&buf, "%s:%d:%s\n", path, lineNum, scanner.Text())
				if buf.Len() > bashMaxOutput {
					return nil
				}
			}
		}
		return nil
	})

	if walkErr != nil && walkErr != filepath.SkipDir {
		return "", fmt.Errorf("grep: %w", walkErr)
	}

	if matchCount == 0 {
		return "No matches found.", nil
	}

	result := buf.String()
	result = strings.TrimSpace(result)
	if len(result) > grepMaxResultLen {
		result = result[:grepMaxResultLen] + "\n...[truncated]..."
	}
	return result, nil
}

// globToRegex converts a simplified glob pattern to a regex.
func globToRegex(pattern string) string {
	var buf strings.Builder
	buf.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				buf.WriteString(".*")
				i++
			} else {
				buf.WriteString("[^/]*")
			}
		case '?':
			buf.WriteString("[^/]")
		case '.':
			buf.WriteString("\\.")
		case '{':
			buf.WriteString("(?:")
			i++
			var parts []string
			for i < len(pattern) && pattern[i] != '}' {
				start := i
				for i < len(pattern) && pattern[i] != ',' && pattern[i] != '}' {
					i++
				}
				parts = append(parts, globToRegex(pattern[start:i]))
				if i < len(pattern) && pattern[i] == ',' {
					i++
				}
			}
			buf.WriteString(strings.Join(parts, "|"))
			buf.WriteString(")")
		case '[':
			buf.WriteByte('[')
			i++
			for i < len(pattern) && pattern[i] != ']' {
				buf.WriteByte(pattern[i])
				i++
			}
			if i < len(pattern) {
				buf.WriteByte(']')
			}
		default:
			buf.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	buf.WriteString("$")
	return buf.String()
}
