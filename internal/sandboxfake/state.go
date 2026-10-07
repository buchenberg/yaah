package sandboxfake

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// state.go implements the snapshot half of the fake: Capture records the
// whole tree into a WorkspaceState, Apply restores it, Diff reports what
// changed since. This is what lets the supervised capture → mutate → apply-out
// rhythm (and its rollback) be tested without a container.
//
// modTime is deliberately NOT part of the state: Capture → Apply → Capture of
// an untouched tree must produce identical digests, or drift detection becomes
// noise. A real backend derives identity from content or revisions, not from
// write timestamps.

// capture records the tree as a WorkspaceState. Revision counts captures, so
// two captures of different trees never collide on the same key.
func (s *Sandbox) Capture(_ context.Context) (shepherd.WorkspaceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return shepherd.WorkspaceState{}, err
	}
	s.rev++
	files := make(map[string]any, len(s.files))
	for p, e := range s.files {
		files[p] = snapshotEntry(e)
	}
	return shepherd.WorkspaceState{
		Backend:  s.Backend(),
		Revision: strconv.Itoa(s.rev),
		Data:     map[string]any{"files": files},
	}, nil
}

func snapshotEntry(e *entry) map[string]any {
	m := map[string]any{
		"kind": e.kindString(),
		"mode": modeOctal(e.mode),
	}
	switch {
	case e.isLink:
		m["target"] = e.target
	case !e.isDir:
		m["content"] = base64.StdEncoding.EncodeToString(e.data)
	}
	return m
}

// apply resets the tree to a previously captured state. The state is
// reusable: applying it twice is idempotent.
func (s *Sandbox) Apply(_ context.Context, ws shepherd.WorkspaceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	if ws.Backend != s.Backend() {
		return fmt.Errorf("sandboxfake: cannot apply state from backend %q", ws.Backend)
	}
	raw, ok := ws.Data["files"].(map[string]any)
	if !ok {
		return fmt.Errorf("sandboxfake: malformed state data: no files map")
	}
	files := make(map[string]*entry, len(raw))
	for p, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("sandboxfake: malformed state entry for %s", p)
		}
		e, err := restoreEntry(m)
		if err != nil {
			return fmt.Errorf("sandboxfake: state entry %s: %w", p, err)
		}
		files[p] = e
	}
	if _, ok := files["/"]; !ok {
		return fmt.Errorf("sandboxfake: state has no root directory")
	}
	// Swap, do not merge: Apply is a reset, and stale paths from the current
	// tree must disappear or a rollback would leave debris behind.
	s.files = files
	return nil
}

func restoreEntry(m map[string]any) (*entry, error) {
	e := &entry{modTime: time.Now()}
	kind, _ := m["kind"].(string)
	switch kind {
	case "directory":
		e.isDir = true
	case "symbolic link":
		e.isLink = true
		e.target, _ = m["target"].(string)
	case "regular file":
		content, _ := m["content"].(string)
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, err
		}
		e.data = data
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	if octal, ok := m["mode"].(string); ok {
		if u, err := strconv.ParseUint(octal, 8, 32); err == nil {
			e.mode = toMode(fs.FileMode(u))
		}
	}
	return e, nil
}

// diff compares a captured state against the current tree and returns a
// unified diff plus the sorted list of changed paths (added, modified, or
// deleted). maxLines <= 0 means unbounded.
func (s *Sandbox) Diff(_ context.Context, ws shepherd.WorkspaceState, maxLines int) (string, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return "", nil, err
	}
	if ws.Backend != s.Backend() {
		return "", nil, fmt.Errorf("sandboxfake: cannot diff state from backend %q", ws.Backend)
	}
	raw, ok := ws.Data["files"].(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("sandboxfake: malformed state data: no files map")
	}
	old := make(map[string]*entry, len(raw))
	for p, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("sandboxfake: malformed state entry for %s", p)
		}
		e, err := restoreEntry(m)
		if err != nil {
			return "", nil, fmt.Errorf("sandboxfake: state entry %s: %w", p, err)
		}
		old[p] = e
	}

	var changed []string
	for p, cur := range s.files {
		prev, ok := old[p]
		if !ok {
			changed = append(changed, p)
			continue
		}
		if !sameEntry(prev, cur) {
			changed = append(changed, p)
		}
	}
	for p := range old {
		if _, ok := s.files[p]; !ok {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return "", nil, nil
	}

	var lines []string
	for _, p := range changed {
		prev, hadPrev := old[p]
		cur, hasCur := s.files[p]
		switch {
		case !hadPrev:
			lines = append(lines, fmt.Sprintf("+++ added %s", p))
		case !hasCur:
			lines = append(lines, fmt.Sprintf("--- deleted %s", p))
		default:
			lines = append(lines, unifiedDiff(p, prev, cur)...)
		}
		if maxLines > 0 && len(lines) >= maxLines {
			lines = append(lines[:maxLines], fmt.Sprintf("... diff truncated at %d lines", maxLines))
			return strings.Join(lines, "\n"), changed, nil
		}
	}
	return strings.Join(lines, "\n"), changed, nil
}

// sameEntry compares two entries by everything the state carries. modTime is
// excluded on purpose: it is not part of a capture, so it must not be part of
// a change.
func sameEntry(a, b *entry) bool {
	if a.isDir != b.isDir || a.isLink != b.isLink || a.mode != b.mode {
		return false
	}
	if a.isLink {
		return a.target == b.target
	}
	if a.isDir {
		return true
	}
	return string(a.data) == string(b.data)
}

// unifiedDiff renders one file's change. Text files get a line diff with
// three lines of context; kind changes (file↔dir↔symlink) get a note, since
// there are no lines to diff.
func unifiedDiff(p string, prev, cur *entry) []string {
	if prev.kindString() != cur.kindString() {
		return []string{fmt.Sprintf("--- %s was a %s", p, prev.kindString()), fmt.Sprintf("+++ %s is now a %s", p, cur.kindString())}
	}
	if prev.isDir || cur.isDir {
		// No content to diff; the change is in descendants.
		return nil
	}
	oldLines := splitLines(string(prev.data))
	newLines := splitLines(string(cur.data))
	out := []string{"--- " + p, "+++ " + p}
	out = append(out, diffHunks(oldLines, newLines, 3)...)
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// A trailing newline does not create a final empty line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOp is one line-level operation: 'c' common, '-' delete, '+' insert.
type diffOp struct {
	kind byte
	line string
}

// lcsOps computes the edit script between a and b via an LCS table. The inputs
// are test-sized; the quadratic table is the clarity choice, not a perf one.
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{'c', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// diffHunks groups an edit script into unified hunks with the given context.
func diffHunks(a, b []string, context int) []string {
	ops := lcsOps(a, b)

	// oldAt[k]/newAt[k] are the old/new line indices op k starts at, so each
	// hunk header can state real positions without a second pass.
	oldAt := make([]int, len(ops)+1)
	newAt := make([]int, len(ops)+1)
	oi, ni := 0, 0
	for k, op := range ops {
		oldAt[k], newAt[k] = oi, ni
		switch op.kind {
		case 'c':
			oi++
			ni++
		case '-':
			oi++
		case '+':
			ni++
		}
	}
	oldAt[len(ops)], newAt[len(ops)] = oi, ni

	// Collect [start, end] op ranges: a hunk per change, extended by up to
	// context common lines on each side, split when common runs exceed twice
	// the context.
	var ranges [][2]int
	k := 0
	for k < len(ops) {
		if ops[k].kind == 'c' {
			k++
			continue
		}
		start := k
		for c := 0; start > 0 && ops[start-1].kind == 'c' && c < context; c++ {
			start--
		}
		last := k
		for j := k; j < len(ops) && j-last <= 2*context; j++ {
			if ops[j].kind != 'c' {
				last = j
			}
		}
		end := last
		for c := 0; end+1 < len(ops) && ops[end+1].kind == 'c' && c < context; c++ {
			end++
		}
		ranges = append(ranges, [2]int{start, end})
		k = end + 1
	}

	var out []string
	for _, r := range ranges {
		oldStart, newStart := oldAt[r[0]]+1, newAt[r[0]]+1
		oldCount, newCount := oldAt[r[1]+1]-oldAt[r[0]], newAt[r[1]+1]-newAt[r[0]]
		var b strings.Builder
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[r[0] : r[1]+1] {
			switch op.kind {
			case 'c':
				b.WriteString("\n " + op.line)
			case '-':
				b.WriteString("\n-" + op.line)
			case '+':
				b.WriteString("\n+" + op.line)
			}
		}
		out = append(out, b.String())
	}
	return out
}
