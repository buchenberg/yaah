package exec

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Budget axes named in BudgetExceededEvent and Result.Reason. They are
// strings rather than an enum so the event log states which ceiling ran out in
// the same words Limits documents.
const (
	AxisMaxDispatches           = "max_dispatches"
	AxisMaxWallClock            = "max_wall_clock"
	AxisMaxConcurrentDispatches = "max_concurrent_dispatches"
)

// Limits are the ceilings a run enforces. Zero means undeclared, and an
// undeclared axis is declined rather than guessed: this package invents no
// host defaults, because a default cap inside a deterministic core is a host
// policy nobody authored. The caller passes what the graph declares and what
// the host configures — LimitsFromBudgets maps the graph's half.
//
// MaxConcurrentDispatches is a scheduling constraint, not a stop condition:
// the run never exceeds it, and reaching it parks ready nodes until a slot
// frees instead of ending anything. MaxDispatches and MaxWallClock are
// ceilings on the run: exhausting either ends it with RunBudgetExceeded.
type Limits struct {
	MaxConcurrentDispatches int
	MaxDispatches           int
	MaxWallClock            time.Duration
}

// LimitsFromBudgets maps a resolved graph's declared budgets onto Limits. A
// nil field is undeclared and maps to zero. A negative declared value is
// treated as undeclared rather than as an impossible ceiling: the document is
// the author's, and refusing to schedule anything because a field was
// mistyped is the validator's business, not the scheduler's.
//
// MaxTokens is not mapped. Token usage does not arrive on the completion
// envelope, so the scheduler cannot meter the axis it would be enforcing, and
// silently accepting a ceiling it cannot honor is worse than declining it and
// saying so; accounting for tokens belongs to the trace bead (yaah-0u0v.17).
func LimitsFromBudgets(b *ir.Budgets) Limits {
	var out Limits
	if b == nil {
		return out
	}
	if v := b.MaxDispatches; v != nil && *v > 0 {
		out.MaxDispatches = *v
	}
	if v := b.MaxWallClockSeconds; v != nil && *v > 0 {
		out.MaxWallClock = time.Duration(*v) * time.Second
	}
	if v := b.MaxConcurrentDispatches; v != nil && *v > 0 {
		out.MaxConcurrentDispatches = *v
	}
	return out
}

// String renders the declared ceilings for the log line at run start. An
// undeclared axis reads as "none", so a reader can tell a ceiling that was
// never set from one set to zero.
func (l Limits) String() string {
	return fmt.Sprintf("concurrent=%s dispatches=%s wall_clock=%s",
		noneIfZero(l.MaxConcurrentDispatches), noneIfZero(l.MaxDispatches), noneIfDur(l.MaxWallClock))
}

func noneIfZero(v int) string {
	if v <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d", v)
}

func noneIfDur(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	return d.String()
}

// GuardFunc answers whether a node's `when` guard admits it. Selector
// evaluation is value plumbing owned by the bead that extracts values
// (yaah-0u0v.13), so the scheduler asks instead of implementing it. A nil
// Guard means no evaluator is wired, and then every guard is treated as
// satisfied — a choice recorded in Result.Warnings so a caller can see that
// guarded nodes ran.
//
// A Guard must not call back into the Run that is asking: it is invoked from
// inside the reducer, and the reducer is the only place state changes.
type GuardFunc func(n *ir.Node, s ir.Scope) (bool, error)

// Config is a run's input, fixed at New.
type Config struct {
	// RunID labels the run. Empty asks the scheduler to mint one from the
	// graph name; the value always lands in run_started.
	RunID string
	// SpecHash is the digest of the document this graph resolved from
	// (spec.SpecHash). It is persisted with the run so a resume can refuse to
	// continue a run against a document that has changed underneath it
	// (yaah-0u0v.16). Empty declines the refusal, and is only appropriate for a
	// caller that keeps no state.
	SpecHash string
	// SpecPath is the document path the run was started from, recorded as the
	// caller named it. It is what `yaah workflow resume <run_id>` re-reads when
	// no path is given. Informational: the scheduler never opens it.
	SpecPath string
	// Store is the durable projection of this run (yaah-0u0v.16). Nil means the
	// run keeps no state, which is what a one-shot run and every deterministic
	// test do.
	//
	// The store is written from inside the reducer, after every applied event
	// and before Apply returns, so a run that stops at any point leaves a
	// checkpoint that is exactly the state the reducer had committed — never a
	// half-applied step. A save that fails is reported to the driver and NO
	// further event is applied: a run whose state cannot be recorded must not
	// advance, or a resume would re-do work the log never recorded.
	Store Store
	// Limits are the ceilings the caller declares. The scheduler adds none.
	Limits Limits
	// Params binds the workflow parameters the graph declares. A declared
	// default fills an unbound name; a required param with neither is an
	// error at New rather than a template that fails later.
	Params map[string]any
	// Guard evaluates node `when` selectors. Optional; see GuardFunc.
	Guard GuardFunc
	// OpenProposals is the caller's declaration that a run may finish
	// succeeded while proposals it sealed are still undisposed.
	//
	// The default is the refusal: a run that ends with a worktree standing has
	// left the author's work neither landed nor thrown away, and reporting
	// that as success is the outcome WF021 and WF025 exist to prevent. The
	// v1 vocabulary declares no per-node `on_abort` field to discharge it
	// (validate/linearity.go says so at WF025's own rule), so the escape is
	// stated by the host that owns the residue — an interactive caller that
	// will settle from its own surface afterwards — rather than invented as a
	// graph key the strict decoder would refuse. When that field lands, this
	// flag becomes its folded default and the guard keeps the same reason.
	OpenProposals bool
}
