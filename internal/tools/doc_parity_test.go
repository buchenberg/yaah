package tools

// doc_parity_test.go enforces the doc rule from epic yaah-gl5m (core
// bead yaah-two7): the JSON schema owns argument semantics; the
// embedded docs in internal/prompts/tools state behavior and policy
// only. Concretely, the test fails when
//
//   - a registered tool has no model-facing doc (or an empty
//     Description) without an explicit exemption,
//   - a tools/*.md doc is not consumed by any registered tool,
//   - a doc never names one of the tool's schema properties, or
//   - a doc claim table contradicts the code table it mirrors
//     (currently git's safe/mutating list vs allowedGitActions).
//
// Every exemption must cite the bead that removes it, so the maps
// below are the visible work queue for the epic's instance beads.

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/prompts"
)

// docForTool maps a registered tool name to the doc file base name in
// internal/prompts/tools when the two disagree. Everything else uses
// the tool name directly.
var docForTool = map[string]string{
	"todowrite":              "todo",
	"memory_search_sessions": "memory_session_search",
}

// docExemptions lists registered tools that still carry an inline
// description instead of an embedded doc. Value is the bead tracking
// the doc's creation. Empty today: proposal and supervisor gained docs
// (yaah-t8b).
var docExemptions = map[string]string{}

// propertyDocGaps lists "tool/property" pairs whose doc does not name
// the schema property yet. Value is the bead tracking the doc fix;
// these docs are all scheduled for rewrites by their instance bead, so
// naming the properties here would state a contract the rewrite is
// about to change. Empty today: the gl5m instance rewrites named every
// property, so new entries only appear when a doc regresses.
var propertyDocGaps = map[string]string{}

// docName resolves the doc base name for a registered tool.
func docName(toolName string) string {
	if doc, ok := docForTool[toolName]; ok {
		return doc
	}
	return toolName
}

func TestRegisteredTools_HaveModelFacingDocs(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Name()
		if bead, ok := docExemptions[name]; ok {
			t.Logf("%s: exempt — inline description, see %s", name, bead)
			continue
		}
		doc := docName(name)
		if got := prompts.ToolDescription(doc); got == "" {
			t.Errorf("%s: internal/prompts/tools/%s.md is missing or empty; add the doc or record a docExemptions entry citing a bead", name, doc)
		}
	}
}

func TestEveryToolDoc_IsConsumedByATool(t *testing.T) {
	used := make(map[string]bool, len(docForTool)+40)
	for _, tool := range allBuiltinTools() {
		if _, exempt := docExemptions[tool.Name()]; exempt {
			continue
		}
		used[docName(tool.Name())] = true
	}
	entries, err := prompts.ToolsFS.ReadDir("tools")
	if err != nil {
		t.Fatalf("read embedded tools dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		doc := strings.TrimSuffix(entry.Name(), ".md")
		if !used[doc] {
			t.Errorf("internal/prompts/tools/%s.md is not consumed by any registered tool", doc)
		}
	}
}

func TestToolDocs_NameEverySchemaProperty(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Name()
		if _, exempt := docExemptions[name]; exempt {
			continue
		}
		text := prompts.ToolDescription(docName(name))
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Fatalf("%s: schema is not valid JSON: %v", name, err)
		}
		for prop := range schema.Properties {
			if _, ok := propertyDocGaps[name+"/"+prop]; ok {
				continue
			}
			// Word-boundary match: a common property name (path, pattern)
			// is satisfied by prose that merely uses the word. The check's
			// contract is "the doc names the property at least once", not
			// "the doc documents it thoroughly" — a stronger guarantee
			// would need structured docs, which the repo's no-codegen rule
			// rules out for now.
			re, err := regexp.Compile(`\b` + regexp.QuoteMeta(prop) + `\b`)
			if err != nil {
				t.Fatalf("compile pattern for %s.%s: %v", name, prop, err)
			}
			if !re.MatchString(text) {
				t.Errorf("%s: doc never names schema property %q; mention it or record the drift in propertyDocGaps citing a bead", name, prop)
			}
		}
	}
}

// TestGitDoc_MatchesDangerTable pins git.md's safe/mutating action
// lists to allowedGitActions, the code table of record. The doc's
// parenthesized lists are prose, but they are the model's only
// statement of which actions need approval, so they must not drift.
func TestGitDoc_MatchesDangerTable(t *testing.T) {
	text := prompts.ToolDescription("git")
	safe, ok := docActionList(t, text, "Read-only actions (")
	if !ok {
		t.Fatal("git.md no longer states a 'Read-only actions (...)' list; update the doc and this test together")
	}
	mutating, ok := docActionList(t, text, "mutating actions (")
	if !ok {
		t.Fatal("git.md no longer states a 'mutating actions (...)' list; update the doc and this test together")
	}
	for _, action := range allowedGitActionsNames() {
		dangerous := allowedGitActions[action].dangerous
		want, wrong := safe, mutating
		if dangerous {
			want, wrong = mutating, safe
		}
		if !slices.Contains(want, action) {
			t.Errorf("git.md %s list does not include %q (danger table says dangerous=%v)", listLabel(dangerous), action, dangerous)
		}
		if slices.Contains(wrong, action) {
			t.Errorf("git.md lists %q under %s but the danger table says dangerous=%v", action, listLabel(!dangerous), dangerous)
		}
	}
	// Reverse direction: an action the doc lists but the code table does
	// not know is drift in the other direction.
	known := allowedGitActionsNames()
	for _, action := range safe {
		if !slices.Contains(known, action) {
			t.Errorf("git.md lists %q as read-only, but allowedGitActions has no such action", action)
		}
	}
	for _, action := range mutating {
		if !slices.Contains(known, action) {
			t.Errorf("git.md lists %q as mutating, but allowedGitActions has no such action", action)
		}
	}
}

// TestReadDoc_OffsetLimitUnits pins read.md to line semantics. The
// schema owns the units — offset is a 1-based line, limit a line
// count — and the byte-slicing claims this doc used to make were the
// drift that motivated the parity tests (bead yaah-yf5).
func TestReadDoc_OffsetLimitUnits(t *testing.T) {
	text := strings.ToLower(prompts.ToolDescription("read"))
	for _, claim := range []string{"byte offset", "max bytes"} {
		if strings.Contains(text, claim) {
			t.Errorf("read.md claims %q; read slices by lines per its schema (bead yaah-yf5)", claim)
		}
	}
}

func allowedGitActionsNames() []string {
	names := make([]string, 0, len(allowedGitActions))
	for name := range allowedGitActions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func listLabel(dangerous bool) string {
	if dangerous {
		return "mutating"
	}
	return "read-only"
}

// docActionList extracts the comma-separated action names from the
// first "prefix(...)" span in a doc.
func docActionList(t *testing.T, text, prefix string) ([]string, bool) {
	t.Helper()
	i := strings.Index(text, prefix)
	if i < 0 {
		return nil, false
	}
	rest := text[i+len(prefix):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		return nil, false
	}
	fields := strings.Split(rest[:j], ",")
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		if name := strings.TrimSpace(f); name != "" {
			names = append(names, name)
		}
	}
	return names, true
}
