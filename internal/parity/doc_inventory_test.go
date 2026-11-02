package parity

// doc_inventory_test.go derives the prose inventories from the same sources
// the code uses. These are the repeated-claim failures from the 2026-06 audit:
// a pipeline list, a host-only tool list, and a package tree that drifted from
// the code. When an inventory is worth stating in prose, it gets a check.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
)

func TestArchitectureNamesDefaultPipelineInOrder(t *testing.T) {
	doc := read(t, "../../docs/architecture.md")
	const startMarker = "The pipeline is built in `buildPipeline()` with a config-driven order:"
	start := strings.Index(doc, startMarker)
	if start < 0 {
		t.Fatalf("architecture.md no longer has the pipeline inventory marker %q", startMarker)
	}
	rest := doc[start+len(startMarker):]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatalf("architecture.md pipeline list has no terminating blank line")
	}
	lines := regexp.MustCompile(`(?m)^\d+\.\s+`+"`([a-z_]+)`").FindAllStringSubmatch(rest[:end], -1)
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		got = append(got, line[1])
	}
	if len(got) == 0 {
		t.Fatalf("architecture.md pipeline list has no numbered backticked middleware names")
	}
	want := pipeline.DefaultPipelineNames()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("architecture.md pipeline list = %v, defaultPipelineNames = %v", got, want)
	}
}

func TestFeaturesNamesEveryHostOnlyLeafTool(t *testing.T) {
	doc := read(t, "../../docs/features.md")
	for _, name := range tools.HostOnlyLeafTools() {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("features.md does not name host-only tool %q; HostOnlyLeafTools returned %v", name, tools.HostOnlyLeafTools())
		}
	}
}

func TestCodeOrganizationNamesEveryInternalPackage(t *testing.T) {
	doc := read(t, "../../docs/code-organization.md")
	entries, err := os.ReadDir(filepath.Clean("../../internal"))
	if err != nil {
		t.Fatalf("read internal packages: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.Contains(doc, "── "+entry.Name()+"/") {
			t.Errorf("code-organization.md does not name internal/%s/; the tree must match `ls internal/`", entry.Name())
		}
	}
}

// TestFeaturesPlanModeNamesInspectionSet pins the Plan mode section of
// features.md to readOnlyToolNames, the code-side source of truth for the
// inspection set. The prose inventory drifted from the code in the 2026-06
// audit; this keeps the tool list and the vocabulary from drifting again.
func TestFeaturesPlanModeNamesInspectionSet(t *testing.T) {
	doc := read(t, "../../docs/features.md")
	start := strings.Index(doc, "## Plan mode")
	if start < 0 {
		t.Fatalf("features.md no longer has a ## Plan mode section")
	}
	section := doc[start:]
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	for _, name := range agent.InspectionSetNames() {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("features.md Plan mode section does not name %q (inspection set: %v)", name, agent.InspectionSetNames())
		}
	}
	if strings.Contains(section, "read-only tools") {
		t.Errorf("features.md Plan mode section still says %q; the vocabulary is now \"inspection set\"", "read-only tools")
	}
	if !strings.Contains(section, "inspection set") {
		t.Errorf("features.md Plan mode section does not say \"inspection set\"")
	}
}
