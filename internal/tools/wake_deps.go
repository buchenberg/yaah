package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/wake"
)

// NewWakeDeps builds the predicate engine's side-effect seam on the session's
// workspace. Everything a predicate observes — a command's exit, a file's
// content, an HTTP status — goes through the same Workspace and SSRF-guarded
// client the other tools use, so a wait in an isolated session is contained
// exactly like a bash or webfetch call.
func NewWakeDeps(ws Workspace, j *jobs.BackgroundJobs, procs *process.Manager) wake.Deps {
	return wake.Deps{
		Jobs:      j,
		Processes: procs,
		Exec: func(ctx context.Context, command string) (wake.ExecResult, error) {
			if ws == nil {
				return wake.ExecResult{ExitCode: -1}, fmt.Errorf("no workspace")
			}
			res, err := ws.Exec(ctx, shellCommand(ws, command))
			return wake.ExecResult{ExitCode: res.ExitCode, Output: res.Stdout + res.Stderr}, err
		},
		HTTP: func(ctx context.Context, url string) (wake.HTTPResult, error) {
			return probeHTTP(ctx, url)
		},
		Glob: func(ctx context.Context, pattern string) ([]string, error) {
			return wakeGlob(ctx, ws, pattern)
		},
		ReadFile: func(ctx context.Context, path string) ([]byte, error) {
			if ws == nil {
				return nil, fmt.Errorf("no workspace")
			}
			resolved, err := ws.ResolvePath(path)
			if err != nil {
				return nil, err
			}
			return ws.ReadFile(ctx, resolved)
		},
		ReadFileLimit: func(ctx context.Context, path string, max int) ([]byte, error) {
			if ws == nil {
				return nil, fmt.Errorf("no workspace")
			}
			resolved, err := ws.ResolvePath(path)
			if err != nil {
				return nil, err
			}
			if lr, ok := ws.(interface {
				ReadFileLimit(context.Context, string, int) ([]byte, error)
			}); ok {
				return lr.ReadFileLimit(ctx, resolved, max)
			}
			// A substrate without a bounded reader still gets the cap;
			// only the host path avoids materializing the whole file.
			data, err := ws.ReadFile(ctx, resolved)
			if err != nil {
				return nil, err
			}
			if len(data) > max {
				data = data[:max]
			}
			return data, nil
		},
	}
}

// wakeHTTPBodyCap bounds one probe's body: the engine only tails evidence,
// and a poll must not pull a multi-megabyte page on every attempt.
const wakeHTTPBodyCap = 256 * 1024

// probeHTTP performs one GET through the SSRF-guarded tool client. A non-2xx
// response is a successful probe (the status is the evidence); only transport
// and URL-guard failures are errors.
func probeHTTP(ctx context.Context, rawURL string) (wake.HTTPResult, error) {
	if err := validateURL(rawURL); err != nil {
		return wake.HTTPResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return wake.HTTPResult{}, fmt.Errorf("invalid url: %w", err)
	}
	req.Header.Set("User-Agent", "yaah/1.0")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return wake.HTTPResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, wakeHTTPBodyCap))
	if err != nil {
		return wake.HTTPResult{}, fmt.Errorf("read body: %w", err)
	}
	return wake.HTTPResult{Status: resp.StatusCode, Body: string(body)}, nil
}

// wakeGlob finds files matching a workspace-relative glob. It is the file
// predicate's eyes: the same pattern syntax as the glob tool, walked through
// the injected Workspace so isolated sessions see their own tree.
func wakeGlob(ctx context.Context, ws Workspace, pattern string) ([]string, error) {
	if ws == nil {
		return nil, fmt.Errorf("no workspace")
	}
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, fmt.Errorf("empty file pattern")
	}
	if !hasGlobMeta(pattern) {
		resolved, err := ws.ResolvePath(pattern)
		if err != nil {
			return nil, err
		}
		if _, err := ws.Stat(ctx, resolved); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		return []string{resolved}, nil
	}

	// Match the magic part relative to the *resolved* walk root. Matching
	// the full pattern against a base-relative path breaks when the root
	// contains a symlink or a ".." segment: ResolvePath canonicalizes the
	// walk root, so the paths the walker yields no longer carry the
	// lexical spelling the pattern used.
	root, magic, err := splitGlob(pattern)
	if err != nil {
		return nil, err
	}
	re, err := patternToRegex(magic)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	resolvedRoot, err := ws.ResolvePath(root)
	if err != nil {
		return nil, err
	}

	const maxMatches = 1000
	var matches []string
	walkErr := walkWorkspace(ctx, ws, resolvedRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if p != resolvedRoot && commonIgnoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			if p != resolvedRoot && isNestedWorktreeRoot(ctx, ws, p) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(resolvedRoot, p)
		if rerr != nil {
			return nil
		}
		if re.MatchString(filepath.ToSlash(rel)) {
			matches = append(matches, p)
			if len(matches) >= maxMatches {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipDir && walkErr != filepath.SkipAll {
		return nil, walkErr
	}
	sort.Strings(matches)
	return matches, nil
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[{")
}

// splitGlob returns the non-magic prefix to walk from and the magic suffix
// to match against a root-relative path. Splitting on segments (not a string
// prefix) keeps dotfile patterns intact: ".*" has no literal root, so its
// magic part is ".*", not "*".
//
// A root-anchored pattern such as "/*.log" keeps "/" rather than being
// silently rewound to the workspace. ResolvePath then refuses it as outside
// the workspace (or accepts it when the workspace contains that path), which
// is the containment rule every file tool follows. File predicates are
// workspace-relative by design; the error is the honest answer.
func splitGlob(pattern string) (root, magic string, err error) {
	parts := strings.Split(pattern, "/")
	i := 0
	for i < len(parts) && !hasGlobMeta(parts[i]) {
		i++
	}
	if i == len(parts) {
		return "", "", fmt.Errorf("pattern %q has no wildcard", pattern)
	}
	magic = strings.Join(parts[i:], "/")
	root = strings.Join(parts[:i], "/")
	if root == "" {
		if strings.HasPrefix(pattern, "/") {
			root = "/"
		} else {
			root = "."
		}
	}
	return root, magic, nil
}
