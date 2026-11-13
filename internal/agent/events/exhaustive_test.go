package events_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/events"
)

// allEvents returns one instance of every concrete Event type.
// When you add an Event, ADD IT HERE. The exhaustive tests below
// then force you to acknowledge it in every View consumer.
func allEvents() []events.Event {
	return []events.Event{
		&events.TokenDeltaEvent{},
		&events.ThinkingEvent{},
		&events.FlushEvent{},
		&events.ToolStartEvent{},
		&events.ToolEndEvent{},
		&events.SubAgentStartEvent{},
		&events.SubAgentEndEvent{},
		&events.EscalationEvent{},
		&events.TitleSetEvent{},
		&events.AgentChangedEvent{},
		&events.ModeChangedEvent{},
		&events.WakeEvent{},
		&events.DoneEvent{},
		&events.CompactionStartedEvent{},
		&events.CompactionDoneEvent{},
	}
}

// TestAllEventsRegistryIsComplete cross-checks the allEvents registry
// against every "*Event struct" declaration in events.go so a new
// event type cannot silently dodge registration.
func TestAllEventsRegistryIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range allEvents() {
		name := reflect.TypeOf(e).Elem().Name()
		if seen[name] {
			t.Errorf("duplicate event %s in allEvents()", name)
		}
		seen[name] = true
	}

	eventsSrc := readFile(t, "events.go")
	sc := bufio.NewScanner(strings.NewReader(eventsSrc))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "type ") || !strings.HasSuffix(line, "Event struct") {
			continue
		}
		if strings.HasPrefix(line, "type Event") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "type "), " struct")
		if !seen[name] {
			t.Errorf("event %s declared in events.go but missing from allEvents()", name)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan events.go: %v", err)
	}
}

// consumerScanDirs is where every terminal consumer of the sealed Event
// interface lives: internal/ holds the TUI and ACP views, cmd/yaah/ holds the
// REPL and web views. The walk is confined to these two subtrees to keep the
// gate fast — scanning the whole repository would also parse the dispatch-lane
// copies under .yaah/ (the dot-prefix skip below keeps them out, but the
// point stands). knownConsumers pins the restriction honest: if a view ever
// moves outside these trees, the floor assertion fails and the list is
// widened deliberately rather than silently losing a consumer.
var consumerScanDirs = []string{"internal", "cmd"}

// knownConsumers is the sanity floor for derivation. These are the views that
// were hardcoded here before the list became derived; each one must still turn
// up in the walk, with its handle-able shape intact.
var knownConsumers = []string{
	"internal/tui/proxy.go",
	"internal/acp/view.go",
	"cmd/yaah/view_terminal.go",
	"cmd/yaah/web_view.go",
}

// loopPackageDir is excluded from the gated set on purpose. The agent
// package's own view.go is the loop side of the interface: NoopView swallows
// everything and BrokerView.forward hands the event to whichever consumer is
// registered. Neither interprets events, so requiring case clauses there would
// push knowledge that belongs at the edges back into the loop. Only the files
// directly in internal/agent are skipped — a subpackage of it that grows a
// real consumer is still gated.
const loopPackageDir = "internal/agent"

// TestConsumersHandleEveryEvent asserts that every View consumer acknowledges
// every event type in a real type-switch case clause. The consumer list is
// derived by walking consumerScanDirs for HandleEvent methods (no hardcoded
// file list to go stale), and acknowledgement is parsed with go/ast so a
// mention in a comment or a string literal no longer satisfies the gate.
// Because package agent re-exports every event type as an alias, both
// `case *agent.XxxEvent:` and `case *events.XxxEvent:` count — they are the
// same type. Deliberate ignoring must be an explicit case with a comment, not
// a silent fallthrough.
func TestConsumersHandleEveryEvent(t *testing.T) {
	names := eventTypeNames()
	consumers := discoverConsumers(t)

	derived := make(map[string]bool, len(consumers))
	for _, rel := range consumers {
		derived[rel] = true
	}
	t.Logf("gated consumers (%d): %s", len(consumers), strings.Join(consumers, ", "))

	var lost []string
	for _, rel := range knownConsumers {
		if !derived[rel] {
			lost = append(lost, rel)
		}
	}
	if len(lost) > 0 {
		t.Fatalf("consumer derivation lost known views %v — the walk over %v or the "+
			"HandleEvent signature check is broken. Derived: %v",
			lost, consumerScanDirs, consumers)
	}

	for _, rel := range consumers {
		missing := missingEvents(handledEventNames(t, rel), names)
		for _, name := range missing {
			t.Errorf("%s does not handle %s: no `case *agent.%s:` or `case *events.%s:` type-switch "+
				"clause anywhere in this file (add a case, or an explicit 'intentionally ignored' case)",
				rel, name, name, name)
		}
	}
}

// srcViewAgentSpell is a consumer written against the agent re-export; it
// handles exactly one event, DoneEvent.
const srcViewAgentSpell = `package view

import "github.com/buchenberg/yaah/internal/agent"

func (v *view) HandleEvent(evt agent.Event) {
	switch evt.(type) {
	case *agent.DoneEvent:
		v.done()
	}
}
`

// srcViewEventsSpell is the same consumer written against the events package
// directly — the alias spelling the old regexp-based matcher missed.
const srcViewEventsSpell = `package view

import "github.com/buchenberg/yaah/internal/agent/events"

func (v *view) HandleEvent(evt events.Event) {
	switch evt.(type) {
	case *events.DoneEvent:
		v.done()
	}
}
`

// srcViewNoopShape is the loop-side shape: a HandleEvent method with an
// unnamed single Event parameter and no switch at all.
const srcViewNoopShape = `package agent

func (NoopView) HandleEvent(Event) {}
`

// srcMentionsOutsideCases never type-switches: the event clause appears only
// in a comment, in a string literal, and as a case of a plain value switch.
const srcMentionsOutsideCases = `package view

import "github.com/buchenberg/yaah/internal/agent"

func (v *view) HandleEvent(evt agent.Event) {
	// case *agent.DoneEvent: acknowledged in prose, which is not handling.
	label := "case *agent.DoneEvent: a string that only looks like a clause"
	switch label {
	case "case *agent.DoneEvent:":
		v.done()
	default:
	}
}
`

// srcValueTypeClause switches on the event type but lists the value form, not
// the pointer form every consumer actually receives.
const srcValueTypeClause = `package view

import "github.com/buchenberg/yaah/internal/agent"

func (v *view) HandleEvent(evt agent.Event) {
	switch evt.(type) {
	case agent.DoneEvent:
		v.done()
	}
}
`

// TestGateMatchesCaseClausesNotSubstrings proves the matcher's two properties
// on synthetic sources, so the bite is asserted without mutating a consumer:
// an alias spelling counts, a comment or string mention does not, and a
// missing case clause lands the event in the reported failure set.
func TestGateMatchesCaseClausesNotSubstrings(t *testing.T) {
	names := eventTypeNames()

	handled := eventNamesInTypeSwitches(parseSnippet(t, "agent.go", srcViewAgentSpell))
	if !handled["DoneEvent"] {
		t.Error("`case *agent.DoneEvent:` in a type switch was not recognised as handling")
	}
	handled = eventNamesInTypeSwitches(parseSnippet(t, "events.go", srcViewEventsSpell))
	if !handled["DoneEvent"] {
		t.Error("`case *events.DoneEvent:` in a type switch was not recognised as handling")
	}

	handled = eventNamesInTypeSwitches(parseSnippet(t, "mentions.go", srcMentionsOutsideCases))
	if len(handled) != 0 {
		t.Errorf("comment/string/value-switch mentions must not count as handling, got %v", handled)
	}

	handled = eventNamesInTypeSwitches(parseSnippet(t, "value.go", srcValueTypeClause))
	if len(handled) != 0 {
		t.Errorf("a non-pointer type-switch clause must not count as handling, got %v", handled)
	}

	// The bite: a consumer that handles only DoneEvent is reported missing
	// every other registered event — which is what happens in the gate above
	// when a case clause is dropped from a real view.
	missing := missingEvents(eventNamesInTypeSwitches(parseSnippet(t, "agent.go", srcViewAgentSpell)), names)
	if len(missing) != len(names)-1 {
		t.Fatalf("expected %d events missing, got %d (%v)", len(names)-1, len(missing), missing)
	}
	for _, name := range missing {
		if name == "DoneEvent" {
			t.Fatal("DoneEvent was reported missing even though the case clause is present")
		}
	}
}

// TestGateBitesOnRealConsumerSource takes a shipped view's own source, deletes
// one case clause in memory, and asserts the gate reports exactly that event as
// unhandled. This is the mutation proof that the gate bites, without touching a
// consumer file — and it fails loudly if the clause it relies on is renamed or
// reformatted, so the proof cannot quietly go stale.
func TestGateBitesOnRealConsumerSource(t *testing.T) {
	const rel = "internal/tui/proxy.go"
	src := readRepoFile(t, rel)
	mutated := strings.Replace(src, "\tcase *agent.WakeEvent:\n", "", 1)
	if mutated == src {
		t.Fatalf("`case *agent.WakeEvent:` not found in %s — update this proof", rel)
	}
	handled := eventNamesInTypeSwitches(parseSnippet(t, rel, mutated))
	missing := missingEvents(handled, eventTypeNames())
	if len(missing) != 1 || missing[0] != "WakeEvent" {
		t.Errorf("deleting the WakeEvent case clause from %s should leave exactly that event "+
			"unhandled, got %v", rel, missing)
	}
}

// TestGateDetectsViewHandleEventSignature pins what the derivation accepts as
// a consumer: a method named HandleEvent with exactly one Event-typed
// parameter, spelled Event, agent.Event or events.Event.
func TestGateDetectsViewHandleEventSignature(t *testing.T) {
	if !declaresHandleEvent(parseSnippet(t, "agent.go", srcViewAgentSpell)) {
		t.Error("HandleEvent(agent.Event) should be derived as a consumer")
	}
	if !declaresHandleEvent(parseSnippet(t, "events.go", srcViewEventsSpell)) {
		t.Error("HandleEvent(events.Event) should be derived as a consumer")
	}
	if !declaresHandleEvent(parseSnippet(t, "noop.go", srcViewNoopShape)) {
		t.Error("HandleEvent(Event) should be derived as a consumer (it is excluded by directory, not by shape)")
	}
	if declaresHandleEvent(parseSnippet(t, "arity.go", `package view

import "github.com/buchenberg/yaah/internal/agent"

func (v *view) HandleEvent(a, b agent.Event) {}
`)) {
		t.Error("a two-parameter HandleEvent is not the View shape")
	}
	if declaresHandleEvent(parseSnippet(t, "plain.go", `package view

import "github.com/buchenberg/yaah/internal/agent"

func HandleEvent(evt agent.Event) {}
`)) {
		t.Error("a function, not a method, is not a consumer")
	}
	if declaresHandleEvent(parseSnippet(t, "other.go", `package view

import "github.com/buchenberg/yaah/internal/agent"

func (v *view) HandleEvent(update agent.SessionUpdate) {}
`)) {
		t.Error("HandleEvent taking a non-Event type is not a consumer")
	}
}

// eventTypeNames returns the concrete type names registered in allEvents(),
// sorted so failures are reported in a stable order.
func eventTypeNames() []string {
	all := allEvents()
	names := make([]string, 0, len(all))
	for _, e := range all {
		names = append(names, reflect.TypeOf(e).Elem().Name())
	}
	sort.Strings(names)
	return names
}

// missingEvents returns the registered names the consumer's handled set does
// not cover.
func missingEvents(handled map[string]bool, names []string) []string {
	var missing []string
	for _, name := range names {
		if !handled[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// repoRoot returns the repository root. This file lives at
// internal/agent/events/exhaustive_test.go, three levels below it, and the
// go.mod check keeps that assumption from silently pointing somewhere else.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("derived repo root %s has no go.mod: %v", root, err)
	}
	return root
}

// discoverConsumers walks consumerScanDirs from the repo root and returns,
// repo-relative, slash-separated and sorted, every non-test .go file that
// declares a HandleEvent method taking exactly one Event. Hidden directories
// (.yaah/ dispatch worktrees, .git, .agents), vendor/ and testdata/ are not
// entered, and files in loopPackageDir are left out by design.
func discoverConsumers(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var found []string
	for _, dir := range consumerScanDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("consumer walk root %s: %v", base, err)
		}
		walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != base && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata") {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(mustRelativize(t, root, path))
			if filepath.Dir(rel) == loopPackageDir {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			// Cheap pre-filter: only a file that spells the method name can
			// declare it, so parsing is reserved for a handful of candidates.
			if !strings.Contains(string(src), "HandleEvent") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, src, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", rel, parseErr)
			}
			if declaresHandleEvent(file) {
				found = append(found, rel)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", dir, walkErr)
		}
	}
	sort.Strings(found)
	return found
}

// mustRelativize turns a walk path into a repo-relative path.
func mustRelativize(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relativize %s under %s: %v", path, root, err)
	}
	return rel
}

// declaresHandleEvent reports whether the file declares a method named
// HandleEvent with exactly one parameter of type Event, agent.Event or
// events.Event — the agent.View shape.
func declaresHandleEvent(file *ast.File) bool {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "HandleEvent" || fn.Type == nil {
			continue
		}
		params := fn.Type.Params
		if params == nil || len(params.List) != 1 || paramCount(params) != 1 {
			continue
		}
		if isEventInterfaceType(params.List[0].Type) {
			return true
		}
	}
	return false
}

// paramCount counts declared parameters, expanding grouped names — a field
// with two names is two parameters — and rejecting a variadic signature,
// which go/ast spells as an *ast.Ellipsis parameter type.
func paramCount(fields *ast.FieldList) int {
	n := 0
	for _, f := range fields.List {
		if _, variadic := f.Type.(*ast.Ellipsis); variadic {
			return -1
		}
		if len(f.Names) == 0 {
			n++
			continue
		}
		n += len(f.Names)
	}
	return n
}

// isEventInterfaceType matches the Event interface spelled Event (as written
// inside package agent), agent.Event or events.Event.
func isEventInterfaceType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "Event"
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return false
		}
		return (pkg.Name == "agent" || pkg.Name == "events") && e.Sel.Name == "Event"
	}
	return false
}

// handledEventNames returns the event types the consumer at rel acknowledges
// in a type-switch case clause. The scope is the whole file on purpose: some
// views dispatch through more than one switch (internal/acp/view.go translates
// in View.SendTo and only forwards from ViewWithWrite.HandleEvent), so the
// gate asks whether the file acknowledges the event, not where.
func handledEventNames(t *testing.T, rel string) map[string]bool {
	t.Helper()
	return eventNamesInTypeSwitches(parseSnippet(t, rel, readRepoFile(t, rel)))
}

// eventNamesInTypeSwitches collects the event type names listed in the case
// clauses of every type switch in the file.
func eventNamesInTypeSwitches(file *ast.File) map[string]bool {
	handled := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.TypeSwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				if name, ok := caseEventName(expr); ok {
					handled[name] = true
				}
			}
		}
		return true
	})
	return handled
}

// caseEventName extracts the type name from a case clause expressed as
// *agent.XxxEvent or *events.XxxEvent. Other spellings — a value type, another
// package qualifier, a plain constant — are not event acknowledgement.
func caseEventName(expr ast.Expr) (string, bool) {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || (pkg.Name != "agent" && pkg.Name != "events") {
		return "", false
	}
	return sel.Sel.Name, true
}

// parseSnippet parses src without type-checking: the gate is deliberately
// syntactic, since a full typed load of every consumer package would need the
// whole module to build.
func parseSnippet(t *testing.T, name, src string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file
}

// readRepoFile reads a repo-root-relative path through readFile, so every path
// in this test resolves against this file's own location rather than the
// working directory the test binary happens to run from.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	return readFile(t, filepath.Join("..", "..", "..", filepath.FromSlash(rel)))
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
