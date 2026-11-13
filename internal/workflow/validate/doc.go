// Package validate holds the workflow DSL validators. Rules are plain
// functions over the resolved ir.Graph returning []Diagnostic: no
// framework, no registry, no reflection. A rule never mutates the graph
// and never assumes the graph was built honestly, and an aggregate runs
// every rule in its family so a caller reports all findings at once
// instead of only the first.
//
// Rule codes are the stable contract with authors and with the trace
// surface. This family owns the structural rules:
//
//	WF001  the document does not describe a graph: strict decoding or
//	       resolution failed (unknown fields, unknown enum values,
//	       missing required fields, parse limits, unbound references)
//	WF002  node ids are unique in their scope and every step reference
//	       (needs, branch arms, gate dispositions) names a step that exists
//	WF003  needs is acyclic, and a call node's declared depth is bounded
//	WF004  every node is reachable from the entry set
//	WF005  output bindings have non-empty unique names, and a declared
//	       schema is a schema name or a well-formed JSON Schema object
//
// Later beads join this package with the value and template rules
// (WF010–WF013), the proposal-linearity rules (WF020–WF029), and the
// authority and cost rules (WF030–WF036). Each family gets its own
// aggregate next to Structural and its own rows in this table: the code
// set is small and part of the document contract, so it stays somewhere a
// human can read all of it.
package validate
