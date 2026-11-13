package yaah

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
	"github.com/buchenberg/yaah/internal/workflow/validate"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// workflow_analysis.go is the loading and calling half of `yaah workflow
// validate` / `yaah workflow explain` (bead yaah-0u0v.9). It owns exactly
// three things the validator packages do not: reading one file from disk,
// turning live host configuration into validate.Env, and splitting the
// validators' findings into the two severities the CLI reports. Every
// judgement about a document comes from internal/workflow/... — nothing
// here inspects a graph to decide whether it is legal.
//
// Neither command dispatches an agent or writes run state: no session, no
// store, no worktree, no proposal. The only host reads are the config file,
// the role search paths, and the jail/container availability probe that
// worldinfo already exposes for doctor and the TUI.

// warningCodes lists the rules whose finding depends on this host's
// configuration rather than on the document. The role registry, the network
// ceiling, the dispatch budget, and the configured caps are all things a
// workflow cannot control from the page it is written on, so a finding on
// those axes means "this plan does not fit the machine you are standing
// on" — a warning by default, and a failure under --strict.
//
// Every other code describes the document itself and is an error. Two of
// the four codes here have document-intrinsic halves and they are
// deliberately still warnings: WF030 refuses an empty `role:` whatever the
// registry holds (an author can read that finding as a warning and fix it
// without a registry, and --strict is the place that demands it), and
// WF033's negative `max_dispatches` is priced against a bound the host set.
// Splitting one rule across two severities by message text would make the
// scheme depend on prose; the code is the contract, so the code decides.
var warningCodes = map[string]bool{
	validate.CodeWF030: true,
	validate.CodeWF032: true,
	validate.CodeWF033: true,
	validate.CodeWF035: true,
}

// workflowAnalysis is one spec document run through every validator family.
// The slices are always non-nil so the JSON report renders `[]` rather than
// `null` for a clean document.
type workflowAnalysis struct {
	path   string
	name   string
	strict bool

	graph *ir.Graph
	env   validate.Env

	// params is every declared parameter with the effective value the CLI
	// resolved for it, in name order.
	params []workflowParam

	errors   []validate.Diagnostic
	warnings []validate.Diagnostic

	// authority, cost, and proof are the validator families' own summary
	// artifacts, which is all explain renders. Explain returns the
	// authority and cost diagnostics alongside them, so those two families
	// are never run twice.
	authority validate.AuthorityUnion
	cost      validate.CostEstimate
	proof     *validate.SettlementProof
}

// failed reports whether the run must exit non-zero: any error-severity
// finding, or any finding at all under --strict.
func (a *workflowAnalysis) failed() bool {
	return len(a.errors) > 0 || (a.strict && len(a.warnings) > 0)
}

// status is the report's one-word verdict, shared by the text and JSON
// shapes. "unresolved" is distinct from "invalid" because a document the
// decoder or the resolver refused never became a graph: the node-level
// families could not run, and a reader needs to know that the absence of
// other findings proves nothing.
func (a *workflowAnalysis) status() string {
	switch {
	case a.graph == nil:
		return "unresolved"
	case a.failed():
		return "invalid"
	default:
		return "valid"
	}
}

// analyzeWorkflow loads path, folds the --param overrides into the
// authoring document, resolves the graph, and runs every family: Structural,
// Values, Linearity (the three validate.Validate itself runs), then
// Authority and Cost through Explain — which also yields the two summaries
// explain reports — and finally Prove for the settlement dispositions.
//
// A returned error means the analysis could not run at all: the file is
// missing or unreadable, oversized, or a --param was malformed. A document
// that decodes badly is NOT an error: it is one WF001 finding, reported
// like every other finding.
func analyzeWorkflow(path string, rawParams []string, env validate.Env, strict bool) (*workflowAnalysis, error) {
	a := &workflowAnalysis{
		path:     path,
		strict:   strict,
		env:      env,
		errors:   []validate.Diagnostic{},
		warnings: []validate.Diagnostic{},
	}
	data, err := readWorkflowSpec(path)
	if err != nil {
		return nil, err
	}
	doc, derr := spec.Parse(data)
	if derr != nil {
		a.errors = append(a.errors, validate.Diagnose(derr)...)
		return a, nil
	}
	a.name = doc.Metadata.Name
	params, perr := applyWorkflowParams(doc, rawParams)
	if perr != nil {
		return nil, perr
	}
	a.params = params

	g, rerr := ir.Resolve(doc)
	if rerr != nil {
		a.errors = append(a.errors, validate.Diagnose(rerr)...)
		return a, nil
	}
	a.graph = g

	findings := validate.Structural(g)
	findings = append(findings, validate.Values(doc, g)...)
	findings = append(findings, validate.Linearity(g)...)
	ex := validate.Explain(g, env)
	findings = append(findings, ex.Diagnostics...)
	a.authority, a.cost = ex.Authority, ex.Cost
	a.proof = validate.Prove(g)

	sortWorkflowFindings(findings)
	for _, d := range findings {
		if warningCodes[d.Code] {
			a.warnings = append(a.warnings, d)
		} else {
			a.errors = append(a.errors, d)
		}
	}
	return a, nil
}

// sortWorkflowFindings orders findings by code, then path, then message.
// Each family already emits deterministically in catalog and document order,
// but five families joined in family order would leave the authority/cost
// block after linearity and WF033-WF035 after WF036. Sorting on the visible
// triple keeps the sequence stable for a given graph and puts every code
// contiguous, which is what a diffing reader wants.
func sortWorkflowFindings(diags []validate.Diagnostic) {
	sort.SliceStable(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
}

// readWorkflowSpec returns the document bytes, refusing an oversized file
// before reading it: spec.Parse caps the document it is handed, but a CLI
// must not slurp an arbitrary file to find out.
func readWorkflowSpec(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow spec: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a workflow spec", path)
	}
	if info.Size() > spec.MaxSpecBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the workflow decoder's %d-byte limit",
			path, info.Size(), spec.MaxSpecBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow spec: %w", err)
	}
	return data, nil
}

// ----------------------------------------------------------------- params

// workflowParam is one declared parameter and the value the CLI resolved
// for it. Source is "param" (a --param override), "default" (the document's
// own default), or "unset".
type workflowParam struct {
	Name     string
	Type     string
	Required bool
	Source   string
	Value    any
}

// applyWorkflowParams folds repeatable --param key=value settings into the
// authoring document's parameter declarations, in the only way the existing
// pipeline expects: ir.Resolve reads doc.Spec.Params and copies each entry's
// Default into the graph's Param table, so a supplied value becomes the
// document's effective value before resolution and every family — including
// the value rules that check a param's declared type against a schema —
// sees the same concrete document.
//
// A key the document does not declare is refused rather than ignored:
// accepting --param target=main and doing nothing with it is a lie about
// the interface. The returned list covers every declared parameter, in name
// order, whether or not it was overridden.
func applyWorkflowParams(doc *spec.Document, raw []string) ([]workflowParam, error) {
	supplied := map[string]bool{}
	for _, item := range raw {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf(`--param %q: expected key=value`, item)
		}
		key = strings.TrimSpace(key)
		ps, declared := doc.Spec.Params[key]
		if !declared {
			return nil, fmt.Errorf(`--param %s=%s: the spec declares no parameter %q (declared: %s)`,
				key, value, key, strings.Join(declaredParamNames(doc), ", "))
		}
		v, cerr := coerceParam(ps.Type, value)
		if cerr != nil {
			return nil, fmt.Errorf("--param %s: %w", key, cerr)
		}
		ps.Default = v
		doc.Spec.Params[key] = ps
		supplied[key] = true
	}

	names := declaredParamNames(doc)
	out := make([]workflowParam, 0, len(names))
	for _, name := range names {
		ps := doc.Spec.Params[name]
		p := workflowParam{Name: name, Type: string(ps.Type), Required: ps.Required, Value: ps.Default}
		switch {
		case supplied[name]:
			p.Source = "param"
		case ps.Default != nil:
			p.Source = "default"
		default:
			p.Source = "unset"
		}
		out = append(out, p)
	}
	return out, nil
}

func declaredParamNames(doc *spec.Document) []string {
	names := make([]string, 0, len(doc.Spec.Params))
	for name := range doc.Spec.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// coerceParam turns a flag string into the declared parameter type. list
// and map take JSON, because "a,b" cannot say whether it meant a nested
// element, and the document's own defaults for those types are JSON too.
func coerceParam(t spec.ParamType, raw string) (any, error) {
	switch t {
	case spec.ParamString:
		return raw, nil
	case spec.ParamInteger:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("wants an integer, got %q", raw)
		}
		return n, nil
	case spec.ParamNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("wants a number, got %q", raw)
		}
		return f, nil
	case spec.ParamBoolean:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("wants a boolean (true/false), got %q", raw)
		}
		return b, nil
	case spec.ParamList:
		var v []any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("wants a JSON list, got %q", raw)
		}
		return v, nil
	case spec.ParamMap:
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("wants a JSON object, got %q", raw)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("has no usable parameter type (declared %q)", string(t))
	}
}

// -------------------------------------------------------------------- env

// workflowEnv resolves the host authority validate.Env from live
// configuration and the live role registry, which is the seam the validator
// package documents for this CLI: "the caller resolves roles, worlds, and
// caps from the live registry and configuration".
//
// Axes the configuration has no ceiling for stay zero. A zero field means
// "not configured" and the rule declines that axis rather than inventing a
// bound the host never set, so an unset field is an honest gap in the
// report and not a silent pass.
func workflowEnv() validate.Env {
	cfg, _ := config.Load()
	env := validate.Env{
		Roles:  workflowRoles(),
		Worlds: workflowWorlds(cfg),
	}
	if cfg != nil {
		env.HostNetwork = ir.NetworkMode(worldinfo.Network(cfg))
		env.BrokerHosts = cfg.Agent.SubAgent.NetworkHosts
		// The host's per-dispatch price: the configured default timeout is
		// what a dispatch without timeout_seconds gets, and
		// default_max_turns is the ceiling a node's max_turns may not widen.
		env.SecondsPerDispatch = cfg.Agent.SubAgent.DefaultTimeout
		env.MaxTurnsPerDispatch = cfg.Agent.SubAgent.DefaultMaxToolTurns
	}
	// The world a dispatch without `world:` lands in is the resolution the
	// runtime ladder reaches for the proposal lane — not the configured
	// request, which is usually the "auto" selector rather than a world.
	if res := worldinfo.Resolve(cfg, false); res.ProposalErr == nil {
		env.DefaultWorld = string(res.Proposal.Resolved)
	}
	return env
}

// workflowRoles indexes the delegatable surface from built-in role files
// plus the role search paths for the current directory. It builds a local
// registry instead of reading the process-wide one, so validating a spec
// never mutates the global the running sessions share, and a registry that
// was never initialised still answers.
//
// The mode mapping is the one validate/doc.go states: primary →
// {not delegatable, primary-only}, subagent and all → {delegatable}.
func workflowRoles() map[string]validate.Role {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	reg := subagent.NewRoleRegistry()
	if files := runner.BuiltinRoleFiles(); files != nil {
		_ = reg.LoadBytes(files)
	}
	for _, dir := range runner.RoleSearchPaths(cwd) {
		_ = reg.LoadDir(dir)
	}
	out := make(map[string]validate.Role, len(reg.List()))
	for name, def := range reg.List() {
		out[string(name)] = validate.Role{
			Delegatable: def.Mode != "primary",
			PrimaryOnly: def.Mode == "primary",
		}
	}
	return out
}

// workflowWorlds lists the worlds this host declares for a dispatch, taken
// from the same availability record doctor and the TUI read. `shared` is
// always listed: it is a resolution the shared lane legitimately reaches.
func workflowWorlds(cfg *config.Config) []string {
	avail := worldinfo.Availability(cfg, false)
	worlds := []string{string(subagent.WorldShared)}
	if avail.Worktree {
		worlds = append(worlds, string(subagent.WorldWorktree))
	}
	if avail.Container {
		worlds = append(worlds, string(subagent.WorldContainer))
	}
	sort.Strings(worlds)
	return worlds
}
