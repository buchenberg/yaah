package spec

import "encoding/json"

// APIVersion is the workflow document apiVersion. There is exactly one
// version and no alias or migration code (WD7): documents that disagree
// with the constant fail to decode.
type APIVersion string

// APIVersionV1 is the only accepted apiVersion.
const APIVersionV1 APIVersion = "yaah.workflow/v1"

// DocumentKind is the workflow document kind.
type DocumentKind string

// KindWorkflow is the only accepted document kind.
const KindWorkflow DocumentKind = "Workflow"

// Document is the full workflow file: envelope plus spec.
type Document struct {
	APIVersion APIVersion   `json:"apiVersion"`
	Kind       DocumentKind `json:"kind"`
	Metadata   Metadata     `json:"metadata"`
	Spec       Spec         `json:"spec"`
}

// Metadata names the workflow. Version is optional and free-form.
type Metadata struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Spec is the workflow body: parameters, shared defaults, budgets, and the
// step graph.
type Spec struct {
	Params   map[string]ParamSpec `json:"params,omitempty"`
	Defaults *Defaults            `json:"defaults,omitempty"`
	Budgets  *Budgets             `json:"budgets,omitempty"`
	Steps    []Node               `json:"steps"`
}

// ParamType enumerates the declared workflow parameter types.
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

// ParamSpec declares one workflow parameter.
type ParamSpec struct {
	Type     ParamType `json:"type"`
	Required bool      `json:"required,omitempty"`
	Default  any       `json:"default,omitempty"`
}

// GrantAccess enumerates grant access levels. The values match the
// delegate tool's grant vocabulary.
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

// Lane enumerates dispatch lanes. Values match the delegate tool.
type Lane string

// Dispatch lanes.
const (
	LaneReport   Lane = "report"
	LaneProposal Lane = "proposal"
)

// NetworkMode enumerates network policies for dispatches. Values match the
// sub-agent network confinement vocabulary (allow_all, deny_all, broker).
type NetworkMode string

// Network modes.
const (
	NetworkAllowAll NetworkMode = "allow_all"
	NetworkDenyAll  NetworkMode = "deny_all"
	NetworkBroker   NetworkMode = "broker"
)

// Defaults are shared delegate settings applied to dispatch nodes that do
// not override them.
type Defaults struct {
	Lane    Lane        `json:"lane,omitempty"`
	Network NetworkMode `json:"network,omitempty"`
	Grants  []Grant     `json:"grants,omitempty"`
}

// Budgets bound the cost of a workflow run. All fields are optional
// pointers so unset (nil) is distinguishable from zero.
type Budgets struct {
	MaxDispatches           *int `json:"max_dispatches,omitempty"`
	MaxWallClockSeconds     *int `json:"max_wall_clock_seconds,omitempty"`
	MaxTokens               *int `json:"max_tokens,omitempty"`
	MaxConcurrentDispatches *int `json:"max_concurrent_dispatches,omitempty"`
}

// NodeKind enumerates the fixed v1 node vocabulary (WD1: no user-defined
// node types).
type NodeKind string

// Node kinds.
const (
	KindDelegate  NodeKind = "delegate"
	KindFanout    NodeKind = "fanout"
	KindBranch    NodeKind = "branch"
	KindLoop      NodeKind = "loop"
	KindGate      NodeKind = "gate"
	KindSettle    NodeKind = "settle"
	KindSelect    NodeKind = "select"
	KindAggregate NodeKind = "aggregate"
	KindCall      NodeKind = "call"
	KindEmit      NodeKind = "emit"
	KindHuman     NodeKind = "human"
)

// FailurePolicy is the per-node failure disposition (§5.7).
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

// Join enumerates fan-out join semantics. P1 executes "all" only; the
// other values decode now and are rejected by later validators/runtime.
type Join string

// Join kinds.
const (
	JoinAll    Join = "all"
	JoinAny    Join = "any"
	JoinQuorum Join = "quorum"
)

// Policy is the shared enum for select and settle policy fields, which
// share the "policy" key on a flat node. Per-kind membership is a
// validator concern.
type Policy string

// Policies.
const (
	PolicyJudge        Policy = "judge"
	PolicyFirstSuccess Policy = "first_success"
	PolicySmallestDiff Policy = "smallest_diff"
	PolicyPathDisjoint Policy = "path_disjoint"
	PolicyThreeWay     Policy = "three_way"
)

// SettleAction enumerates the four settlement verbs (§5.6). continue/fork
// are deliberately absent: they are delegate lifecycle operations
// (continue_from/fork_from), not settlement dispositions.
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

// EmptyPolicy decides what a settle does when `where` selects nothing.
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

// AggregateStrategy enumerates combine strategies.
type AggregateStrategy string

// Aggregate strategies.
const (
	AggregateConcat    AggregateStrategy = "concat"
	AggregateSummarize AggregateStrategy = "summarize"
	AggregateJSONMerge AggregateStrategy = "json_merge"
)

// OutputType enumerates the §5.2 value types a delegate output may
// declare.
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

// OutputSpec declares a node output: a binding name plus an optional
// value type and JSON Schema reference. A bare string decodes as
// {name: <string>}.
type OutputSpec struct {
	Name   string     `json:"name"`
	Type   OutputType `json:"type,omitempty"`
	Schema string     `json:"schema,omitempty"`
}

// Selector is a structured value reference (§5.4): a source path plus one
// optional predicate. A bare string decodes as {from: <string>}. Predicate
// membership (equals vs not_equals vs ...) is a validator concern.
type Selector struct {
	From      string `json:"from"`
	Field     string `json:"field,omitempty"`
	Equals    any    `json:"equals,omitempty"`
	NotEquals any    `json:"not_equals,omitempty"`
	In        []any  `json:"in,omitempty"`
	NotEmpty  *bool  `json:"not_empty,omitempty"`
	Exists    *bool  `json:"exists,omitempty"`
	AsList    *bool  `json:"as_list,omitempty"`
}

// Over is the fan-out source: a selector, a literal list, or the bare
// string selector shorthand.
type Over struct {
	Selector *Selector
	Values   []any
}

// MarshalJSON writes either the literal list or the selector object so the
// canonical encoding stays a flat, deterministic union.
func (o Over) MarshalJSON() ([]byte, error) {
	if o.Values != nil {
		return json.Marshal(o.Values)
	}
	if o.Selector != nil {
		return json.Marshal(o.Selector)
	}
	return []byte("null"), nil
}

// Body is a node body: a single node (shorthand) or a list of nodes.
type Body []Node

// Retry bounds node retries.
type Retry struct {
	MaxAttempts    *int     `json:"max_attempts"`
	BackoffSeconds *float64 `json:"backoff_seconds,omitempty"`
}

// BranchCase is one literal match arm of a branch node.
type BranchCase struct {
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
	Role   string `json:"role"`
	Prompt string `json:"prompt"`
}

// Node is one workflow step. The v1 surface is a flat struct: common
// fields plus the typed payload fields of each kind. Payload fields are
// decoded wherever they appear; rejecting a field that does not belong to
// the node's kind is a validator rule (WF005), not a decode rule.
type Node struct {
	// Common fields (§5.3). ID is required for top-level steps only;
	// body nodes are positional.
	ID             string        `json:"id,omitempty"`
	Kind           NodeKind      `json:"kind"`
	Needs          []string      `json:"needs,omitempty"`
	When           *Selector     `json:"when,omitempty"`
	Retry          *Retry        `json:"retry,omitempty"`
	TimeoutSeconds *int          `json:"timeout_seconds,omitempty"`
	OnFailure      FailurePolicy `json:"on_failure,omitempty"`
	Output         *OutputSpec   `json:"output,omitempty"`

	// delegate.
	Role         string      `json:"role,omitempty"`
	Prompt       string      `json:"prompt,omitempty"`
	Lane         Lane        `json:"lane,omitempty"`
	SeedFrom     string      `json:"seed_from,omitempty"`
	ContinueFrom string      `json:"continue_from,omitempty"`
	ForkFrom     string      `json:"fork_from,omitempty"`
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
	Body          Body        `json:"body,omitempty"`
	Collect       string      `json:"collect,omitempty"`
	Join          Join        `json:"join,omitempty"`
	Quorum        *int        `json:"quorum,omitempty"`
	OnItemFailure ItemFailure `json:"on_item_failure,omitempty"`

	// branch.
	On      *Selector    `json:"on,omitempty"`
	Cases   []BranchCase `json:"cases,omitempty"`
	Default string       `json:"default,omitempty"`

	// loop.
	Until         *Selector `json:"until,omitempty"`
	MaxIterations *int      `json:"max_iterations,omitempty"`

	// gate and select.
	Assert     *Assert   `json:"assert,omitempty"`
	Candidates *Selector `json:"candidates,omitempty"`
	Judge      *Judge    `json:"judge,omitempty"`
	OnPass     string    `json:"on_pass,omitempty"`
	OnFail     string    `json:"on_fail,omitempty"`
	Policy     Policy    `json:"policy,omitempty"`

	// settle.
	Proposals  *Selector      `json:"proposals,omitempty"`
	Where      *Selector      `json:"where,omitempty"`
	Action     SettleAction   `json:"action,omitempty"`
	Order      SettleOrder    `json:"order,omitempty"`
	OnConflict ConflictPolicy `json:"on_conflict,omitempty"`
	OnEmpty    EmptyPolicy    `json:"on_empty,omitempty"`

	// aggregate.
	Inputs   []Selector        `json:"inputs,omitempty"`
	Strategy AggregateStrategy `json:"strategy,omitempty"`

	// call.
	Workflow string         `json:"workflow,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	MaxDepth *int           `json:"max_depth,omitempty"`

	// emit.
	Value string `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`

	// human.
	OnTimeout FailurePolicy `json:"on_timeout,omitempty"`
}
