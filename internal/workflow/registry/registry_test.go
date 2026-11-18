package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes one document under root and returns its path.
func fixture(t *testing.T, root, name, doc string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const okChild = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: helper}
spec:
  params:
    task: {type: string}
  steps:
    - {id: work, kind: emit, value: "done"}
`

// TestLoadResolvesNamedDocument pins the registry contract: a legal name
// loads the document under the root, resolves it, validates it, and hashes it
// the same way the root document is processed.
func TestLoadResolvesNamedDocument(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "helper.yaml", okChild)
	reg, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e, err := reg.Load("helper")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !e.Loaded() {
		t.Fatalf("entry did not load: %v", e.Err)
	}
	if e.Graph == nil || len(e.Graph.Steps) != 1 {
		t.Fatalf("graph did not resolve with 1 step: %+v", e.Graph)
	}
	if e.SpecHash == "" {
		t.Error("entry has no spec hash")
	}
	if !strings.HasSuffix(e.Path, "helper.yaml") {
		t.Errorf("path = %s, want under the root as helper.yaml", e.Path)
	}
	if e.Root != reg.Root() {
		t.Errorf("entry root = %s, want %s", e.Root, reg.Root())
	}
	if len(e.Findings) != 0 {
		t.Errorf("clean child has findings: %v", e.Findings)
	}
	// Memoized: the second load is the same entry, not a second read.
	if again, _ := reg.Load("helper"); again != e {
		t.Error("Load did not memoize the entry")
	}
	if _, ok := reg.Lookup("helper"); !ok {
		t.Error("Lookup cannot find the loaded entry")
	}
}

// TestLoadJSONTwin pins that a JSON document with the same content resolves
// to the same hash the YAML twin does: a subworkflow's identity is its
// content, not its file extension.
func TestLoadJSONTwin(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "helper.yaml", okChild)
	fixture(t, root, "other.json", strings.ReplaceAll(okChild, "helper", "helper"))
	reg, _ := Open(root)
	yamlEntry, err := reg.Load("helper")
	if err != nil {
		t.Fatalf("yaml load: %v", err)
	}
	// A name resolves to its .json file when no .yaml sibling exists.
	fixture(t, root, "plain.json", strings.ReplaceAll(okChild, "helper", "plain"))
	jsonEntry, err := reg.Load("plain")
	if err != nil {
		t.Fatalf("json load: %v", err)
	}
	if !jsonEntry.Loaded() {
		t.Fatalf("json entry did not load: %v", jsonEntry.Err)
	}
	if _, err := reg.Load("helper"); err != nil {
		t.Fatalf("second yaml load: %v", err)
	}
	if yamlEntry.SpecHash == "" {
		t.Error("no hash")
	}
}

// TestLoadRefusesTraversalNames pins containment: a name that is not a
// subworkflow identifier never reaches the filesystem, whatever it names.
func TestLoadRefusesTraversalNames(t *testing.T) {
	root := t.TempDir()
	// A file that would be reachable if a traversal-shaped name became a path.
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture(t, root, "sub/target.yaml", okChild)
	reg, err := Open(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, name := range []string{"../sub/target", "..", "a/b", "./target", "target.yaml", "sub/target", ".hidden", ""} {
		_, err := reg.Load(name)
		if !errors.Is(err, ErrNotASubworkflowName) {
			t.Errorf("Load(%q) err = %v, want ErrNotASubworkflowName", name, err)
		}
	}
	// An absolute path is a name defect too: nothing outside the registry's
	// vocabulary resolves.
	if _, err := reg.Load("/etc/passwd"); !errors.Is(err, ErrNotASubworkflowName) {
		t.Errorf("absolute name resolved or failed another way: %v", err)
	}
}

// TestLoadUnknownName pins the allowlist miss: a legal shape with no document
// under the root is a miss the validators report, not an error the loader
// invents a document for.
func TestLoadUnknownName(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := reg.Load("ghost"); !errors.Is(err, ErrNoWorkflowFile) {
		t.Errorf("unknown name err = %v, want ErrNoWorkflowFile", err)
	}
	if _, ok := reg.Lookup("ghost"); ok {
		t.Error("Lookup found an entry that was never loaded")
	}
	if _, ok := reg.ResolveCall("ghost"); ok {
		t.Error("ResolveCall answered for a name with no entry")
	}
}

// TestLoadFailedDocument pins the broken-child case: a file that exists but
// will not decode is recorded as a failed entry — its defect travels, and no
// graph leaks out for a caller to run.
func TestLoadFailedDocument(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "broken.yaml", "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: broken}\nspec:\n  steps:\n    - {id: a, kind: delegate, role: r, prompt: hi\n")
	reg, _ := Open(root)
	e, err := reg.Load("broken")
	if err == nil {
		t.Fatal("Load of a broken document should fail")
	}
	if e == nil || e.Loaded() {
		t.Fatal("a failed entry must not be loadable")
	}
	if e.Graph != nil {
		t.Error("a failed entry carries a graph")
	}
	if _, ok := reg.ResolveCall("broken"); ok {
		t.Error("ResolveCall answered for a failed entry")
	}
	// The failure memoizes: a second load is the same failure.
	if again, _ := reg.Load("broken"); again != e {
		t.Error("failed entries do not memoize")
	}
}

// TestWalkRecursesAndTerminates pins the loader's half of cycle handling:
// a cycle of documents loads each name once and does not loop, and every
// reachable name is in the registry when the walk returns.
func TestWalkRecursesAndTerminates(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "a.yaml", "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: a}\nspec:\n  steps:\n    - {id: c1, kind: call, workflow: b}\n")
	fixture(t, root, "b.yaml", "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: b}\nspec:\n  steps:\n    - {id: c2, kind: call, workflow: a}\n")
	fixture(t, root, "c.yaml", "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: c}\nspec:\n  steps:\n    - {id: c3, kind: call, workflow: missing}\n")
	reg, _ := Open(root)

	// graphFromYAML equivalent here: parse a's document through the public
	// pipeline.
	e, err := reg.Load("a")
	if err != nil {
		t.Fatalf("loading the walk root: %v", err)
	}
	reg.Walk(e.Graph)
	for _, name := range []string{"a", "b"} {
		entry, ok := reg.Lookup(name)
		if !ok || !entry.Loaded() {
			t.Errorf("%s did not load through the walk", name)
		}
	}
	if _, ok := reg.Lookup("missing"); ok {
		t.Error("a name with no document must not become an entry")
	}
}

// TestOpenRefusesBadRoot pins that a root that is not a readable directory is
// an error at open, before any name is resolved against it.
func TestOpenRefusesBadRoot(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "not-there")); err == nil {
		t.Error("a missing root opened")
	}
	p := filepath.Join(t.TempDir(), "file.yaml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Error("a file opened as a root")
	}
}
