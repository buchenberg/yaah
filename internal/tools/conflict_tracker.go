package tools

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type FileRecord struct {
	SubAgentLabel string
	FilePath      string
	ToolName      string
}

type ConflictTracker struct {
	mu      sync.Mutex
	records []FileRecord
}

func (ct *ConflictTracker) Record(label, filePath, toolName string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.records = append(ct.records, FileRecord{
		SubAgentLabel: label,
		FilePath:      filePath,
		ToolName:      toolName,
	})
}

type opGroup struct {
	label string
	tools map[string]bool
}

func (ct *ConflictTracker) DetectAndReset() string {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	byFile := make(map[string][]FileRecord)
	for _, r := range ct.records {
		byFile[r.FilePath] = append(byFile[r.FilePath], r)
	}

	ct.records = nil

	type conflictFile struct {
		path string
		ops  []opGroup
	}

	var conflicts []conflictFile
	for path, records := range byFile {
		groups := make(map[string]*opGroup)
		for _, r := range records {
			g, ok := groups[r.SubAgentLabel]
			if !ok {
				g = &opGroup{label: r.SubAgentLabel, tools: make(map[string]bool)}
				groups[r.SubAgentLabel] = g
			}
			g.tools[r.ToolName] = true
		}
		if len(groups) < 2 {
			continue
		}
		var ops []opGroup
		for _, g := range groups {
			ops = append(ops, *g)
		}
		sort.Slice(ops, func(i, j int) bool { return ops[i].label < ops[j].label })
		conflicts = append(conflicts, conflictFile{path: path, ops: ops})
	}

	if len(conflicts) == 0 {
		return ""
	}

	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].path < conflicts[j].path })

	var sb strings.Builder
	sb.WriteString("CONFLICT: Parallel workers modified the same file(s).\n\n")
	for _, cf := range conflicts {
		sb.WriteString(fmt.Sprintf("File: %s\n", cf.path))
		for _, g := range cf.ops {
			toolNames := make([]string, 0, len(g.tools))
			for t := range g.tools {
				toolNames = append(toolNames, t)
			}
			sort.Strings(toolNames)
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", g.label, strings.Join(toolNames, ", ")))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Inspect each file with the read tool, decide on the correct version,")
	sb.WriteString(" and resolve with targeted edits or by re-dispatching a single worker.")

	return sb.String()
}
