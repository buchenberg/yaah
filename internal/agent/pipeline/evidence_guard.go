package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// EvidenceThrashError is returned when the agent keeps issuing calls that
// yield no new evidence. Use errors.Is with a zero value to match:
//
//	errors.Is(err, EvidenceThrashError{})
type EvidenceThrashError struct {
	Stale    int
	Distinct int
}

func (e EvidenceThrashError) Error() string {
	return fmt.Sprintf("evidence guard: %d consecutive tool calls produced no new evidence (%d distinct results) — halting to prevent stuck exploration",
		e.Stale, e.Distinct)
}

func (e EvidenceThrashError) Is(target error) bool {
	_, ok := target.(EvidenceThrashError)
	return ok
}

const (
	defaultEvidenceNudgeAfter = 6
	unboundedRangeEnd         = int(^uint(0) >> 1)
	maxEvidenceEntries        = 1024
)

// spillPathPattern strips the timestamped spill filename from oversized
// tool results so a re-read hashes the same as the original.
var spillPathPattern = regexp.MustCompile(`full output at \S+`)

// EvidenceGuardMiddleware detects exploration that is not producing new
// evidence: repeated identical calls, and re-reads of the same unchanged
// file range. Exact-result repetition is still the loop detector's job; this
// guard covers heterogeneous read/grep thrash that never forms an identical
// five-call run.
type EvidenceGuardMiddleware struct {
	// calls maps a fixed-width hash of (tool, canonical args) to the result
	// hash. Only hashes are retained, so long sessions do not keep every
	// command in memory.
	calls map[string]string
	// reads maps a path to its observed line ranges and their slice hashes.
	// Ranges are exact: without file-level metadata a subrange cannot be
	// compared to a larger read, so only an identical range repeats count.
	reads map[string]map[[2]int]string

	nudgeAfter int
	haltAfter  int
	stale      int
	nudged     bool
}

// NewEvidenceGuardMiddleware builds the guard. nudgeAfter 0 uses the default,
// negative disables it, and haltAfter 0 keeps halt off.
func NewEvidenceGuardMiddleware(nudgeAfter, haltAfter int) *EvidenceGuardMiddleware {
	if nudgeAfter < 0 {
		nudgeAfter = 0
	} else if nudgeAfter == 0 {
		nudgeAfter = defaultEvidenceNudgeAfter
	}
	if haltAfter > 0 && nudgeAfter > 0 && haltAfter < nudgeAfter {
		haltAfter = nudgeAfter
	}
	return &EvidenceGuardMiddleware{
		calls:      make(map[string]string),
		reads:      make(map[string]map[[2]int]string),
		nudgeAfter: nudgeAfter,
		haltAfter:  haltAfter,
	}
}

func (m *EvidenceGuardMiddleware) Name() string { return "evidence_guard" }

func (m *EvidenceGuardMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *EvidenceGuardMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *EvidenceGuardMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.nudgeAfter == 0 {
		return step, nil
	}
	batchStale := 0
	anyNew := false
	for _, result := range results {
		if result.Name == "" {
			continue
		}
		if m.observe(result) {
			anyNew = true
		} else {
			batchStale++
		}
	}
	// Batch order must not decide the counter: if any call produced new
	// evidence the batch made progress; otherwise every call was stale.
	if anyNew {
		m.stale = 0
		m.nudged = false
	} else {
		m.stale += batchStale
	}
	if m.haltAfter > 0 && m.stale >= m.haltAfter {
		return step, EvidenceThrashError{Stale: m.stale, Distinct: m.distinct()}
	}
	if !m.nudged && m.stale >= m.nudgeAfter {
		m.nudged = true
		slog.Debug("evidence guard nudged", "stale", m.stale, "distinct", m.distinct())
		step.Messages = append(step.Messages, types.UserMsg(
			prompts.EvidenceSteerMessage(m.stale, m.distinct())))
	}
	return step, nil
}

// observe returns true when the result is new evidence. False means the call
// repeated an already-seen call or an already-seen unchanged read range.
func (m *EvidenceGuardMiddleware) observe(result ToolResult) bool {
	if mutationSucceeded(result) {
		// File contents may have changed; prior read hashes are no longer
		// trustworthy. Call history is kept so repeated greps still count.
		m.reads = make(map[string]map[[2]int]string)
		return true
	}
	if result.Name == "read" {
		return m.observeRead(result)
	}
	key := callKey(result.Name, result.Args)
	hash := resultHash(result)
	if previous, ok := m.calls[key]; ok && previous == hash {
		return false
	}
	m.calls[key] = hash
	m.trim()
	return true
}

func (m *EvidenceGuardMiddleware) observeRead(result ToolResult) bool {
	var args struct {
		FilePath string `json:"filePath"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(result.Args), &args); err != nil || args.FilePath == "" {
		key := callKey("read", result.Args)
		hash := resultHash(result)
		if previous, ok := m.calls[key]; ok && previous == hash {
			return false
		}
		m.calls[key] = hash
		m.trim()
		return true
	}
	start, end := readRange(args.Offset, args.Limit)
	rng := [2]int{start, end}
	hash := resultHash(result)

	ranges, ok := m.reads[args.FilePath]
	if !ok {
		m.reads[args.FilePath] = map[[2]int]string{rng: hash}
		m.trim()
		return true
	}
	if previous, seen := ranges[rng]; seen {
		if previous == hash {
			return false
		}
		// The same range returned different content: the file changed.
		// Drop the other range hashes because they describe the old file.
		m.reads[args.FilePath] = map[[2]int]string{rng: hash}
		return true
	}
	ranges[rng] = hash
	m.trim()
	return true
}

func (m *EvidenceGuardMiddleware) trim() {
	if len(m.calls)+len(m.reads) <= maxEvidenceEntries {
		return
	}
	// Bounded memory matters more than perfect recall on very long runs.
	m.calls = make(map[string]string)
	m.reads = make(map[string]map[[2]int]string)
}

func (m *EvidenceGuardMiddleware) distinct() int {
	entries := len(m.calls)
	for _, ranges := range m.reads {
		entries += len(ranges)
	}
	return entries
}

func mutationSucceeded(result ToolResult) bool {
	if result.Error != nil {
		return false
	}
	switch result.Name {
	case "write", "edit", "patch", "delete", "replace", "sed":
		return true
	case "proposal":
		var args struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(result.Args), &args); err != nil {
			return false
		}
		return args.Action == "apply" || args.Action == "select"
	case "json_query":
		var args struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(result.Args), &args); err != nil {
			return false
		}
		return args.Action == "write" || args.Action == "delete"
	}
	return false
}

func callKey(name, rawArgs string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + canonicalArgs(rawArgs)))
	return hex.EncodeToString(sum[:])
}

func canonicalArgs(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

func resultHash(result ToolResult) string {
	resultText := spillPathPattern.ReplaceAllString(result.Result, "full output at <spill>")
	sum := sha256.Sum256([]byte(resultText + "\x00" + errorString(result.Error)))
	return hex.EncodeToString(sum[:])
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readRange(offset, limit int) (int, int) {
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		return offset, unboundedRangeEnd
	}
	end := int64(offset) + int64(limit) - 1
	if end > int64(unboundedRangeEnd) || end < int64(offset) {
		return offset, unboundedRangeEnd
	}
	return offset, int(end)
}
