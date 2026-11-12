package spec

import (
	"reflect"
)

// Parse limits (bead yaah-0u0v.3). They bound what the decoder will even
// look at, so a hostile or runaway spec fails fast with a path-bearing
// error instead of consuming the host.
const (
	// MaxSpecBytes caps the raw document size.
	MaxSpecBytes = 1 << 20 // 1 MiB
	// MaxNodes caps the total number of nodes, including nested fan-out
	// and loop bodies.
	MaxNodes = 256
	// MaxPromptBytes caps the byte size of any single prompt.
	MaxPromptBytes = 64 << 10 // 64 KiB
	// MaxTemplateDepth caps {{ ... }} nesting inside a template string.
	MaxTemplateDepth = 8
)

// nodeType and friends cache reflect types used by the walker and the
// schema emitter.
var (
	documentType  = reflect.TypeOf(Document{})
	specType      = reflect.TypeOf(Spec{})
	nodeType      = reflect.TypeOf(Node{})
	selectorType  = reflect.TypeOf(Selector{})
	outputSpType  = reflect.TypeOf(OutputSpec{})
	overType      = reflect.TypeOf(Over{})
	bodyType      = reflect.TypeOf(Body{})
	judgeType     = reflect.TypeOf(Judge{})
	docKindType   = reflect.TypeOf(DocumentKind(""))
	apiVerType    = reflect.TypeOf(APIVersion(""))
	nodeKindType  = reflect.TypeOf(NodeKind(""))
	grantAccType  = reflect.TypeOf(GrantAccess(""))
	laneType      = reflect.TypeOf(Lane(""))
	networkType   = reflect.TypeOf(NetworkMode(""))
	failureType   = reflect.TypeOf(FailurePolicy(""))
	itemFailType  = reflect.TypeOf(ItemFailure(""))
	joinType      = reflect.TypeOf(Join(""))
	policyType    = reflect.TypeOf(Policy(""))
	settleActType = reflect.TypeOf(SettleAction(""))
	conflictType  = reflect.TypeOf(ConflictPolicy(""))
	emptyType     = reflect.TypeOf(EmptyPolicy(""))
	orderType     = reflect.TypeOf(SettleOrder(""))
	aggStratType  = reflect.TypeOf(AggregateStrategy(""))
	outputTType   = reflect.TypeOf(OutputType(""))
	paramTType    = reflect.TypeOf(ParamType(""))
)

// enumRegistry maps enum types to their accepted values, in declaration
// order. It is the single source of truth for both the decoder and the
// JSON Schema emitter.
var enumRegistry = map[reflect.Type][]string{
	apiVerType:   {string(APIVersionV1)},
	docKindType:  {string(KindWorkflow)},
	nodeKindType: nodeKindValues(),
	grantAccType: {string(AccessReadOnly), string(AccessReadWrite)},
	laneType:     {string(LaneReport), string(LaneProposal)},
	networkType:  {string(NetworkAllowAll), string(NetworkDenyAll), string(NetworkBroker)},
	failureType:  {string(OnFailureAbort), string(OnFailureContinue), string(OnFailureSkip)},
	itemFailType: {string(OnItemAbort), string(OnItemContinue), string(OnItemCollectFailures)},
	joinType:     {string(JoinAll), string(JoinAny), string(JoinQuorum)},
	policyType: {
		string(PolicyJudge), string(PolicyFirstSuccess), string(PolicySmallestDiff),
		string(PolicyPathDisjoint), string(PolicyThreeWay),
	},
	settleActType: {string(SettleSelect), string(SettleApply), string(SettleRelease), string(SettleDiscard)},
	conflictType:  {string(OnConflictStop), string(OnConflictSkip), string(OnConflictEscalate)},
	emptyType:     {string(OnEmptyPass), string(OnEmptyFail), string(OnEmptySkip)},
	orderType:     {string(OrderSmallestDiffFirst)},
	aggStratType:  {string(AggregateConcat), string(AggregateSummarize), string(AggregateJSONMerge)},
	outputTType: {
		string(OutputReport), string(OutputProposal), string(OutputSettled),
		string(OutputVerdict), string(OutputPlan), string(OutputArtifact),
	},
	paramTType: {
		string(ParamString), string(ParamInteger), string(ParamNumber),
		string(ParamBoolean), string(ParamList), string(ParamMap),
	},
}

// nodeKinds is the set of accepted node kinds, derived from the registry.
var nodeKinds = func() map[string]bool {
	m := make(map[string]bool, len(enumRegistry[nodeKindType]))
	for _, v := range enumRegistry[nodeKindType] {
		m[v] = true
	}
	return m
}()

func nodeKindValues() []string {
	return []string{
		string(KindDelegate), string(KindFanout), string(KindBranch),
		string(KindLoop), string(KindGate), string(KindSettle),
		string(KindSelect), string(KindAggregate), string(KindCall),
		string(KindEmit), string(KindHuman),
	}
}

// checkEnum validates a decoded string against the registry. Types not in
// the registry pass through unchecked.
func checkEnum(t reflect.Type, s, path string) error {
	values, ok := enumRegistry[t]
	if !ok {
		return nil
	}
	for _, v := range values {
		if v == s {
			return nil
		}
	}
	return decodeErrf(path, "unknown %s %q", t.Name(), s)
}
