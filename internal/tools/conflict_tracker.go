package tools

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	// ConflictLaneShared is a dispatch that writes the session workspace
	// directly. Two labels on one path are a real clobber.
	ConflictLaneShared = "shared"
	// ConflictLaneRetained is an isolated proposal/report world. Two labels on
	// one path are only a speculative overlap; the proposal registry is
	// authoritative for settlement.
	ConflictLaneRetained = "retained"

	ConflictKindShared             = "shared_write_conflict"
	ConflictKindRetained           = "retained_speculative_overlap"
	ConflictKindUnverifiedShared   = "shared_unverified_change"
	ConflictKindUnverifiedRetained = "retained_unverified_change"

	// conflictTrackerMaxHashBytes bounds the file read used to prove a write
	// changed content. A larger file is recorded as unknown rather than read
	// into memory.
	conflictTrackerMaxHashBytes = 32 << 20
)

// FileRecord is one observed filesystem change. Known reports whether both
// before and after states could be inspected; when it is false, the record is
// evidence of an attempted change, not proof of the resulting content.
type FileRecord struct {
	// DispatchID is unique per delegate call (including background calls).
	// SubAgentLabel is the human-readable role/description label.
	DispatchID    string
	SubAgentLabel string
	// Lane is "shared" or "retained".
	Lane string
	// FilePath is the normalized, workspace-relative path where possible.
	FilePath string
	// Key is pathKey(FilePath): case-folded on case-insensitive hosts.
	Key      string
	ToolName string

	BeforeExists bool
	AfterExists  bool
	BeforeDigest string
	AfterDigest  string
	BeforeMode   string
	AfterMode    string
	Known        bool

	// Unknown records a shell/script mutation whose paths were not observed.
	Unknown bool
	Detail  string
}

// ConflictEntry is one typed conflict, grouped by path and lane.
type ConflictEntry struct {
	Path    string
	Kind    string
	Lane    string
	Records []FileRecord
}

// ConflictReport is the typed result of one detection pass. Render is the
// model-facing view; callers that need more than prose consume Entry records.
type ConflictReport struct {
	Entries []ConflictEntry
	Unknown []FileRecord
}

// FileCount is the number of reported conflict paths plus unknown mutation
// observations, for the existing event payload.
func (r *ConflictReport) FileCount() int {
	if r == nil {
		return 0
	}
	return len(r.Entries) + len(r.Unknown)
}

// ConflictTracker records successful sub-agent writes and shell mutations for
// the parent loop. It deliberately records after execution: a failed write is
// not evidence of a change.
type ConflictTracker struct {
	mu      sync.Mutex
	records []FileRecord
}

// Record is the legacy label-only entry point. It has no before/after evidence,
// so detection treats it as an unknown mutation on a shared path.
func (ct *ConflictTracker) Record(label, filePath, toolName string) {
	ct.RecordChange(FileRecord{
		DispatchID:    label,
		SubAgentLabel: label,
		Lane:          ConflictLaneShared,
		FilePath:      filePath,
		ToolName:      toolName,
		BeforeExists:  true,
		AfterExists:   true,
		AfterDigest:   label + "\x00" + toolName,
		AfterMode:     "legacy",
		Known:         true,
	})
}

// RecordChange adds one evidence-backed change. It is called after a tool
// succeeds; no-op writes are filtered by the caller.
func (ct *ConflictTracker) RecordChange(rec FileRecord) {
	if rec.FilePath == "" {
		return
	}
	rec.FilePath = normalizeTrackedPath(rec.FilePath)
	if rec.FilePath == "" {
		return
	}
	rec.Key = pathKey(rec.FilePath)
	if rec.DispatchID == "" {
		rec.DispatchID = rec.SubAgentLabel
	}
	if rec.Lane == "" {
		rec.Lane = ConflictLaneShared
	}
	ct.mu.Lock()
	ct.records = append(ct.records, rec)
	ct.mu.Unlock()
}

// RecordUnknown records a successful shell command that may have changed
// files. The tracker cannot name the paths, so this is an explicit unknown
// rather than a false all-clear.
func (ct *ConflictTracker) RecordUnknown(dispatch ConflictDispatch, toolName, detail string) {
	rec := FileRecord{
		DispatchID:    dispatch.ID,
		SubAgentLabel: dispatch.Label,
		Lane:          dispatch.Lane,
		ToolName:      toolName,
		Unknown:       true,
		Detail:        detail,
	}
	if rec.DispatchID == "" {
		rec.DispatchID = rec.SubAgentLabel
	}
	if rec.Lane == "" {
		rec.Lane = ConflictLaneShared
	}
	ct.mu.Lock()
	ct.records = append(ct.records, rec)
	ct.mu.Unlock()
}

// DetectAndReset preserves the prose-facing API. The returned report is a view
// over the typed records; callers that need structure use DetectAndResetReport.
func (ct *ConflictTracker) DetectAndReset() string {
	report := ct.DetectAndResetReport()
	if report == nil {
		return ""
	}
	return report.Render()
}

// DetectAndResetReport consumes the current records and classifies them.
func (ct *ConflictTracker) DetectAndResetReport() *ConflictReport {
	ct.mu.Lock()
	records := ct.records
	ct.records = nil
	ct.mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	type pathLane struct {
		key  string
		lane string
	}
	byPath := make(map[pathLane][]FileRecord)
	report := &ConflictReport{}
	for _, rec := range records {
		if rec.Unknown || rec.FilePath == "" {
			report.Unknown = append(report.Unknown, rec)
			continue
		}
		lane := rec.Lane
		if lane == "" {
			lane = ConflictLaneShared
		}
		key := rec.Key
		if key == "" {
			key = pathKey(rec.FilePath)
		}
		group := pathLane{key: key, lane: lane}
		byPath[group] = append(byPath[group], rec)
	}

	for group, recs := range byPath {
		dispatches := make(map[string][]FileRecord)
		for _, rec := range recs {
			id := rec.DispatchID
			if id == "" {
				id = rec.SubAgentLabel
			}
			dispatches[id] = append(dispatches[id], rec)
		}
		if len(dispatches) < 2 {
			continue
		}
		ids := make([]string, 0, len(dispatches))
		for id := range dispatches {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		identical := true
		for i := 0; i < len(ids) && identical; i++ {
			for j := i + 1; j < len(ids); j++ {
				if !conflictGroupsIdentical(dispatches[ids[i]], dispatches[ids[j]]) {
					identical = false
					break
				}
			}
		}
		if identical {
			continue
		}

		allKnown := true
		for _, id := range ids {
			if known, _, _, _ := conflictGroupState(dispatches[id]); !known {
				allKnown = false
				break
			}
		}
		kind := ConflictKindShared
		if !allKnown {
			kind = ConflictKindUnverifiedShared
		}
		if group.lane == ConflictLaneRetained {
			kind = ConflictKindRetained
			if !allKnown {
				kind = ConflictKindUnverifiedRetained
			}
		}
		entry := ConflictEntry{
			Path:    recs[0].FilePath,
			Kind:    kind,
			Lane:    group.lane,
			Records: recs,
		}
		report.Entries = append(report.Entries, entry)
	}

	sort.Slice(report.Entries, func(i, j int) bool {
		if report.Entries[i].Lane != report.Entries[j].Lane {
			return report.Entries[i].Lane < report.Entries[j].Lane
		}
		return report.Entries[i].Path < report.Entries[j].Path
	})
	sort.Slice(report.Unknown, func(i, j int) bool {
		if report.Unknown[i].SubAgentLabel != report.Unknown[j].SubAgentLabel {
			return report.Unknown[i].SubAgentLabel < report.Unknown[j].SubAgentLabel
		}
		return report.Unknown[i].ToolName < report.Unknown[j].ToolName
	})

	if len(report.Entries) == 0 && len(report.Unknown) == 0 {
		return nil
	}
	return report
}

// conflictGroupsIdentical reports whether two dispatch groups observed the same
// resulting state. Unknown evidence is never treated as identical: the tracker
// must not turn an unobserved write into a false all-clear.
func conflictGroupsIdentical(a, b []FileRecord) bool {
	aKnown, aExists, aDigest, aMode := conflictGroupState(a)
	bKnown, bExists, bDigest, bMode := conflictGroupState(b)
	if !aKnown || !bKnown {
		return false
	}
	if aExists != bExists {
		return false
	}
	if !aExists {
		return true
	}
	return aDigest == bDigest && aMode == bMode
}

func conflictGroupState(records []FileRecord) (known, exists bool, digest, mode string) {
	if len(records) == 0 {
		return false, false, "", ""
	}
	last := records[len(records)-1]
	known = last.Known
	exists = last.AfterExists
	digest = last.AfterDigest
	mode = last.AfterMode
	for _, rec := range records {
		if !rec.Known {
			known = false
			break
		}
	}
	return known, exists, digest, mode
}

// Render renders the typed report for the parent conversation.
func (r *ConflictReport) Render() string {
	if r == nil || (len(r.Entries) == 0 && len(r.Unknown) == 0) {
		return ""
	}
	var b strings.Builder
	hasShared := false

	renderLane := func(lane, kind, header string) {
		var entries []ConflictEntry
		for _, entry := range r.Entries {
			if entry.Lane == lane && entry.Kind == kind {
				entries = append(entries, entry)
			}
		}
		if len(entries) == 0 {
			return
		}
		if lane == ConflictLaneShared {
			hasShared = true
		}
		b.WriteString(header)
		b.WriteString("\n\n")
		for _, entry := range entries {
			fmt.Fprintf(&b, "File: %s\n", entry.Path)
			for _, rec := range entry.Records {
				fmt.Fprintf(&b, "  - %s: %s\n", rec.SubAgentLabel, rec.ToolName)
			}
			b.WriteString("\n")
		}
	}

	renderUnknown := func(lane, header string) {
		counts := make(map[string]int)
		details := make(map[string]string)
		for _, rec := range r.Unknown {
			laneOf := rec.Lane
			if laneOf == "" {
				laneOf = ConflictLaneShared
			}
			if laneOf != lane {
				continue
			}
			key := rec.SubAgentLabel + "\x00" + rec.ToolName
			counts[key]++
			if details[key] == "" {
				details[key] = rec.Detail
			}
		}
		if len(counts) == 0 {
			return
		}
		keys := make([]string, 0, len(counts))
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString(header)
		b.WriteString("\n\n")
		for _, key := range keys {
			label, tool, _ := strings.Cut(key, "\x00")
			fmt.Fprintf(&b, "  - %s: %s", label, tool)
			if n := counts[key]; n > 1 {
				fmt.Fprintf(&b, " (%d calls)", n)
			}
			if details[key] != "" {
				fmt.Fprintf(&b, " (%s)", details[key])
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	renderLane(ConflictLaneShared, ConflictKindShared, "CONFLICT: Parallel workers modified the same file(s).")
	renderLane(ConflictLaneShared, ConflictKindUnverifiedShared, "UNVERIFIED CHANGES: Parallel workers changed the same path but the content could not be inspected.")
	renderLane(ConflictLaneRetained, ConflictKindRetained, "SPECULATIVE OVERLAP: Parallel workers in isolated worlds changed the same file(s). Nothing has landed; proposal list is authoritative.")
	renderLane(ConflictLaneRetained, ConflictKindUnverifiedRetained, "SPECULATIVE UNVERIFIED CHANGES: Isolated workers changed the same path but the content could not be inspected; proposal list is authoritative.")
	renderUnknown(ConflictLaneShared, "UNKNOWN MUTATIONS: Shell commands ran and may have changed files not tracked by structured write tools.")
	renderUnknown(ConflictLaneRetained, "SPECULATIVE UNKNOWN MUTATIONS: Shell commands ran in isolated worlds and may have changed files there.")

	if hasShared {
		b.WriteString("Inspect each file with the read tool, decide on the correct version,")
		b.WriteString(" and resolve with targeted edits or by re-dispatching a single worker.")
	}
	return b.String()
}

func normalizeTrackedPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	return path.Clean(p)
}
