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
	const startMarker = "The pipeline is built in `buildPipeline()` in a fixed order:"
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

// TestAgentsLayoutNamesEveryInternalPackage applies the same rule to AGENTS.md.
// Its layout is the routing table every agent session loads, so a package
// missing from it misroutes work to the wrong directory. code-organization.md
// was already gated and stayed current; AGENTS.md was not, and drifted.
func TestAgentsLayoutNamesEveryInternalPackage(t *testing.T) {
	doc := read(t, "../../AGENTS.md")
	entries, err := os.ReadDir(filepath.Clean("../../internal"))
	if err != nil {
		t.Fatalf("read internal packages: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.Contains(doc, "── "+entry.Name()+"/") {
			t.Errorf("AGENTS.md repo layout does not name internal/%s/; the tree must match `ls internal/`", entry.Name())
		}
	}
}

// planModeSection returns features.md's Plan mode section: from the heading to
// the next "## " heading, or to the end of the page when Plan mode is the last
// section. The bound is checked rather than assumed — a restructure that moves
// the section must fail here, because the checks below read the section as the
// inventory.
func planModeSection(t *testing.T, doc string) string {
	t.Helper()
	const heading = "## Plan mode"
	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("features.md no longer has a %q section", heading)
	}
	rest := doc[start+len(heading):]
	end := strings.Index(rest, "\n## ")
	if end < 0 {
		// No following section: Plan mode is the page's last one, so the tail
		// is the section. Assert that instead of assuming it — any other
		// heading level in the tail means the boundary inference missed and
		// the inventory scan would be reading someone else's prose.
		if m := headingLine.FindString(rest); m != "" {
			t.Fatalf("features.md %q section is no longer bounded: %q follows it, so the section boundary must be updated", heading, m)
		}
		end = len(rest)
	}
	section := rest[:end]
	if strings.TrimSpace(section) == "" {
		t.Fatalf("features.md %q section is empty", heading)
	}
	return section
}

// planModeInventory returns the tool names in the Plan mode section's
// inventory — the run of backticked names introduced by the "inspection set"
// marker and terminated by the next blank line. Reading that paragraph rather
// than the whole section is what lets the reverse check below be honest: the
// section also backticks git subcommands, flags and file names, none of which
// are tools.
func planModeInventory(t *testing.T, section string) []string {
	t.Helper()
	const marker = "narrows the toolset to the **inspection set**"
	start := strings.Index(section, marker)
	if start < 0 {
		t.Fatalf("features.md Plan mode section no longer introduces the inventory with %q", marker)
	}
	rest := section[start:]
	gap := strings.Index(rest, "\n\n")
	if gap < 0 {
		t.Fatalf("features.md Plan mode inventory has no blank line after %q", marker)
	}
	list := strings.TrimLeft(rest[gap:], "\n")
	if end := strings.Index(list, "\n\n"); end >= 0 {
		list = list[:end]
	}
	names := make([]string, 0, len(agent.InspectionSetNames()))
	for _, m := range backtickedName.FindAllStringSubmatch(list, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatalf("features.md Plan mode inventory paragraph has no backticked tool names: %q", list)
	}
	return names
}

var (
	headingLine    = regexp.MustCompile(`(?m)^#{1,6} [^\n]*`)
	backtickedName = regexp.MustCompile("`([a-z_]+)`")
)

// TestFeaturesPlanModeNamesInspectionSet pins the Plan mode section of
// features.md to readOnlyToolNames, the code-side source of truth for the
// inspection set — in both directions. The prose inventory drifted from the
// code in the 2026-06 audit (a name it dropped, and names it invented), so the
// check keeps both from drifting again: every tool in the code's set must be
// named, and every name the prose lists must be a tool in the set.
func TestFeaturesPlanModeNamesInspectionSet(t *testing.T) {
	doc := read(t, "../../docs/features.md")
	section := planModeSection(t, doc)

	names := agent.InspectionSetNames()
	for _, name := range names {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("features.md Plan mode section does not name %q (inspection set: %v)", name, names)
		}
	}

	inSet := make(map[string]bool, len(names))
	for _, name := range names {
		inSet[name] = true
	}
	inventory := planModeInventory(t, section)
	for _, name := range inventory {
		if !inSet[name] {
			t.Errorf("features.md Plan mode inventory names %q, which is not in the inspection set %v (readOnlyToolNames, internal/agent/tools.go)", name, names)
		}
	}

	if strings.Contains(section, "read-only tools") {
		t.Errorf("features.md Plan mode section still says %q; the vocabulary is now \"inspection set\"", "read-only tools")
	}
	if !strings.Contains(section, "inspection set") {
		t.Errorf("features.md Plan mode section does not say \"inspection set\"")
	}
}
