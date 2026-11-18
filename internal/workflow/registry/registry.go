// Package registry resolves a call node's `workflow` name to a validated,
// resolved subworkflow document (yaah-0u0v.23).
//
// The registry is the plan's answer to its open question 6: a call names a
// workflow, not a file. Names resolve against an explicit root directory the
// operator declared with --workflow-root, and nothing else. File-path includes
// ("./sub.yaml") are refused by construction: a name that is not a legal
// subworkflow identifier never becomes a path, and the path a legal name maps
// to is re-checked to stay inside the root, so containment holds twice.
//
// A subworkflow is loaded exactly the way the root document is: read within
// the decoder's size limit, strict-decoded, resolved into the workflow graph,
// and run through the document-intrinsic validator families. Its findings are
// the entry's, for the caller to report alongside the root's — a document that
// calls a broken workflow is a document with a defect, and the author needs to
// see the defect in the child, with the child's file named.
//
// The registry holds no judgment about graphs (cycles and depth are the
// validators' business, over graphs the registry merely supplies) and performs
// no run-time work: it is read-only by construction, and the executor resolves
// calls through the host door, not through this package.
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
	"github.com/buchenberg/yaah/internal/workflow/validate"
)

// Load errors. They name which side of the lookup failed, because the caller
// reports each differently: a name that is not a name is the call site's
// defect, a name with no file under the root is an allowlist miss, and a file
// that exists but will not load is the child's defect, recorded on its entry.
var (
	// ErrNotASubworkflowName marks a name that is not a subworkflow
	// identifier (path traversal shapes among them).
	ErrNotASubworkflowName = errors.New("not a subworkflow name")
	// ErrNoWorkflowFile marks a legal name with no document under the root.
	ErrNoWorkflowFile = errors.New("no workflow file under the registry root")
)

// Entry is one resolved subworkflow. Graph and SpecHash are set only when the
// document loaded cleanly; Findings carries the document-intrinsic families'
// diagnostics over the loaded graph; Err names the load failure otherwise, and
// a failed entry carries no graph — a document the registry could not resolve
// may not run, and handing out a half-graph would let a caller pretend it could.
type Entry struct {
	Name string
	// Path is the file under Root the entry was loaded from, absolute.
	Path     string
	Root     string
	Document *spec.Document
	SpecHash string
	Graph    *ir.Graph
	Findings []validate.Diagnostic
	Err      error
}

// Loaded reports whether the entry is a usable subworkflow.
func (e *Entry) Loaded() bool {
	return e != nil && e.Err == nil && e.Graph != nil
}

// Registry is the set of subworkflows loaded under one root. Entries memoize:
// loading a name twice reads the file once, and a cycle of documents resolves
// to one entry per name rather than recursing into itself.
type Registry struct {
	root    string
	order   []string
	entries map[string]*Entry
}

// Open returns a registry rooted at dir, refusing a root that is not a
// readable directory: a root that does not exist makes every call an unknown
// name, and refusing the root up front states that louder than N findings.
func Open(dir string) (*Registry, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("workflow root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workflow root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workflow root %s is not a directory", dir)
	}
	return &Registry{
		root:    abs,
		entries: map[string]*Entry{},
	}, nil
}

// Root is the directory names resolve under, absolute.
func (r *Registry) Root() string { return r.root }

// Names lists the loaded subworkflows in load order.
func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

// Lookup returns the entry loaded under name, or false when the registry has
// none — an unresolvable name is the validators' finding to report, not this
// method's error, because the same miss (a call the registry cannot serve)
// reads differently in validation, analysis, and the run gate.
func (r *Registry) Lookup(name string) (*Entry, bool) {
	e, ok := r.entries[name]
	return e, ok
}

// ResolveCall adapts the registry to validate's CallResolver seam: the named
// document's graph when it loaded cleanly, and nothing when it did not. A
// failed entry answers false so the validators report the name from the call
// site rather than the loader reporting a defect twice.
func (r *Registry) ResolveCall(name string) (validate.CallDocument, bool) {
	e, ok := r.Lookup(name)
	if !ok || !e.Loaded() {
		return validate.CallDocument{}, false
	}
	return validate.CallDocument{Name: e.Name, SpecHash: e.SpecHash, Graph: e.Graph}, true
}

// resolve maps a legal name to its candidate files under the root, never
// leaving the root. The name's shape already refuses every separator, so the
// containment check is defense in depth against a future change in the shape
// rule rather than the only guard.
func (r *Registry) resolve(name string) ([]string, error) {
	if !spec.ValidSubworkflowName(name) {
		return nil, fmt.Errorf("%w: %q", ErrNotASubworkflowName, name)
	}
	candidates := []string{
		filepath.Join(r.root, name+".yaml"),
		filepath.Join(r.root, name+".json"),
	}
	for _, c := range candidates {
		if !pathInsideRoot(r.root, c) {
			return nil, fmt.Errorf("%w: %q resolves outside the registry root", ErrNotASubworkflowName, name)
		}
	}
	return candidates, nil
}

func pathInsideRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Load reads, decodes, resolves, and validates one subworkflow, memoizing the
// entry whether it loaded or failed. The document-intrinsic families
// (structural, values, linearity) run here, so an entry's Findings is exactly
// what the root's own analysis would report about that document; the
// host-relative families run where the root's run, because they price against
// this host's configuration.
//
// A child's findings do not stop the load: validation is reporting, and the
// caller decides severity. Only a document that will not decode or resolve
// fails the entry.
func (r *Registry) Load(name string) (*Entry, error) {
	if e, ok := r.entries[name]; ok {
		return e, e.Err
	}
	candidates, err := r.resolve(name)
	if err != nil {
		return nil, err
	}
	path := ""
	for _, c := range candidates {
		if info, serr := os.Stat(c); serr == nil && !info.IsDir() && info.Size() <= spec.MaxSpecBytes {
			path = c
			break
		}
	}
	if path == "" {
		return nil, ErrNoWorkflowFile
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, rerr
	}
	e := &Entry{Name: name, Path: path, Root: r.root}
	doc, derr := spec.Parse(data)
	if derr != nil {
		e.Err = fmt.Errorf("%s: %w", path, derr)
		r.remember(e)
		return e, e.Err
	}
	if hash, herr := spec.SpecHash(doc); herr == nil {
		e.SpecHash = hash
	} else {
		e.Err = fmt.Errorf("%s: %w", path, herr)
		r.remember(e)
		return e, e.Err
	}
	g, gerr := ir.Resolve(doc)
	if gerr != nil {
		e.Err = fmt.Errorf("%s: %w", path, gerr)
		r.remember(e)
		return e, e.Err
	}
	e.Document = doc
	e.Graph = g
	findings := validate.Structural(g)
	findings = append(findings, validate.Values(doc, g)...)
	findings = append(findings, validate.Linearity(g)...)
	e.Findings = findings
	r.remember(e)
	return e, nil
}

func (r *Registry) remember(e *Entry) {
	if _, ok := r.entries[e.Name]; !ok {
		r.order = append(r.order, e.Name)
	}
	r.entries[e.Name] = e
}

// Walk loads every subworkflow reachable from g's call nodes, and every
// subworkflow those reach, transitively. Each name loads once; a cycle of
// documents terminates because the second visit is a memo hit, not a load.
// The validator reports the cycle; the loader only refuses to loop.
//
// A name that fails to load is skipped — walk supplies the manager with the
// entries it could build, and the names it could not are findings the Calls
// validator reports from the graph, in the document that named them.
func (r *Registry) Walk(g *ir.Graph) {
	if g == nil {
		return
	}
	r.walk(g)
}

func (r *Registry) walk(g *ir.Graph) {
	for _, n := range callNodes(g) {
		name := n.Workflow
		if name == "" {
			continue // classifyKind and WF014's business, not the loader's
		}
		if _, ok := r.entries[name]; ok {
			continue
		}
		if _, err := r.Load(name); err != nil {
			continue
		}
		e := r.entries[name]
		if e.Loaded() {
			r.walk(e.Graph)
		}
	}
}

// callNodes lists a graph's call nodes in canonical order, bodies included.
func callNodes(g *ir.Graph) []*ir.Node {
	var out []*ir.Node
	var visit func(nodes []*ir.Node)
	visit = func(nodes []*ir.Node) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if n.Kind == ir.KindCall {
				out = append(out, n)
			}
			if len(n.Body) > 0 {
				visit(n.Body)
			}
		}
	}
	visit(g.Steps)
	return out
}
