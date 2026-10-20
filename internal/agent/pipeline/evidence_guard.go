package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

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
)

type readState struct {
	hash   string
	ranges [][2]int
}

// EvidenceGuardMiddleware detects exploration that is not producing new
// evidence: repeated identical calls, and re-reads of unchanged file ranges.
// Exact-result repetition is still the loop detector's job; this guard covers
// heterogeneous read/grep thrash that never forms an identical five-call run.
type EvidenceGuardMiddleware struct {
	calls map[string]string // canonical (tool, args) -> result hash
	reads map[string]readState

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
		reads:      make(map[string]readState),
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
	for _, result := range results {
		if result.Name == "" {
			continue
		}
		if m.observe(result) {
			m.stale = 0
			m.nudged = false
		} else {
			m.stale++
		}
	}
	if m.stale >= m.haltAfter && m.haltAfter > 0 {
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
// repeated an already-seen call or an already-covered, unchanged read.
func (m *EvidenceGuardMiddleware) observe(result ToolResult) bool {
	if isMutation(result) {
		if result.Error == nil {
			// A successful mutation invalidates read coverage: files may have
			// changed. Call history is kept so repeated greps still count.
			m.reads = make(map[string]readState)
			return true
		}
	}
	if result.Name == "read" {
		return m.observeRead(result)
	}
	key := result.Name + "\x00" + canonicalArgs(result.Args)
	hash := resultHash(result)
	if previous, ok := m.calls[key]; ok && previous == hash {
		return false
	}
	m.calls[key] = hash
	return true
}

func (m *EvidenceGuardMiddleware) observeRead(result ToolResult) bool {
	var args struct {
		FilePath string `json:"filePath"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(result.Args), &args); err != nil || args.FilePath == "" {
		key := "read\x00" + canonicalArgs(result.Args)
		hash := resultHash(result)
		if previous, ok := m.calls[key]; ok && previous == hash {
			return false
		}
		m.calls[key] = hash
		return true
	}
	hash := resultHash(result)
	state, ok := m.reads[args.FilePath]
	if !ok || state.hash != hash {
		start, end := readRange(args.Offset, args.Limit)
		m.reads[args.FilePath] = readState{hash: hash, ranges: [][2]int{{start, end}}}
		return true
	}
	start, end := readRange(args.Offset, args.Limit)
	if rangeCovered(state.ranges, start, end) {
		return false
	}
	state.ranges = mergeRange(state.ranges, start, end)
	m.reads[args.FilePath] = state
	return true
}

func (m *EvidenceGuardMiddleware) distinct() int {
	return len(m.calls) + len(m.reads)
}

func isMutation(result ToolResult) bool {
	switch result.Name {
	case "write", "edit", "apply_patch":
		return true
	case "proposal":
		var args struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(result.Args), &args); err != nil {
			return false
		}
		return args.Action == "apply" || args.Action == "select"
	}
	return false
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
	sum := sha256.Sum256([]byte(result.Result + "\x00" + errorString(result.Error)))
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
	return offset, offset + limit - 1
}

func rangeCovered(ranges [][2]int, start, end int) bool {
	for _, r := range ranges {
		if r[0] <= start && r[1] >= end {
			return true
		}
	}
	return false
}

func mergeRange(ranges [][2]int, start, end int) [][2]int {
	ranges = append(ranges, [2]int{start, end})
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	merged := ranges[:0]
	for _, r := range ranges {
		if len(merged) == 0 || r[0] > merged[len(merged)-1][1]+1 {
			merged = append(merged, r)
			continue
		}
		if r[1] > merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = r[1]
		}
	}
	return merged
}
