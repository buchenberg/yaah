// Package ir resolves a decoded workflow spec into the immutable graph the
// validators and executor consume: defaults folded into dispatch nodes,
// fan-out/loop bodies expanded into stable node paths, templates compiled
// into segment lists, and structured selectors compiled into a small AST.
//
// Nothing here parses a template or selector string at run time: rendering
// walks pre-compiled segments against a Scope supplied by the scheduler.
// The graph is immutable once resolved; run state lives elsewhere.
package ir

// Kind enumerates the resolved node kinds. The string values mirror the
// spec surface so node paths and goldens read identically.
type Kind string

// Node kinds.
const (
	KindDelegate  Kind = "delegate"
	KindFanout    Kind = "fanout"
	KindBranch    Kind = "branch"
	KindLoop      Kind = "loop"
	KindGate      Kind = "gate"
	KindSettle    Kind = "settle"
	KindSelect    Kind = "select"
	KindAggregate Kind = "aggregate"
	KindCall      Kind = "call"
	KindEmit      Kind = "emit"
	KindHuman     Kind = "human"
)

// Lane enumerates dispatch lanes.
type Lane string

// Dispatch lanes.
const (
	LaneReport   Lane = "report"
	LaneProposal Lane = "proposal"
)

// NetworkMode enumerates network policies for dispatches.
type NetworkMode string

// Network modes.
const (
	NetworkAllowAll NetworkMode = "allow_all"
	NetworkDenyAll  NetworkMode = "deny_all"
	NetworkBroker   NetworkMode = "broker"
)

// FailurePolicy is the per-node failure disposition.
type FailurePolicy string

// Failure policies.
const (
	OnFailureAbort    FailurePolicy = "abort"
	OnFailureContinue FailurePolicy = "continue"
	OnFailureSkip     FailurePolicy = "skip"
)

// ItemFailure is the per-fanout-item failure disposition.
type ItemFailure string

// Item failure policies.
const (
	OnItemAbort           ItemFailure = "abort"
	OnItemContinue        ItemFailure = "continue"
	OnItemCollectFailures ItemFailure = "collect_failures"
)

// Join enumerates fan-out join semantics.
type Join string

// Join kinds.
const (
	JoinAll    Join = "all"
	JoinAny    Join = "any"
	JoinQuorum Join = "quorum"
)

// SettleAction enumerates the four settlement verbs.
type SettleAction string

// Settlement verbs.
const (
	SettleSelect  SettleAction = "select"
	SettleApply   SettleAction = "apply"
	SettleRelease SettleAction = "release"
	SettleDiscard SettleAction = "discard"
)

// ConflictPolicy decides what a settle does on a settlement conflict.
type ConflictPolicy string

// Conflict policies.
const (
	OnConflictStop     ConflictPolicy = "stop"
	OnConflictSkip     ConflictPolicy = "skip"
	OnConflictEscalate ConflictPolicy = "escalate"
)

// EmptyPolicy decides what a settle does when where selects nothing.
type EmptyPolicy string

// Empty policies.
const (
	OnEmptyPass EmptyPolicy = "pass"
	OnEmptyFail EmptyPolicy = "fail"
	OnEmptySkip EmptyPolicy = "skip"
)

// SettleOrder enumerates settle processing orders.
type SettleOrder string

// Settle orders.
const (
	OrderSmallestDiffFirst SettleOrder = "smallest_diff_first"
)

// SelectPolicy enumerates candidate-selection policies.
type SelectPolicy string

// Select policies.
const (
	SelectJudge        SelectPolicy = "judge"
	SelectFirstSuccess SelectPolicy = "first_success"
	SelectSmallestDiff SelectPolicy = "smallest_diff"
)

// AggregateStrategy enumerates combine strategies.
type AggregateStrategy string

// Aggregate strategies.
const (
	AggregateConcat    AggregateStrategy = "concat"
	AggregateSummarize AggregateStrategy = "summarize"
	AggregateJSONMerge AggregateStrategy = "json_merge"
)

// OutputType enumerates the value types a node output may declare.
type OutputType string

// Output value types.
const (
	OutputReport   OutputType = "Report"
	OutputProposal OutputType = "Proposal"
	OutputSettled  OutputType = "Settled"
	OutputVerdict  OutputType = "Verdict"
	OutputPlan     OutputType = "Plan"
	OutputArtifact OutputType = "Artifact"
)

// ParamType enumerates declared workflow parameter types.
type ParamType string

// Parameter types.
const (
	ParamString  ParamType = "string"
	ParamInteger ParamType = "integer"
	ParamNumber  ParamType = "number"
	ParamBoolean ParamType = "boolean"
	ParamList    ParamType = "list"
	ParamMap     ParamType = "map"
)

// GrantAccess enumerates grant access levels.
type GrantAccess string

// Grant access levels.
const (
	AccessReadOnly  GrantAccess = "readonly"
	AccessReadWrite GrantAccess = "readwrite"
)

// Grant scopes filesystem access for a dispatch to a world-relative path.
type Grant struct {
	Path   string      `json:"path"`
	Access GrantAccess `json:"access"`
}

// Retry bounds node retries.
type Retry struct {
	MaxAttempts    *int     `json:"max_attempts,omitempty"`
	BackoffSeconds *float64 `json:"backoff_seconds,omitempty"`
}

// OutputSpec declares a node output binding.
type OutputSpec struct {
	Name   string     `json:"name"`
	Type   OutputType `json:"type,omitempty"`
	Schema string     `json:"schema,omitempty"`
}

// Param is a resolved workflow parameter declaration.
type Param struct {
	Name       string    `json:"name"`
	Type       ParamType `json:"type"`
	Required   bool      `json:"required,omitempty"`
	HasDefault bool      `json:"has_default,omitempty"`
	Default    any       `json:"default,omitempty"`
}

// Budgets bound the cost of a workflow run.
type Budgets struct {
	MaxDispatches           *int `json:"max_dispatches,omitempty"`
	MaxWallClockSeconds     *int `json:"max_wall_clock_seconds,omitempty"`
	MaxTokens               *int `json:"max_tokens,omitempty"`
	MaxConcurrentDispatches *int `json:"max_concurrent_dispatches,omitempty"`
}

// Graph is the resolved, immutable workflow. Steps are top-level nodes in
// spec order; body nodes hang off their containers with stable paths
// ("steps.build.body[0]"). Build only via Resolve.
type Graph struct {
	Name    string           `json:"name"`
	Params  map[string]Param `json:"params,omitempty"`
	Budgets *Budgets         `json:"budgets,omitempty"`
	Steps   []*Node          `json:"steps"`
}

// Node is one resolved workflow node. Payload fields belong to their kind;
// unset fields stay zero. Path is the stable node path, ID the step id
// (empty for unnamed single-node bodies).
type Node struct {
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
	Kind Kind   `json:"kind"`

	Needs          []string      `json:"needs,omitempty"`
	When           *Selector     `json:"when,omitempty"`
	Retry          *Retry        `json:"retry,omitempty"`
	TimeoutSeconds *int          `json:"timeout_seconds,omitempty"`
	OnFailure      FailurePolicy `json:"on_failure,omitempty"`
	Output         *OutputSpec   `json:"output,omitempty"`

	// delegate.
	Role         string      `json:"role,omitempty"`
	Prompt       *Template   `json:"prompt,omitempty"`
	Lane         Lane        `json:"lane,omitempty"`
	SeedFrom     *Template   `json:"seed_from,omitempty"`
	ContinueFrom *Template   `json:"continue_from,omitempty"`
	ForkFrom     *Template   `json:"fork_from,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Model        string      `json:"model,omitempty"`
	MaxTurns     *int        `json:"max_turns,omitempty"`
	Grants       []Grant     `json:"grants,omitempty"`
	Network      NetworkMode `json:"network,omitempty"`
	World        string      `json:"world,omitempty"`

	// fanout.
	Over          *Over       `json:"over,omitempty"`
	As            string      `json:"as,omitempty"`
	MaxParallel   *int        `json:"max_parallel,omitempty"`
	Body          []*Node     `json:"body,omitempty"`
	Collect       string      `json:"collect,omitempty"`
	Join          Join        `json:"join,omitempty"`
	Quorum        *int        `json:"quorum,omitempty"`
	OnItemFailure ItemFailure `json:"on_item_failure,omitempty"`

	// branch.
	On      *Selector `json:"on,omitempty"`
	Cases   []Case    `json:"cases,omitempty"`
	Default string    `json:"default,omitempty"`

	// loop.
	Until         *Selector `json:"until,omitempty"`
	MaxIterations *int      `json:"max_iterations,omitempty"`

	// gate and select.
	Assert     *Assert      `json:"assert,omitempty"`
	Candidates *Selector    `json:"candidates,omitempty"`
	Judge      *Judge       `json:"judge,omitempty"`
	OnPass     string       `json:"on_pass,omitempty"`
	OnFail     string       `json:"on_fail,omitempty"`
	Policy     SelectPolicy `json:"policy,omitempty"`

	// settle.
	Proposals  *Selector      `json:"proposals,omitempty"`
	Where      *Selector      `json:"where,omitempty"`
	Action     SettleAction   `json:"action,omitempty"`
	Order      SettleOrder    `json:"order,omitempty"`
	OnConflict ConflictPolicy `json:"on_conflict,omitempty"`
	OnEmpty    EmptyPolicy    `json:"on_empty,omitempty"`

	// aggregate.
	Inputs   []*Selector       `json:"inputs,omitempty"`
	Strategy AggregateStrategy `json:"strategy,omitempty"`

	// call.
	Workflow   string                `json:"workflow,omitempty"`
	CallParams map[string]ParamValue `json:"call_params,omitempty"`
	MaxDepth   *int                  `json:"max_depth,omitempty"`

	// emit.
	Value        *Template `json:"value,omitempty"`
	ArtifactPath string    `json:"artifact_path,omitempty"`

	// human shares the delegate Prompt field (a template).
	OnTimeout FailurePolicy `json:"on_timeout,omitempty"`
}

// Over is a resolved fan-out source: literal values or a compiled selector.
type Over struct {
	Literals []any     `json:"literals,omitempty"`
	Selector *Selector `json:"selector,omitempty"`
}

// Case is one literal match arm of a branch node.
type Case struct {
	Value any    `json:"value"`
	Then  string `json:"then"`
}

// Assert is a named deterministic check with fixed arguments.
type Assert struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// Judge is a verdict-producing delegate sub-payload.
type Judge struct {
	Role   string    `json:"role"`
	Prompt *Template `json:"prompt"`
}

// ParamValue is one resolved call parameter: a constant or a compiled
// template.
type ParamValue struct {
	Const    any       `json:"const,omitempty"`
	Template *Template `json:"template,omitempty"`
}
