package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// globMaxResultLen caps the total glob output length.
const globMaxResultLen = 8192

// GlobTool finds files matching a glob pattern.
type GlobTool struct{}

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return "Finds files matching a glob pattern. Returns sorted file paths."
}

func (t *GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "The glob pattern to match (e.g. \"**/*.go\", \"src/**/*.ts\")"},
			"path": {"type": "string", "description": "Directory to search in (default current directory)"}
		},
		"required": ["pattern"]
	}`)
}

// globParams holds the parameters for glob execution.
type globParams struct {
	Pattern string
	Path    string
}

func (t *GlobTool) Execute(ctx context.Context, args string) (string, error) {
	var params globParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("glob: invalid arguments: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}

	if rgAvailable() {
		return t.globRipgrep(ctx, params)
	}
	return t.globNative(ctx, params)
}

func (t *GlobTool) globRipgrep(ctx context.Context, params globParams) (string, error) {
	exclude := make([]string, 0, len(commonIgnoreDirs))
	for dir := range commonIgnoreDirs {
		exclude = append(exclude, fmt.Sprintf("!%s", dir))
	}
	rgArgs := []string{"--files", "--glob", params.Pattern, "--no-messages"}
	rgArgs = append(rgArgs, exclude...)
	rgArgs = append(rgArgs, "--", params.Path)

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("glob: %w\n%s", err, string(output))
	}

	result := string(output)
	if result == "" {
		return "No files found.", nil
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	sort.Strings(lines)

	result = strings.Join(lines, "\n")
	if len(result) > globMaxResultLen {
		result = result[:globMaxResultLen] + "\n...[truncated]..."
	}
	return result, nil
}

func (t *GlobTool) globNative(ctx context.Context, params globParams) (string, error) {
	re, err := patternToRegex(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob: invalid pattern: %w", err)
	}

	var matches []string
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
		if re.MatchString(path) {
			matches = append(matches, path)
			if len(matches) >= 10000 {
				return nil
			}
		}
		return nil
	})

	if walkErr != nil && walkErr != filepath.SkipDir {
		return "", fmt.Errorf("glob: %w", walkErr)
	}

	if len(matches) == 0 {
		return "No files found.", nil
	}

	sort.Strings(matches)

	result := strings.Join(matches, "\n")
	if len(result) > globMaxResultLen {
		result = result[:globMaxResultLen] + fmt.Sprintf("\n...[truncated: %d files total]...", len(matches))
	}
	return result, nil
}

// patternToRegex converts a glob pattern to a regex for matching file paths.
func patternToRegex(pattern string) (*regexp.Regexp, error) {
	var buf strings.Builder
	buf.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && (pattern[i+2] == '/' || pattern[i+2] == '\\') {
					buf.WriteString("(?:.*[/\\\\])?")
					i += 2
				} else {
					buf.WriteString(".*")
					i++
				}
			} else {
				buf.WriteString("[^/\\\\]*")
			}
		case '?':
			buf.WriteString("[^/\\\\]")
		case '.':
			buf.WriteString("\\.")
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
		case '{':
			buf.WriteString("(?:")
			i++
			var parts []string
			for i < len(pattern) && pattern[i] != '}' {
				start := i
				depth := 0
				for i < len(pattern) {
					if pattern[i] == '{' {
						depth++
					} else if pattern[i] == '}' {
						if depth == 0 {
							break
						}
						depth--
					} else if pattern[i] == ',' && depth == 0 {
						break
					}
					i++
				}
				subRe, err := patternToRegex(pattern[start:i])
				if err != nil {
					return nil, err
				}
				parts = append(parts, subRe.String()[1:len(subRe.String())-1])
				if i < len(pattern) && pattern[i] == ',' {
					i++
				}
			}
			buf.WriteString(strings.Join(parts, "|"))
			buf.WriteString(")")
		default:
			buf.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	buf.WriteString("$")
	return regexp.Compile(buf.String())
}
