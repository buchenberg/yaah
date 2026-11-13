// Package validate holds the workflow DSL validators. Rules are plain
// functions over the resolved ir.Graph returning []Diagnostic: no
// framework, no registry, no reflection. A rule never mutates the graph and
// never assumes the graph was built honestly, and an aggregate runs every
// rule in its family so a caller reports all findings at once instead of
// only the first.
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
//	WF005  output bindings have non-empty unique names in their scope, and a
//	       declared schema is a schema name or a well-formed JSON Schema
//	       object
//
// The value and template family owns what a structurally sound graph can
// still get wrong about the values it moves. Rules read the IR's compiled
// templates and selectors, never the source strings, and stay silent about
// any value whose type or shape nobody declared — "shallow but explicit":
// a finding always names a declaration the graph really carries.
//
//	WF010  every compiled reference resolves in scope: a declared param, a
//	       step the node can see, output or collect, an item binding or
//	       index inside the body that declares it
//	WF011  a consumer reads the kind of value it acts on — fanout.over,
//	       settle.proposals, and settle.where need a list, branch.on and the
//	       seed/continue/fork bindings must not read one, json_merge needs
//	       objects, and a declared output type cannot contradict its own
//	       schema
//	WF012  a field a reference reads exists in the shape the producer
//	       declares, whether it is read through an access path or through a
//	       selector's field:
//	WF013  no recursion and no calls: a node cannot read its own output or
//	       the collect of the fan-out that contains it, a compiled name is a
//	       plain identifier rather than an invocation, and a template's
//	       segments and references agree
//	WF014  a payload field belongs to its node kind: cases on a branch,
//	       on_pass on a gate, until on a loop, over on a fan-out, role on a
//	       delegate — a field the resolver drops is a constraint the author
//	       believes is set
//
// WF014 is an extension of the plan catalog, which listed this family as
// WF010–WF013. It is appended to the family rather than folded into WF011
// because "the value is the wrong shape" and "the value was written where no
// shape can hold it" are different fixes, and the codes are the contract
// authors and the trace surface depend on.
//
// Values is the aggregate for that family and Validate runs it after
// Structural. It takes the authoring document alongside the graph for one
// reason: ir.compileSelector does not carry Selector.Field into the IR
// (bead yaah-0u0v.32), so the only place a `where: {field: verdict}` still
// exists is the document. A nil document runs every other check and keeps
// that one silent.
//
// Scope discipline is shared by WF005 and this family, and is stated once
// here because both files enforce halves of it. A name a node binds is
// unique in the scope a reader resolves it in: step ids at the top level,
// earlier siblings inside a body, and nothing visible across that boundary
// in either direction. Output names follow the same rule — a container's
// body is its own namespace, and a body-local name may not shadow a
// top-level one — because a fan-out body rebinds per item and cannot own a
// graph-wide handle.
//
// The authority and cost family treats the workflow file as a permission
// plan: what a run may touch, and how much it may spend. Authority and Cost
// both take an Env of plain host data — role registry, declared worlds,
// network ceiling, configured caps — because the validator must not import
// the runtime; the CLI (bead yaah-0u0v.9) resolves Env from the live
// registry and configuration. A zero field means "not configured", and the
// rule declines that axis rather than inventing a ceiling. Authority judges
// every node, reachable or not — a stranded escalation is still a defect in
// the plan, and WF004 has already said why it will not run — while the
// Explain summary counts only reachable nodes, because a run cannot touch
// what the scheduler cannot enter.
//
//	WF030  every dispatch names a role the host registry knows and may
//	       delegate to; a primary-only persona is refused by name
//	WF031  filesystem grants are world-relative, disjoint within one
//	       dispatch, and inside a world the host declares; an emit
//	       artifact path may not climb out of its world either
//	WF032  a network mode may narrow the host ceiling but never widen
//	       it, and a broker with no configured hosts admits nothing
//	WF033  the worst-case dispatch count — fan-out bound × enclosing
//	       loop bound × call expansion over reachable nodes — fits the
//	       declared budget, and the declared budget fits the host
//	       ceiling; a bound the document does not supply makes the
//	       estimate a floor, flagged as such, rather than a guess
//	WF034  every loop declares max_iterations and every fan-out declares
//	       max_parallel: repetition is bounded in the document, not at
//	       run time
//	WF035  prompts and cost limits stay within the configured caps that
//	       sit above the decode-level floor
//	WF036  no credential is written into the document itself — a marker
//	       tripwire until the vocabulary grows a `secrets:` reference
//
// Explain is the family's summary artifact for the CLI: the authority union
// (every reachable grant at its widest access, every broker host, every
// artifact path, sorted and deduplicated so two runs over one graph agree
// byte for byte) and the worst-case cost.
//
// One more family is planned for this package: the proposal-linearity rules
// (WF020–WF029). It gets its own aggregate next to Structural, Values,
// Authority, and Cost and its own rows in this table: the code set is small
// and part of the document contract, so it stays somewhere a human can read
// all of it.
package validate
