package validate

import (
	"errors"
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// Rule codes. The strings are part of the workflow document contract and
// must not change meaning: adding a rule means a new constant here and a
// row in the table in doc.go.
const (
	// CodeWF001 marks a document that never became a graph: the strict
	// decoder or the resolver rejected it, so no node-level rule could
	// run. See Diagnose.
	CodeWF001 = "WF001"
	// CodeWF002 marks an id-scope defect or a step reference that names
	// no step: duplicate ids, an unlabelled top-level step, an unknown
	// needs, branch arm, or gate disposition.
	CodeWF002 = "WF002"
	// CodeWF003 marks a needs cycle or an unbounded call depth.
	CodeWF003 = "WF003"
	// CodeWF004 marks a node the scheduler can never enter, because no
	// path from the entry set reaches it.
	CodeWF004 = "WF004"
	// CodeWF005 marks an output binding: an empty or duplicated name, or
	// a schema that is neither a schema name nor a well-formed JSON
	// Schema object.
	CodeWF005 = "WF005"
)

// Diagnostic is one validation finding. Path anchors it: a resolved node
// path ("steps.build.body[0].needs[1]") for the graph rules, a document
// path ("spec.steps[0].kolect") for a WF001 that came from the decoder.
// Path is empty only when the finding has no location — a document that
// is not valid YAML at all, or a diagnostic that describes the whole
// graph.
type Diagnostic struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// String renders the finding as "WF004 steps.tail: not reachable …".
func (d Diagnostic) String() string {
	if d.Path == "" {
		return d.Code + ": " + d.Message
	}
	return d.Code + " " + d.Path + ": " + d.Message
}

// Diagnose maps a failure raised before validation — the strict decoder in
// spec.Parse, or the resolver in ir.Resolve — onto WF001 diagnostics.
//
// Both stages share the code deliberately. WF001 means "this document does
// not describe a graph for the node-level rules to walk", and neither a
// decode error nor a resolve error leaves a graph behind to attribute a
// rule to: the first bad field stops the walk, so only the first failure is
// ever known. The path and message are carried through verbatim, so an
// author still sees which key was unknown at which location. Later
// families join Validate on top of this seam; none of them should widen
// it.
func Diagnose(err error) []Diagnostic {
	if err == nil {
		return nil
	}
	var derr *spec.DecodeError
	if errors.As(err, &derr) {
		return []Diagnostic{{Code: CodeWF001, Path: derr.Path, Message: derr.Message}}
	}
	var rerr *ir.ResolveError
	if errors.As(err, &rerr) {
		return []Diagnostic{{Code: CodeWF001, Path: rerr.Path, Message: rerr.Message}}
	}
	return []Diagnostic{{Code: CodeWF001, Message: err.Error()}}
}

// diag builds one finding. format is applied only when args are present,
// so a message containing a literal percent sign never needs escaping.
func diag(code, path, format string, args ...any) Diagnostic {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return Diagnostic{Code: code, Path: path, Message: msg}
}

// hasCode reports whether any finding carries code.
func hasCode(diags []Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}
