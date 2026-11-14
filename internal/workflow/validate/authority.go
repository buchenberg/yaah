package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Rule codes for the authority and cost family. A workflow file is a
// permission plan, so these rules answer the two questions a plan must
// answer before anything runs: what may this run touch, and how much may
// it spend. As with the structural family, adding a rule means a new
// constant here and a row in the table in doc.go.
const (
	// CodeWF030 marks a dispatch authority the host does not grant: a
	// delegate or judge that names no role, names a role the registry has
	// never heard of, or names a primary-only persona it may not delegate
	// to.
	CodeWF030 = "WF030"
	// CodeWF031 marks a filesystem grant that is not world-relative, that
	// overlaps another grant on the same dispatch, or that names a world
	// the host does not declare — including an emit artifact path that
	// climbs out of its world.
	CodeWF031 = "WF031"
	// CodeWF032 marks a network policy the host cannot honour: a broker
	// with no hosts to broker, or a mode more permissive than the host's
	// configured ceiling.
	CodeWF032 = "WF032"
	// CodeWF033 marks a run whose worst-case dispatch count exceeds the
	// budget it declares, or a declared budget above the host ceiling.
	CodeWF033 = "WF033"
	// CodeWF034 marks an unbounded repetition: a loop without
	// max_iterations or a fan-out without max_parallel.
	CodeWF034 = "WF034"
	// CodeWF035 marks a prompt or a cost limit above the configured cap.
	CodeWF035 = "WF035"
	// CodeWF036 marks a credential written into the document itself
	// instead of reached through a reference.
	CodeWF036 = "WF036"
	// CodeWF037 marks a gate whose condition cannot run: no assert and no
	// judge, both at once, or an assert name/argument set the fixed registry
	// does not accept. See gate.go — the code lives here so the family's
	// vocabulary stays in one place.
	CodeWF037 = "WF037"
)

// Role is one entry of the host role registry as the caller saw it. Plain
// data, because the validator must not import the runtime: rolefile mode
// maps onto these two flags as primary → {false, true}, subagent →
// {true, false}, all → {true, false}, and a name absent from Env.Roles is
// a name the registry does not have.
type Role struct {
	// Delegatable reports that the role may be dispatched as a sub-agent.
	Delegatable bool
	// PrimaryOnly reports that the role is deliberately a primary
	// persona, which is the interesting half of "not delegatable": it
	// exists, so an author will assume it is usable.
	PrimaryOnly bool
}

// Env is the host authority a workflow is checked against. Every field is
// plain data so the rules stay pure and testable and the caller — the CLI
// in bead yaah-0u0v.9 — resolves roles, worlds, and caps from the live
// registry and configuration. One authority model, in the runtime; this is
// a read of it.
//
// Zero means "not configured". Most rules then decline to check that axis
// rather than guess, because a guessed ceiling would invent policy the host
// never set. The exceptions are documented at their fields: MaxPromptBytes
// falls back to the decode-level cap, and an unset role registry still
// refuses dispatches that name no role.
type Env struct {
	// Roles indexes the delegatable surface. An empty or nil map skips
	// WF030's name checks: a caller that has not loaded the registry must
	// not get every workflow flagged for roles it cannot resolve.
	Roles map[string]Role

	// Worlds lists the worlds the host declares. With none listed, WF031
	// checks no world name and a document's `world:` goes unverified.
	Worlds []string
	// DefaultWorld names the world a dispatch without `world:` lands in.
	// It is reporting data for the authority union; empty means the host
	// default is unnamed and the union reports it as "".
	DefaultWorld string

	// HostNetwork is the configured ceiling from agents.subagent.network.
	// A document may narrow it per dispatch but never widen it, which is
	// the whole of WF032's consistency check. Empty skips the check.
	HostNetwork ir.NetworkMode
	// BrokerHosts is agents.subagent.network_hosts: the exact host names
	// the broker admits. Meaningful only with a broker mode; empty means
	// the broker admits nothing.
	BrokerHosts []string

	// MaxDispatches is the host ceiling on one run's dispatch count,
	// applied when the document declares no budgets.max_dispatches and
	// used as the widening bound when it does. Zero means no ceiling.
	MaxDispatches int
	// MaxFanoutItems bounds a fan-out whose item count the document does
	// not bound — an `over:` a selector resolves at run time. Zero keeps
	// the estimate honest instead of inventing a number: the total becomes
	// a floor, flagged in CostEstimate, and WF033 only fires when even
	// that floor is over budget.
	MaxFanoutItems int
	// MaxCallDispatches bounds the work a `call:` node reaches into, for
	// the same reason: this graph cannot see the called workflow. Zero
	// contributes only the declared depth and marks the total a floor.
	MaxCallDispatches int

	// MaxPromptBytes caps one prompt as written. Decode already refuses a
	// prompt over spec.MaxPromptBytes, so this is the host's tighter layer
	// above it; zero takes spec.MaxPromptBytes.
	MaxPromptBytes int
	// MaxTurnsPerDispatch is the host ceiling on a node's max_turns and,
	// when set, the per-dispatch turn budget for the cost estimate. Zero
	// checks no max_turns and reports no turn bound.
	MaxTurnsPerDispatch int
	// MaxTokens is the host ceiling on budgets.max_tokens. Zero skips it.
	MaxTokens int
	// SecondsPerDispatch prices a dispatch that declares no
	// timeout_seconds. Zero reports no wall-clock bound.
	SecondsPerDispatch int
}

// Authority runs the authority half of the family — WF030, WF031, WF032,
// WF036, WF037 — over a resolved graph and returns every finding, nodes in
// document order and rules in code order, so the slice is deterministic
// without the caller sorting anything.
//
// These rules look at every node, reachable or not, while Explain's union
// counts only reachable ones. The split is deliberate: a role escalation
// stranded behind a bad `needs:` is still a defect in the plan, and WF004
// has already said why it will not run; the union, by contrast, answers
// "what could this run touch" and must not credit authority no scheduler
// can enter.
func Authority(g *ir.Graph, env Env) []Diagnostic {
	if g == nil {
		return nil
	}
	var out []Diagnostic
	out = append(out, dispatchRoles(g, env)...)
	out = append(out, filesystemGrants(g, env)...)
	out = append(out, networkPolicy(g, env)...)
	out = append(out, inlineSecrets(g)...)
	out = append(out, gateConditions(g)...)
	return out
}

// dispatchRoles checks WF030. Two kinds dispatch a role: a delegate, and
// the judge payload of a gate or select, which runs the same
// verdict-producing dispatch and so carries the same authority. Reaching a
// primary-only persona from a workflow would hand a run the orchestrator's
// own standing — the escalation this rule exists to refuse.
func dispatchRoles(g *ir.Graph, env Env) []Diagnostic {
	order, _ := graphNodes(g)
	resolved := len(env.Roles) > 0
	var out []Diagnostic
	for _, n := range order {
		if n.Kind == ir.KindDelegate {
			if msg := roleProblem(n.Role, env, resolved); msg != "" {
				out = append(out, diag(CodeWF030, n.Path+".role", "%s", msg))
			}
		}
		if n.Judge != nil {
			if msg := roleProblem(n.Judge.Role, env, resolved); msg != "" {
				out = append(out, diag(CodeWF030, n.Path+".judge.role", "%s", msg))
			}
		}
	}
	return out
}

// roleProblem reports why a dispatch's role name is not usable, or "" when
// it is. Naming nothing is provable without a registry and is refused in
// both cases: an unresolvable role is a hole in the permission plan, not
// merely an unknown one.
func roleProblem(name string, env Env, resolved bool) string {
	if strings.TrimSpace(name) == "" {
		return "dispatch must name a role"
	}
	if !resolved {
		return ""
	}
	r, ok := env.Roles[name]
	switch {
	case !ok:
		return "role " + quote(name) + " is not in the host role registry"
	case r.PrimaryOnly:
		return "role " + quote(name) + " is a primary persona and may not be delegated to"
	case !r.Delegatable:
		return "role " + quote(name) + " is not delegatable"
	}
	return ""
}

// filesystemGrants checks WF031: grants are world-relative, disjoint
// within one dispatch, and inside a world the host declares.
//
// World-relative is about the path shape, not the string. A grant is a
// subpath of one world root, so an absolute path, a drive or UNC prefix, or
// a `..` that climbs past the root asks for authority the workflow was
// never given. Disjointness is checked per dispatch: two grants on one node
// that overlap make the wider one silently win, and an author who wrote
// `src` readwrite plus `src/main.go` readonly meant the narrower pair.
// Cross-node disjointness is refused on purpose — fan-out items share the
// container's folded grants by construction, so such a rule would reject
// the shape the vocabulary intends.
//
// An emit artifact path is checked with the same helper. It is the
// workflow's own write rather than a delegated grant, but it lands in the
// same world, and a plan that writes outside its root has escaped it just as
// surely as a grant that does.
func filesystemGrants(g *ir.Graph, env Env) []Diagnostic {
	order, _ := graphNodes(g)
	declared := map[string]bool{}
	for _, w := range env.Worlds {
		declared[w] = true
	}
	var out []Diagnostic
	for _, n := range order {
		if n.World != "" && len(declared) > 0 && !declared[n.World] {
			out = append(out, diag(CodeWF031, n.Path+".world",
				"world %s is not one the host declares", quote(n.World)))
		}
		for i, gr := range n.Grants {
			path := fmt.Sprintf("%s.grants[%d]", n.Path, i)
			if !withinWorld(gr.Path) {
				out = append(out, diag(CodeWF031, path,
					"grant path %s is not world-relative", quote(gr.Path)))
			}
			if gr.Access != ir.AccessReadOnly && gr.Access != ir.AccessReadWrite {
				out = append(out, diag(CodeWF031, path+".access",
					"grant access %s is not recognised", quote(string(gr.Access))))
			}
		}
		out = append(out, overlappingGrants(n)...)
		if n.Kind == ir.KindEmit && n.ArtifactPath != "" && !withinWorld(n.ArtifactPath) {
			out = append(out, diag(CodeWF031, n.Path+".path",
				"artifact path %s is not world-relative", quote(n.ArtifactPath)))
		}
	}
	return out
}

// overlappingGrants reports each overlapping pair inside one node's grant
// list, in list order, anchored on the list rather than on the later entry,
// because the fix is to merge or narrow two paths rather than one.
func overlappingGrants(n *ir.Node) []Diagnostic {
	var out []Diagnostic
	for i := 0; i < len(n.Grants); i++ {
		a := normaliseGrantPath(n.Grants[i].Path)
		if a == "" {
			continue // already refused: nothing unenforceable overlaps it
		}
		for j := i + 1; j < len(n.Grants); j++ {
			b := normaliseGrantPath(n.Grants[j].Path)
			if b == "" || !pathsOverlap(a, b) {
				continue
			}
			out = append(out, diag(CodeWF031, n.Path+".grants",
				"grants[%d] %s and grants[%d] %s overlap; the wider access silently wins",
				i, quote(a), j, quote(b)))
		}
	}
	return out
}

// networkPolicy checks WF032 against the host's own network vocabulary.
//
// The workflow surface states a mode per dispatch (`network`) and has no
// hosts field of its own: the allowlist lives in host configuration
// (agents.subagent.network_hosts) and is what a broker admits. So the two
// findings available from a graph are exactly the two the rule names — a
// broker with nothing to broker, and a mode wider than the ceiling the host
// configured. Docs call the same pair "empty = admit nothing" and "setting
// hosts with another mode is a validation error"; the second half of that
// sentence is the config validator's, not ours.
func networkPolicy(g *ir.Graph, env Env) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	for _, n := range order {
		if n.Kind != ir.KindDelegate || n.Network == "" {
			continue
		}
		path := n.Path + ".network"
		if n.Network == ir.NetworkBroker && len(env.BrokerHosts) == 0 {
			out = append(out, diag(CodeWF032, path,
				"broker mode has no hosts to broker: the configured allowlist is empty, so this dispatch could reach nothing"))
			continue
		}
		if env.HostNetwork != "" && rankNetwork(n.Network) > rankNetwork(env.HostNetwork) {
			out = append(out, diag(CodeWF032, path,
				"network %s is wider than the host ceiling %s; a workflow may narrow it but never widen it",
				quote(string(n.Network)), quote(string(env.HostNetwork))))
		}
	}
	return out
}

// rankNetwork orders the modes by how much they admit: nothing, then a
// broker that admits only its allowlist, then anything. An unknown mode
// ranks below all three, so it can never be reported as a widening.
func rankNetwork(m ir.NetworkMode) int {
	switch m {
	case ir.NetworkDenyAll:
		return 0
	case ir.NetworkBroker:
		return 1
	case ir.NetworkAllowAll:
		return 2
	}
	return -1
}

// inlineSecrets checks WF036 over the strings an author writes: prompt and
// emit text, constant call parameters, and assert arguments.
//
// The v1 vocabulary has no `secrets:` reference mechanism and nothing in
// the graph marks a string as sourced outside the document, so this rule is
// a marker scan and nothing more. It fires on the shapes that are
// credentials in every tool that has leaked one — a PEM private key header,
// an Authorization line, an AWS access key id, a GitHub, Slack, or
// OpenAI-style token — and stays silent on prose that merely discusses a
// token and on references like `{{ params.api_key }}`, which are the
// sanctioned way to reach a secret. An unusual or misspelled credential
// passes: this is a tripwire against the common accident, not a guarantee,
// and the missing `secrets:` vocabulary is what would turn it into one.
func inlineSecrets(g *ir.Graph) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	for _, n := range order {
		seen := map[string]bool{}
		report := func(field, name string) {
			key := field + "\x00" + name
			if seen[key] {
				return
			}
			seen[key] = true
			out = append(out, diag(CodeWF036, field,
				"%s written into the document: pass it as a parameter or a secret reference instead", name))
		}
		scan := func(field, text string) {
			for _, m := range secretMarkers {
				if m.match(text) {
					report(field, m.name)
				}
			}
		}
		if n.Prompt != nil {
			scan(n.Path+".prompt", templateText(n.Prompt))
		}
		if n.Value != nil {
			scan(n.Path+".value", templateText(n.Value))
		}
		if n.Judge != nil && n.Judge.Prompt != nil {
			scan(n.Path+".judge.prompt", templateText(n.Judge.Prompt))
		}
		for _, k := range sortedParamKeys(n.CallParams) {
			pv := n.CallParams[k]
			field := fmt.Sprintf("%s.params.%s", n.Path, k)
			if pv.Template != nil {
				scan(field, templateText(pv.Template))
			}
			for _, s := range literalStrings(pv.Const) {
				scan(field, s)
			}
		}
		if n.Assert != nil {
			for _, k := range sortedAnyKeys(n.Assert.Args) {
				field := fmt.Sprintf("%s.assert.args.%s", n.Path, k)
				for _, s := range literalStrings(n.Assert.Args[k]) {
					scan(field, s)
				}
			}
		}
	}
	return out
}

// secretMarker is one credential shape and how to find it. Plain matchers
// rather than one regexp: the set is small, and a name per shape is what
// makes the diagnostic actionable.
type secretMarker struct {
	name  string
	match func(string) bool
}

var secretMarkers = []secretMarker{
	{"private key block", func(s string) bool { return strings.Contains(s, "-----BEGIN") }},
	{"bearer authorization line", func(s string) bool {
		return strings.Contains(strings.ToLower(s), "authorization:")
	}},
	{"AWS access key id", wordPrefix("akia")},
	{"GitHub token", wordPrefix("ghp_", "gho_", "ghu_", "ghs_", "github_pat_")},
	{"Slack token", wordPrefix("xoxb-", "xoxp-", "xoxa-")},
	{"API key of sk- form", wordPrefix("sk-")},
}

// wordPrefix finds a token-shaped prefix at a word start, so "ask-idx" does
// not read as an sk- key and "shake" does not read as a Slack token.
func wordPrefix(prefixes ...string) func(string) bool {
	return func(s string) bool {
		lower := strings.ToLower(s)
		for _, p := range prefixes {
			for i := 0; i < len(lower); {
				j := strings.Index(lower[i:], p)
				if j < 0 {
					break
				}
				at := i + j
				if at == 0 || !isWordByte(lower[at-1]) {
					return true
				}
				i = at + 1
			}
		}
		return false
	}
}

func isWordByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// literalStrings returns every string under a free-form author value, in a
// deterministic order, bounded so a hostile shape cannot make the walk
// deep: the decoder carries parameters and assert arguments as any with no
// nesting limit of its own.
func literalStrings(v any) []string {
	var out []string
	collectStrings(v, 0, &out)
	return out
}

const maxLiteralDepth = 8

func collectStrings(v any, depth int, out *[]string) {
	if depth > maxLiteralDepth {
		return
	}
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, e := range t {
			collectStrings(e, depth+1, out)
		}
	case map[string]any:
		for _, k := range sortedAnyKeys(t) {
			collectStrings(t[k], depth+1, out)
		}
	}
}

// AuthorityUnion is the filesystem and network authority the run could
// reach: what Explain reports and the explain CLI renders. Every list is
// sorted and deduplicated, so two runs over one graph agree byte for byte
// without the caller sorting anything.
type AuthorityUnion struct {
	// Grants is the merged filesystem set. Two nodes granting one path
	// appear once, at the wider access, because that is what any of them
	// may do.
	Grants []PathGrant
	// Hosts names every broker host the run may reach, taken from the
	// configured allowlist, since the document states only the mode.
	Hosts []string
	// OpenNetwork reports that some reachable dispatch may address any
	// host, so Hosts cannot bound the run.
	OpenNetwork bool
	// NetworkModes lists the effective modes in use, narrowest first. A
	// dispatch that states nothing inherits HostNetwork, which is what
	// the runtime will apply.
	NetworkModes []ir.NetworkMode
	// Artifacts lists the emit paths a reachable node may write: the
	// workflow's own writes rather than delegated grants.
	Artifacts []string
	// Unreachable counts the nodes left out of the union, so a report can
	// say the plan is stranded instead of implying it is small.
	Unreachable int
}

// PathGrant is one world-relative path and the widest access any reachable
// dispatch may use there.
type PathGrant struct {
	World  string
	Path   string
	Access ir.GrantAccess
}

// authorityUnion merges the authority of every reachable node. A grant the
// rule family already refused is skipped: it grants nothing enforceable,
// and repeating it here would let the union disagree with the diagnostics
// about what the plan actually asks for.
func authorityUnion(g *ir.Graph, env Env) AuthorityUnion {
	order, _ := graphNodes(g)
	reach := authorityReachable(g)
	u := AuthorityUnion{}
	merged := map[string]PathGrant{}
	modes := map[ir.NetworkMode]bool{}
	seenArtifact := map[string]bool{}

	for _, n := range order {
		if !reach[n] {
			u.Unreachable++
			continue
		}
		world := n.World
		if world == "" {
			world = env.DefaultWorld
		}
		for _, gr := range n.Grants {
			path := normaliseGrantPath(gr.Path)
			if path == "" {
				continue
			}
			key := world + "\x00" + path
			if prev, had := merged[key]; !had || accessRank(gr.Access) > accessRank(prev.Access) {
				merged[key] = PathGrant{World: world, Path: path, Access: gr.Access}
			}
		}
		if n.Kind == ir.KindEmit && n.ArtifactPath != "" {
			if path := normaliseGrantPath(n.ArtifactPath); path != "" && !seenArtifact[world+"\x00"+path] {
				seenArtifact[world+"\x00"+path] = true
				u.Artifacts = append(u.Artifacts, path)
			}
		}
		if n.Kind != ir.KindDelegate {
			continue
		}
		mode := n.Network
		if mode == "" {
			mode = env.HostNetwork
		}
		switch mode {
		case ir.NetworkDenyAll, ir.NetworkBroker, ir.NetworkAllowAll:
			modes[mode] = true
			if mode == ir.NetworkAllowAll {
				u.OpenNetwork = true
			}
		}
	}

	for _, gr := range merged {
		u.Grants = append(u.Grants, gr)
	}
	sort.Slice(u.Grants, func(i, j int) bool {
		if u.Grants[i].World != u.Grants[j].World {
			return u.Grants[i].World < u.Grants[j].World
		}
		return u.Grants[i].Path < u.Grants[j].Path
	})
	for m := range modes {
		u.NetworkModes = append(u.NetworkModes, m)
	}
	sort.Slice(u.NetworkModes, func(i, j int) bool {
		return rankNetwork(u.NetworkModes[i]) < rankNetwork(u.NetworkModes[j])
	})
	if modes[ir.NetworkBroker] && len(u.Hosts) == 0 {
		u.Hosts = append(u.Hosts, env.BrokerHosts...)
		sort.Strings(u.Hosts)
	}
	sort.Strings(u.Artifacts)
	return u
}

// accessRank orders the two access levels so a merge keeps the wider one.
func accessRank(a ir.GrantAccess) int {
	if a == ir.AccessReadWrite {
		return 1
	}
	return 0
}

// withinWorld reports whether a grant or artifact path sits inside a world
// root once normalised.
func withinWorld(p string) bool { return normaliseGrantPath(p) != "" }

// normaliseGrantPath resolves a world-relative path against its root and
// returns its clean slash-joined form, or "" when the path is not inside
// the world: empty, absolute, drive- or UNC-prefixed, or climbing past the
// root with `..`. A root that names no subpath at all — ".", "./" — is also
// refused, because "the whole world" is a grant the author should have to
// write on purpose rather than by accident.
//
// The split treats both separators as separators and never goes through
// filepath, which cleans to backslashes on Windows: one document must not
// validate differently per host.
func normaliseGrantPath(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return ""
	}
	if len(p) >= 2 && p[1] == ':' && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') {
		return ""
	}
	var segs []string
	for _, s := range strings.FieldsFunc(strings.ReplaceAll(p, `\`, "/"), func(r rune) bool { return r == '/' }) {
		switch s {
		case ".":
			continue
		case "..":
			if len(segs) == 0 {
				return ""
			}
			segs = segs[:len(segs)-1]
		default:
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return ""
	}
	return strings.Join(segs, "/")
}

// pathsOverlap reports whether one normalised path contains or is contained
// by another at a segment boundary, so cmd does not overlap cmdr.
func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// authorityReachable is the reachability fixed point behind WF004,
// recomputed here because the union and the cost estimate must credit only
// the nodes a scheduler may enter. It follows the same join-all rule: a
// node is enterable when its container is and every step it needs is itself
// enterable. Duplicated rather than shared because each rule file in this
// package owns its traversal, and a second return value in reach.go would
// make one rule depend on another's data.
func authorityReachable(g *ir.Graph) map[*ir.Node]bool {
	order, parent := graphNodes(g)
	byID := stepsByID(g)
	reach := make(map[*ir.Node]bool, len(order))
	for {
		progressed := false
		for _, n := range order {
			if reach[n] {
				continue
			}
			if up := parent[n]; up != nil && !reach[up] {
				continue
			}
			enterable := true
			for _, ref := range n.Needs {
				if dep, ok := byID[ref]; !ok || !reach[dep] {
					enterable = false
					break
				}
			}
			if enterable {
				reach[n] = true
				progressed = true
			}
		}
		if !progressed {
			return reach
		}
	}
}

// templateText returns the author-written text of a compiled template. Raw
// is the source form the decode-level caps measure, so the configured cap
// compares against it; a hand-built graph that set only Segments falls back
// to the literal text.
func templateText(t *ir.Template) string {
	if t == nil {
		return ""
	}
	if t.Raw != "" {
		return t.Raw
	}
	var b strings.Builder
	for _, s := range t.Segments {
		if s.Ref == nil {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

func quote(s string) string { return "\"" + s + "\"" }

func sortedParamKeys(m map[string]ir.ParamValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedAnyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
