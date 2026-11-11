package tools

// capability.go declares, for every built-in tool, what it does to the world
// and how it reaches the approval gate. It is the single place a reviewer can
// answer "can this tool mutate/execute/network, and is that gated?" — and
// capability_test.go fails when a tool is added without an entry, when a
// mutating or executing tool has no danger policy, or when a policy claims a
// classifier the tool does not implement.
//
// The name-keyed policy maps elsewhere (permissions.MutatingFileTools,
// agent.readOnlyToolNames, agent.alwaysDangerous) stay as they are: they encode
// different questions (inheritance ceilings, the inspection surface, platform
// independence) and consolidating them has its own blast radius (memory
// tool-gating-consistency). Tests assert they agree with this table instead.

// Effect is what a tool does. Read is the zero bit: a tool that only inspects
// the world declares no mutate/exec/network effect.
type Effect uint8

const (
	// EffectRead inspects files, memory, or process state.
	EffectRead Effect = 1 << iota
	// EffectMutate changes the workspace, harness configuration, or durable
	// session state (memory, todo, proposals).
	EffectMutate
	// EffectExec runs a process or spawns dispatch work.
	EffectExec
	// EffectNetwork reaches the network.
	EffectNetwork
)

// DangerPolicy is how a tool call reaches the approval gate.
type DangerPolicy uint8

const (
	// DangerPolicyNone is the zero value: nothing to gate.
	DangerPolicyNone DangerPolicy = iota
	// DangerPolicyClassifier means the tool implements tools.DangerPolicyClassifier and
	// decides per call (action- or argument-aware).
	DangerPolicyClassifier
	// DangerPolicyAlways means the call is gated even when the tool is absent from
	// the platform's registry (the shell tools exist on every platform in the
	// approval resolver's eyes).
	DangerPolicyAlways
	// DangerPolicySafe means a mutating/executing tool is deliberately ungated.
	// Reason must explain why.
	DangerPolicySafe
)

// Policy is one tool's declared capability and danger policy.
type Policy struct {
	Effects Effect
	Danger  DangerPolicy
	// Inspect marks a tool that is part of the read-only (plan-mode)
	// inspection surface. It is separate from Effects because git and plan
	// both inspect and mutate, and plan mode admits them for their
	// inspection/artifact actions.
	Inspect bool
	// Reason is required for DangerPolicySafe: the deliberate exception.
	Reason string
}

var toolPolicies = map[string]Policy{
	// Filesystem reads.
	"read":       {Effects: EffectRead, Inspect: true},
	"ls":         {Effects: EffectRead, Inspect: true},
	"file_info":  {Effects: EffectRead, Inspect: true},
	"tail":       {Effects: EffectRead, Danger: DangerPolicyClassifier},
	"checksum":   {Effects: EffectRead, Danger: DangerPolicyClassifier},
	"grep":       {Effects: EffectRead, Inspect: true},
	"glob":       {Effects: EffectRead, Inspect: true},
	"find":       {Effects: EffectRead, Danger: DangerPolicyClassifier},
	"json_query": {Effects: EffectRead, Danger: DangerPolicyClassifier},
	"calculate":  {Effects: EffectRead, Inspect: true},
	"go_outline": {Effects: EffectRead, Inspect: true},
	"diff":       {Effects: EffectRead, Inspect: true},
	"question":   {Effects: EffectRead, Inspect: true},

	// Filesystem and workspace mutation.
	"write":       {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"append":      {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"edit":        {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"patch":       {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"delete":      {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"mkdir":       {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"rmdir":       {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"move":        {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"move_tree":   {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"copy":        {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"copy_tree":   {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"replace":     {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"bulk_files":  {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"apply_out":   {Effects: EffectMutate, Danger: DangerPolicyClassifier},
	"go_refactor": {Effects: EffectRead | EffectMutate | EffectExec, Danger: DangerPolicyClassifier},

	// Harness configuration and plans.
	"role":  {Effects: EffectRead | EffectMutate, Danger: DangerPolicyClassifier},
	"skill": {Effects: EffectRead | EffectMutate, Danger: DangerPolicyClassifier},
	"plan":  {Effects: EffectMutate, Danger: DangerPolicyClassifier, Inspect: true},

	// Durable session state: deliberately ungated, but not silent.
	"memory_add":     {Effects: EffectMutate, Danger: DangerPolicySafe, Reason: "writes the session's memory database, not the workspace"},
	"memory_update":  {Effects: EffectMutate, Danger: DangerPolicySafe, Reason: "writes the session's memory database, not the workspace"},
	"memory_delete":  {Effects: EffectMutate, Danger: DangerPolicySafe, Reason: "writes the session's memory database, not the workspace"},
	"memory_promote": {Effects: EffectMutate, Danger: DangerPolicySafe, Reason: "writes the session's memory database, not the workspace"},
	"todowrite":      {Effects: EffectMutate, Danger: DangerPolicySafe, Reason: "session todo list and UI state only"},
	"subagent_jobs":  {Effects: EffectRead | EffectMutate, Danger: DangerPolicySafe, Reason: "control plane for dispatches already approved through delegate"},
	"supervisor":     {Effects: EffectExec, Danger: DangerPolicySafe, Reason: "control plane for runs and scopes; halts/injections are explicit and traced"},

	// Process execution.
	"bash":               {Effects: EffectExec, Danger: DangerPolicyAlways},
	"powershell":         {Effects: EffectExec, Danger: DangerPolicyAlways},
	"background_process": {Effects: EffectExec, Danger: DangerPolicyClassifier},
	"go_test":            {Effects: EffectRead | EffectExec, Danger: DangerPolicySafe, Reason: "runs the project's tests (an expected development action); writes only the Go build cache"},
	"staticcheck":        {Effects: EffectRead | EffectExec, Danger: DangerPolicyClassifier, Inspect: true},
	"go_mod":             {Effects: EffectRead | EffectMutate | EffectExec, Danger: DangerPolicyClassifier},
	"bisect":             {Effects: EffectRead | EffectMutate | EffectExec, Danger: DangerPolicyClassifier},
	"git":                {Effects: EffectRead | EffectMutate | EffectExec, Danger: DangerPolicyClassifier, Inspect: true},
	"delegate":           {Effects: EffectExec, Danger: DangerPolicySafe, Reason: "spawns a role-scoped sub-agent under its own approval and ruleset"},

	// Network.
	"http":                   {Effects: EffectNetwork, Danger: DangerPolicyClassifier},
	"webfetch":               {Effects: EffectNetwork, Danger: DangerPolicySafe, Reason: "network read; SSRF guard applies and the result is text, with no local mutation or execution"},
	"memory_search":          {Effects: EffectRead | EffectNetwork, Danger: DangerPolicySafe, Reason: "read-only memory query; an embedding call reaches the configured embedder, nothing is written"},
	"memory_search_sessions": {Effects: EffectRead | EffectNetwork, Danger: DangerPolicySafe, Reason: "read-only session search; an embedding call reaches the configured embedder, nothing is written"},

	// Read-only metadata and waiting.
	"list_subagents": {Effects: EffectRead},
	"wait_for":       {Effects: EffectRead, Danger: DangerPolicyClassifier},
	"watch":          {Effects: EffectRead, Danger: DangerPolicyClassifier},

	// Retained proposals: settle/continue/fork mutate, list/show read.
	"proposal": {Effects: EffectRead | EffectMutate, Danger: DangerPolicyClassifier},
}

// PolicyFor returns the declared policy for a tool.
func PolicyFor(name string) (Policy, bool) {
	p, ok := toolPolicies[name]
	return p, ok
}

// Policies returns a copy of the policy table.
func Policies() map[string]Policy {
	out := make(map[string]Policy, len(toolPolicies))
	for name, p := range toolPolicies {
		out[name] = p
	}
	return out
}

// InspectionPolicyNames returns the tools the table marks as part of the
// read-only inspection surface, sorted by the caller if needed.
func InspectionPolicyNames() []string {
	var names []string
	for name, p := range toolPolicies {
		if p.Inspect {
			names = append(names, name)
		}
	}
	return names
}
