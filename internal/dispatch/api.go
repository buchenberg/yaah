package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
)

// Lane selects the substrate/output pairing for one dispatch: where the work
// runs, and what comes back. The delegate tool and the workflow executor share
// this vocabulary; a nil role resolver keeps the historical proposal lane.
type Lane string

const (
	// LaneProposal: isolated world, sealed delta, orchestrator settles it.
	LaneProposal Lane = "proposal"
	// LaneReport: isolated world, plain report result, world torn down
	// automatically — no proposal, nothing to settle.
	LaneReport Lane = "report"
	// LaneShared: runs directly on the shared workspace, plain report result.
	LaneShared Lane = "shared"
)

// DispatchStatus is the typed outcome of one dispatch. It is deliberately
// coarser than the model-facing envelope: the tool adapter renders the status
// into the existing JSON frames, while the workflow executor switches on it.
type DispatchStatus string

const (
	DispatchRunning   DispatchStatus = "running"
	DispatchCompleted DispatchStatus = "completed"
	DispatchProposal  DispatchStatus = "proposal"
	DispatchTimedOut  DispatchStatus = "timed_out"
	DispatchStuck     DispatchStatus = "stuck"
	DispatchCancelled DispatchStatus = "cancelled"
	DispatchFailed    DispatchStatus = "failed"
)

// DispatchRequest is the typed input to Run. The tool adapter parses and
// validates model-facing arguments, resolves the lane and timeout, then hands
// the resulting values here. Runtime dependencies travel with the request so
// the executor can call Run without importing internal/tools.
type DispatchRequest struct {
	// Description is a short human label for background jobs and results.
	Description string

	// Prompt is the single user message for the sub-agent.
	Prompt string

	// Lane selects the substrate/output pairing. Empty means proposal.
	Lane Lane

	// Params carries the role, budgets, provider/model, and seed settings.
	Params jobs.SubAgentParams

	// Timeout is the effective deadline. Zero means no timeout.
	Timeout time.Duration

	// Background dispatches asynchronously through BackgroundJobs.
	Background bool

	// Runner is the raw sub-agent runner for shared dispatches and background
	// jobs that do not use the retained runtime.
	Runner jobs.TaskRunner

	// Retained, when non-nil, routes proposal and report lanes through the
	// retained runtime. Nil keeps every dispatch on the shared runner.
	Retained *RetainedRuntime

	// BackgroundJobs manages asynchronous dispatches. Required when
	// Background is set.
	BackgroundJobs *jobs.BackgroundJobs

	// BackgroundContextSeeder, when non-nil, re-attaches caller-scoped context
	// (conflict attribution, inherited permissions) to the background job's
	// session-rooted context. The dispatch core does not inspect it.
	BackgroundContextSeeder func(context.Context) context.Context
}

// DispatchResult is the typed outcome of Run. Result is set for a completed
// report; Partial is set for a run that stopped early; Proposal is set for a
// retained proposal lane (including a sealed error envelope).
type DispatchResult struct {
	Status   DispatchStatus
	Role     string
	Timeout  time.Duration
	Result   string
	Partial  string
	JobID    string
	Label    string
	Proposal *ProposalEnvelope
	Err      error
}

// Run executes one sub-agent dispatch through the runtime's only scheduler and
// settlement path. It is the typed core behind the delegate tool and the
// workflow executor.
func Run(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	runner := req.Runner
	if runner == nil && req.Retained != nil {
		runner = req.Retained.Runner
	}
	if runner == nil {
		return DispatchResult{}, fmt.Errorf("delegate: sub-agent runner not configured")
	}
	params := req.Params
	role := params.Role
	lane := req.Lane
	if lane == "" {
		lane = LaneProposal
	}
	if params.Lane == "" {
		if req.Retained == nil || lane == LaneShared {
			params.Lane = "shared"
		} else {
			params.Lane = "retained"
		}
	}
	label := role
	if req.Description != "" {
		label = label + " — " + req.Description
	}

	// Validate seed_mode independently: a typo that empties seed_from must not
	// leave a stray seed_mode silently ignored.
	if params.SeedMode != "" {
		switch params.SeedMode {
		case jobs.SeedModeWorkspace, jobs.SeedModeConversation:
		default:
			return DispatchResult{}, fmt.Errorf("delegate: seed_mode %q must be %q or %q", params.SeedMode, jobs.SeedModeWorkspace, jobs.SeedModeConversation)
		}
		if params.SeedFrom == "" {
			return DispatchResult{}, fmt.Errorf("delegate: seed_mode requires seed_from")
		}
	}
	if params.SeedFrom != "" {
		if req.Retained == nil {
			return DispatchResult{}, fmt.Errorf("delegate: seed_from requires the retained lane")
		}
		if lane != LaneProposal {
			return DispatchResult{}, fmt.Errorf("delegate: seed_from needs a proposal-lane role; %q resolves to the %s lane", role, lane)
		}
	}

	// A dispatch that never reaches the retained resolver has no world of its
	// own: it runs on the session's tree. Publish that resolution so the start
	// event names "shared" instead of falling back to the session-level
	// summary. A background call has no dispatch writer yet — setupSubAgent
	// installs one for foreground dispatches only — so carry the value on the
	// launch context, which the manager seeds its job from.
	if req.Retained == nil || lane == LaneShared {
		world := SubAgentWorld{Resolved: "shared"}
		if req.Background {
			ctx = WithSubAgentWorldPtr(ctx, &world)
		} else {
			WriteSubAgentWorld(ctx, world)
		}
	}

	if req.Background {
		if req.BackgroundJobs == nil {
			return DispatchResult{}, fmt.Errorf("delegate: background mode requested but not available")
		}
		if req.Retained != nil && lane != LaneShared {
			rt := *req.Retained
			report := lane == LaneReport
			runner = func(ctx context.Context, prompt string, sp jobs.SubAgentParams) (string, error) {
				res, err := startRetainedRun(ctx, rt, prompt, sp, req.Timeout, report)
				return res.String(), err
			}
		}
		if req.BackgroundContextSeeder != nil {
			ctx = jobs.WithBackgroundContextSeeder(ctx, req.BackgroundContextSeeder)
		}
		jobID, err := req.BackgroundJobs.Launch(ctx, runner, role, req.Description, req.Prompt, params, req.Timeout)
		if err != nil {
			return DispatchResult{}, err
		}
		return DispatchResult{
			Status:  DispatchRunning,
			Role:    role,
			Timeout: req.Timeout,
			JobID:   jobID,
			Label:   label,
		}, nil
	}

	if req.Retained != nil {
		switch lane {
		case LaneReport:
			res, err := startRetainedRun(ctx, *req.Retained, req.Prompt, params, req.Timeout, true)
			return ClassifyRunnerResult(res.Report, err, role, req.Timeout, ctx), nil
		case LaneShared:
			// A shared-lane role cannot mutate the tree; isolation buys
			// nothing, so fall through to the direct runner.
		default:
			res, err := startRetainedRun(ctx, *req.Retained, req.Prompt, params, req.Timeout, false)
			if err != nil {
				return DispatchResult{}, err
			}
			return DispatchResult{
				Status:   DispatchProposal,
				Role:     role,
				Timeout:  req.Timeout,
				Proposal: res.Proposal,
			}, nil
		}
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	NotifySubAgentReady(runCtx)
	result, err := runner(runCtx, req.Prompt, params)
	return ClassifyRunnerResult(result, err, role, req.Timeout, runCtx), nil
}

// ClassifyRunnerResult maps a runner outcome to the typed dispatch result. A
// non-empty result alongside an error is treated as the partial output the
// sub-agent produced before stopping.
func ClassifyRunnerResult(result string, err error, role string, timeout time.Duration, runCtx context.Context) DispatchResult {
	if err == nil {
		return DispatchResult{Status: DispatchCompleted, Role: role, Timeout: timeout, Result: result}
	}
	// A report-lane failure carries its caveat out of band, so it never has to
	// be parsed back out of model output.
	cause := err
	note := ""
	var runErr *RetainedRunError
	if errors.As(err, &runErr) {
		cause = runErr.Cause()
		note = runErr.Note()
	}
	partial := retainedNote(result, note)
	switch {
	case errors.Is(err, context.DeadlineExceeded), runCtx.Err() == context.DeadlineExceeded:
		return DispatchResult{Status: DispatchTimedOut, Role: role, Timeout: timeout, Partial: partial}
	case errors.Is(err, jobs.ErrStuckChild):
		return DispatchResult{Status: DispatchStuck, Role: role, Timeout: timeout, Partial: partial}
	case errors.Is(err, context.Canceled), runCtx.Err() == context.Canceled:
		return DispatchResult{Status: DispatchCancelled, Role: role, Timeout: timeout, Partial: partial}
	default:
		// A non-nil error makes the agent loop replace the result with the
		// error text delivered verbatim, so a partial the orchestrator should
		// see must ride inside the error. The partial is success-detail
		// content: keep the body's head — the part that orients — bounded at
		// the tool-result cap, and re-attach the caveat, itself tail-bounded
		// because teardown notes end with the worktree path.
		if strings.TrimSpace(partial) == "" {
			return DispatchResult{Status: DispatchFailed, Role: role, Timeout: timeout, Err: cause}
		}
		return DispatchResult{
			Status:  DispatchFailed,
			Role:    role,
			Timeout: timeout,
			Err:     fmt.Errorf("%w\n%s", cause, foldRetainedPartial(result, note)),
		}
	}
}

// SettleVerb is the fixed settlement vocabulary. There is no extension point:
// every world settles with exactly these four verbs.
type SettleVerb string

const (
	SettleSelect  SettleVerb = "select"
	SettleApply   SettleVerb = "apply"
	SettleRelease SettleVerb = "release"
	SettleDiscard SettleVerb = "discard"
)

// SettleOpts carries the only settlement choice a caller may make: the apply
// strategy. Other settlement behavior is fixed by the world.
type SettleOpts struct {
	// Strategy is "path_disjoint" (default) or "three_way" (worktree apply
	// only).
	Strategy string
}

// Settlement is the typed outcome of one settlement. The envelope fields are
// the model-facing record; Outcome is the typed work/scope/record/notes block,
// not a prose status the caller has to re-parse. A conflict is an outcome, not
// an error: the envelope names the overlap and the proposal stays open.
type Settlement struct {
	ProposalEnvelope
	Action  SettleVerb
	Outcome Outcome
}

// newSettlement pairs a settlement envelope with its typed outcome.
func newSettlement(env proposalEnvelope, outcome Outcome) Settlement {
	return Settlement{ProposalEnvelope: env, Action: SettleVerb(env.Action), Outcome: outcome}
}

// Envelope renders the model-facing JSON shape, with the typed outcome folded
// into the single rendered block the proposal schema advertises.
func (s Settlement) Envelope() ProposalEnvelope {
	env := s.ProposalEnvelope
	env.Action = string(s.Action)
	env.Outcome = s.Outcome.String()
	return env
}

// Settle settles one proposal through the registry's only settlement path. The
// proposal id, verb, and strategy are the complete input; the registry owns all
// world-specific behavior.
func Settle(ctx context.Context, registry *ProposalRegistry, id string, verb SettleVerb, opts SettleOpts) (Settlement, error) {
	if registry == nil {
		return Settlement{}, fmt.Errorf("proposal: no session registry configured — this tool is registered with the registry runner.NewDelegateTool creates")
	}
	env, err := registry.settleRetainedProposalWithStrategy(ctx, id, string(verb), opts.Strategy)
	if err != nil {
		var conflict *proposalApplyConflict
		if verb == SettleApply && errors.As(err, &conflict) {
			// A refusal is an outcome the orchestrator must act on, not a
			// failed tool call: return the same typed frame as a settlement,
			// with the overlap paths named. The proposal stays open.
			paths := conflict.Paths
			if paths == nil {
				paths = []string{}
			}
			strategy := conflict.Strategy
			if strategy == "" {
				strategy = proposalApplyPathDisjoint
			}
			conflictView := proposalConflict{
				Kind:      proposalConflictApply,
				Paths:     paths,
				Proposals: []string{conflict.ProposalID},
				Changes:   conflict.Changes,
			}
			next := proposalVerbs()
			if p, perr := registry.get(conflict.ProposalID); perr == nil {
				conflictView = attachProposalConflictDetails(ctx, []proposalConflict{conflictView}, []proposalAnalysis{{proposal: p}})[0]
				next = nextVerbsFor(p)
			}
			return newSettlement(proposalEnvelope{
				Status:     "conflict",
				ProposalID: conflict.ProposalID,
				Action:     string(verb),
				Strategy:   strategy,
				Error:      conflict.Error(),
				Conflicts:  []proposalConflict{conflictView},
				Next:       next,
			}, Outcome{}), nil
		}
		return Settlement{}, err
	}
	return env, nil
}

// MarshalProposalEnvelope renders a typed envelope in the model-facing JSON
// shape. The fallback mirrors the historical behavior: a marshal failure still
// yields a syntactically valid status frame.
func MarshalProposalEnvelope(env ProposalEnvelope) string {
	return marshalProposalEnvelope(env)
}
