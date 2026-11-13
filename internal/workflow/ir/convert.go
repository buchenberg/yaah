package ir

import "github.com/buchenberg/yaah/internal/workflow/spec"

// Converters from the spec (authoring) enums to the ir (resolved) enums.
// The spec decoder already rejects values outside these sets, so the
// default branches are defensive: they surface bugs in resolve, not in
// authoring.

func convertKind(k spec.NodeKind) (Kind, error) {
	switch k {
	case spec.KindDelegate:
		return KindDelegate, nil
	case spec.KindFanout:
		return KindFanout, nil
	case spec.KindBranch:
		return KindBranch, nil
	case spec.KindLoop:
		return KindLoop, nil
	case spec.KindGate:
		return KindGate, nil
	case spec.KindSettle:
		return KindSettle, nil
	case spec.KindSelect:
		return KindSelect, nil
	case spec.KindAggregate:
		return KindAggregate, nil
	case spec.KindCall:
		return KindCall, nil
	case spec.KindEmit:
		return KindEmit, nil
	case spec.KindHuman:
		return KindHuman, nil
	}
	return "", resolveErrf("", "unconvertible node kind %q", k)
}

func convertLane(l spec.Lane) (Lane, error) {
	switch l {
	case spec.LaneReport:
		return LaneReport, nil
	case spec.LaneProposal:
		return LaneProposal, nil
	}
	return "", resolveErrf("", "unconvertible lane %q", l)
}

func convertNetwork(n spec.NetworkMode) (NetworkMode, error) {
	switch n {
	case spec.NetworkAllowAll:
		return NetworkAllowAll, nil
	case spec.NetworkDenyAll:
		return NetworkDenyAll, nil
	case spec.NetworkBroker:
		return NetworkBroker, nil
	}
	return "", resolveErrf("", "unconvertible network mode %q", n)
}

func convertFailurePolicy(f spec.FailurePolicy) (FailurePolicy, error) {
	switch f {
	case "":
		return "", nil
	case spec.OnFailureAbort:
		return OnFailureAbort, nil
	case spec.OnFailureContinue:
		return OnFailureContinue, nil
	case spec.OnFailureSkip:
		return OnFailureSkip, nil
	}
	return "", resolveErrf("", "unconvertible failure policy %q", f)
}

func convertItemFailure(f spec.ItemFailure) (ItemFailure, error) {
	switch f {
	case "":
		return "", nil
	case spec.OnItemAbort:
		return OnItemAbort, nil
	case spec.OnItemContinue:
		return OnItemContinue, nil
	case spec.OnItemCollectFailures:
		return OnItemCollectFailures, nil
	}
	return "", resolveErrf("", "unconvertible item failure policy %q", f)
}

func convertJoin(j spec.Join) (Join, error) {
	switch j {
	case "":
		return "", nil
	case spec.JoinAll:
		return JoinAll, nil
	case spec.JoinAny:
		return JoinAny, nil
	case spec.JoinQuorum:
		return JoinQuorum, nil
	}
	return "", resolveErrf("", "unconvertible join %q", j)
}

func convertSettleAction(a spec.SettleAction) (SettleAction, error) {
	switch a {
	case spec.SettleSelect:
		return SettleSelect, nil
	case spec.SettleApply:
		return SettleApply, nil
	case spec.SettleRelease:
		return SettleRelease, nil
	case spec.SettleDiscard:
		return SettleDiscard, nil
	}
	return "", resolveErrf("", "unconvertible settle action %q", a)
}

func convertConflictPolicy(c spec.ConflictPolicy) (ConflictPolicy, error) {
	switch c {
	case "":
		return "", nil
	case spec.OnConflictStop:
		return OnConflictStop, nil
	case spec.OnConflictSkip:
		return OnConflictSkip, nil
	case spec.OnConflictEscalate:
		return OnConflictEscalate, nil
	}
	return "", resolveErrf("", "unconvertible conflict policy %q", c)
}

func convertEmptyPolicy(e spec.EmptyPolicy) (EmptyPolicy, error) {
	switch e {
	case "":
		return "", nil
	case spec.OnEmptyPass:
		return OnEmptyPass, nil
	case spec.OnEmptyFail:
		return OnEmptyFail, nil
	case spec.OnEmptySkip:
		return OnEmptySkip, nil
	}
	return "", resolveErrf("", "unconvertible empty policy %q", e)
}

func convertSettleOrder(o spec.SettleOrder) (SettleOrder, error) {
	switch o {
	case "":
		return "", nil
	case spec.OrderSmallestDiffFirst:
		return OrderSmallestDiffFirst, nil
	}
	return "", resolveErrf("", "unconvertible settle order %q", o)
}

func convertSelectPolicy(p spec.Policy) (SelectPolicy, error) {
	switch p {
	case spec.PolicyJudge:
		return SelectJudge, nil
	case spec.PolicyFirstSuccess:
		return SelectFirstSuccess, nil
	case spec.PolicySmallestDiff:
		return SelectSmallestDiff, nil
	}
	return "", resolveErrf("", "unconvertible select policy %q", p)
}

// convertSettlePolicy converts the settle's half of the shared `policy` key.
// A selection policy is not a settlement disposition, so it is rejected here
// rather than dropped: resolveSettle is the only reader of `policy` on a
// settle, and a value converted to "" is invisible to WF014, which reads the
// graph and cannot name a field the resolver never set. convertSelectPolicy
// rejects the mirror case for the same reason.
func convertSettlePolicy(p spec.Policy) (SettlePolicy, error) {
	switch p {
	case spec.PolicyPathDisjoint:
		return SettlePathDisjoint, nil
	case spec.PolicyThreeWay:
		return SettleThreeWay, nil
	}
	return "", resolveErrf("", "unconvertible settle policy %q", p)
}

func convertAggregateStrategy(s spec.AggregateStrategy) (AggregateStrategy, error) {
	switch s {
	case "":
		return "", nil
	case spec.AggregateConcat:
		return AggregateConcat, nil
	case spec.AggregateSummarize:
		return AggregateSummarize, nil
	case spec.AggregateJSONMerge:
		return AggregateJSONMerge, nil
	}
	return "", resolveErrf("", "unconvertible aggregate strategy %q", s)
}

func convertOutputType(t spec.OutputType) (OutputType, error) {
	switch t {
	case "":
		return "", nil
	case spec.OutputReport:
		return OutputReport, nil
	case spec.OutputProposal:
		return OutputProposal, nil
	case spec.OutputSettled:
		return OutputSettled, nil
	case spec.OutputVerdict:
		return OutputVerdict, nil
	case spec.OutputPlan:
		return OutputPlan, nil
	case spec.OutputArtifact:
		return OutputArtifact, nil
	}
	return "", resolveErrf("", "unconvertible output type %q", t)
}

func convertParamType(t spec.ParamType) (ParamType, error) {
	switch t {
	case spec.ParamString:
		return ParamString, nil
	case spec.ParamInteger:
		return ParamInteger, nil
	case spec.ParamNumber:
		return ParamNumber, nil
	case spec.ParamBoolean:
		return ParamBoolean, nil
	case spec.ParamList:
		return ParamList, nil
	case spec.ParamMap:
		return ParamMap, nil
	}
	return "", resolveErrf("", "unconvertible param type %q", t)
}

func convertGrants(gs []spec.Grant) ([]Grant, error) {
	if gs == nil {
		return nil, nil
	}
	out := make([]Grant, 0, len(gs))
	for _, g := range gs {
		access, err := convertGrantAccess(g.Access)
		if err != nil {
			return nil, err
		}
		out = append(out, Grant{Path: g.Path, Access: access})
	}
	return out, nil
}

func convertGrantAccess(a spec.GrantAccess) (GrantAccess, error) {
	switch a {
	case spec.AccessReadOnly:
		return AccessReadOnly, nil
	case spec.AccessReadWrite:
		return AccessReadWrite, nil
	}
	return "", resolveErrf("", "unconvertible grant access %q", a)
}
