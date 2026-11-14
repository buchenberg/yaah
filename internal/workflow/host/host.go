// Package host runs a resolved workflow graph on this machine. It is the door
// the scheduler core (internal/workflow/exec) documents but must not implement:
// it translates one exec.Dispatch into one internal/dispatch.Run call on the
// report lane, translates the completion envelope back into the scheduler's
// Completion mirror, and performs the side effects a run declares that a
// deterministic core may never perform — writing an emit node's declared
// artifact file.
//
// # Why this package exists instead of exec/dispatchruntime.go
//
// internal/workflow/exec must have no internal/tools dependency, and
// internal/dispatch links internal/tools/substrate. Importing dispatch from
// exec would break the criterion for the whole package, which is why the
// scheduler reads the completion as the mirrored Completion type in
// exec/envelope.go and why the producer that bridges the two lives here,
// outside both. envelope_parity_test.go (this package) is the pin that
// exec/doc.go names: it decodes envelopes rendered by dispatch's own result
// type into exec.Completion and fails if a scheduler-visible fact moves or
// disappears.
//
// # What .12 binds, and what it declines
//
// delegate on the report lane: the node's role, prompt (rendered from the
// compiled template segments against the dispatch's scope snapshot — no
// template parsing happens here), per-node provider/model/max_turns/
// timeout_seconds overrides, and the typed output declaration.
//
// emit: no dispatch at all. Its value is rendered by the reducer
// (exec/emit.go); what this package adds is the artifact write, restricted to
// the paths the document declared.
//
// A node the lane cannot honor is declined at the door with an error rather
// than run with a silently weaker meaning: a proposal-lane delegate needs the
// settlement verbs (yaah-0u0v.13), as does a seed_from/continue_from/fork_from
// binding. A node that declares a world, grants, or a network posture is
// dispatched on the shared report lane and reported in Result.Warnings — the
// dispatch happened, the isolation did not, and an author must be able to tell
// those apart from the run's own output.
package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Runtime is the exec.Runtime door over the dispatch lane.
//
// The scheduler's contract is start/poll: Start must not block on the attempt,
// Completed must give a stable answer, and Block lets the run idle without
// spinning. So one attempt runs in its own goroutine, its envelope is stored
// under the dispatch id the scheduler minted, and every read of that store is
// mutex-guarded — the store outlives the goroutine that wrote it, and the
// scheduler polls it from its own.
type Runtime struct {
	// Runner is the sub-agent runner the door dispatches through. Required.
	// Injecting it is also the deterministic seam every test in this package
	// uses: dispatch.Run takes a jobs.TaskRunner, so a scripted closure runs
	// a whole workflow with no provider and no model call.
	Runner jobs.TaskRunner

	// Retained is the session's retained-proposal runtime (yaah-0u0v.13):
	// proposal-lane delegates seal through it and settle nodes dispose
	// through its registry. Nil means the door serves report-lane runs only.
	Retained *dispatch.RetainedRuntime

	// Clock reads the door time. Nil means time.Now. The scheduler samples it
	// at event boundaries; a test supplies a virtual clock so wall-clock
	// budget behavior is deterministic without sleeping.
	Clock func() time.Time

	// Wait honors a node's retry backoff. Nil waits on a real timer bounded
	// by ctx; a test supplies a recorder that returns at once, so no test in
	// this package sleeps.
	Wait func(ctx context.Context, d time.Duration) error

	mu          sync.Mutex
	completions map[string]exec.Completion
	cancels     map[string]context.CancelFunc
	// wake closes on every stored completion and is replaced, so a Block that
	// started waiting cannot miss a completion; unnotified counts completions
	// that arrived since the last Block returned, which is what keeps Block
	// from both hanging on a completion it already saw and spinning without
	// one.
	wake       chan struct{}
	unnotified int
	requests   []dispatch.DispatchRequest
}

// NewRuntime prepares a door. Runner must be non-nil.
func NewRuntime(runner jobs.TaskRunner) *Runtime {
	return &Runtime{
		Runner:      runner,
		completions: map[string]exec.Completion{},
		cancels:     map[string]context.CancelFunc{},
		wake:        make(chan struct{}),
	}
}

var _ exec.Runtime = (*Runtime)(nil)

// Start renders the node's prompt against the dispatch's scope snapshot and
// launches the attempt. A prompt that will not render, or a node this lane
// cannot honor, is reported synchronously: nothing was dispatched, and the
// scheduler records that as a door malfunction with no envelope rather than
// inventing one.
func (rt *Runtime) Start(ctx context.Context, d exec.Dispatch) (exec.Handle, error) {
	if d.Node == nil {
		return exec.Handle{}, errors.New("workflow/host: dispatch with no node")
	}
	if d.DispatchID == "" {
		return exec.Handle{}, errors.New("workflow/host: dispatch with no dispatch id")
	}
	if rt.Runner == nil {
		return exec.Handle{}, errors.New("workflow/host: no sub-agent runner configured")
	}
	lane, err := laneFor(d.Node)
	if err != nil {
		return exec.Handle{}, err
	}
	prompt, err := renderPrompt(d)
	if err != nil {
		return exec.Handle{}, err
	}
	req := dispatch.DispatchRequest{
		Description: descriptionFor(d.Node),
		Prompt:      prompt,
		Lane:        lane,
		Params:      paramsFor(d.Node),
		Timeout:     d.Timeout,
		Runner:      rt.Runner,
	}
	if lane == dispatch.LaneProposal {
		// A proposal sealed by a run that cannot settle it is a leak the
		// scheduler's linearity guard would only catch at the end; refuse at
		// the door instead, before any work starts.
		if rt.Retained == nil || rt.Retained.Proposals == nil {
			return exec.Handle{}, fmt.Errorf(
				"workflow/host: node %s dispatches on the proposal lane but no retained proposal runtime is configured; sealing needs the session's retained runtime and settle needs its registry",
				d.Node.Path)
		}
		req.Retained = rt.Retained
	}

	runCtx, runCancel := context.WithCancel(ctx)
	cancel := runCancel
	// The run's wall-clock budget is the lane's problem, not the reducer's:
	// the scheduler has no timers, so it hands the deadline here, where it
	// can actually stop work in flight. The composite cancel closes over
	// runCancel by its own binding — rebinding `cancel` to a closure that
	// calls `cancel` would recurse into itself forever.
	if !d.Deadline.IsZero() {
		dlCtx, dlCancel := context.WithDeadline(runCtx, d.Deadline)
		cancel = func() { dlCancel(); runCancel() }
		runCtx = dlCtx
	}
	// The envelope names the dispatch it describes, so a trace joins request
	// and completion on the same id the scheduler minted.
	runCtx = jobs.WithDispatchID(runCtx, d.DispatchID)

	rt.mu.Lock()
	rt.cancels[d.DispatchID] = cancel
	rt.requests = append(rt.requests, req)
	rt.mu.Unlock()

	go func() {
		defer cancel()
		res, err := dispatch.Run(runCtx, req)
		rt.store(d.DispatchID, envelopeToCompletion(res, d, err))
	}()
	return exec.Handle{DispatchID: d.DispatchID}, nil
}

// Completed reports the stored envelope for a started attempt. The answer is
// stable once given, which is what lets the scheduler poll the head of its
// dispatch order until that attempt's turn to commit arrives.
func (rt *Runtime) Completed(h exec.Handle) (exec.Completion, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	c, ok := rt.completions[h.DispatchID]
	return c, ok
}

// Block waits until at least one started attempt has become completable, or
// ctx is done. It never returns nil without a completion behind it — the
// scheduler treats that as a door malfunction, and a run that spins on it is the
// failure this door must not produce — and it never waits past a completion that
// landed between the scheduler's poll and this call, because that count is
// carried across calls.
func (rt *Runtime) Block(ctx context.Context) error {
	rt.mu.Lock()
	if rt.unnotified > 0 {
		rt.unnotified = 0
		rt.mu.Unlock()
		return nil
	}
	ch := rt.wake
	rt.mu.Unlock()
	select {
	case <-ch:
		rt.mu.Lock()
		rt.unnotified = 0
		rt.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel stops a running attempt. The scheduler's state does not wait for the
// world to agree: the node is cancelled when the request is made, so the door
// only has to report whether it could actually ask.
func (rt *Runtime) Cancel(h exec.Handle) error {
	rt.mu.Lock()
	cancel, ok := rt.cancels[h.DispatchID]
	rt.mu.Unlock()
	if !ok {
		return fmt.Errorf("workflow/host: cancel for unknown dispatch %q", h.DispatchID)
	}
	cancel()
	return nil
}

// DefaultSettleStrategy is the runtime default for an apply whose node
// declared no policy (yaah-0u0v.13). Validate deliberately does not
// materialize an empty SettlePolicy — the default is a runtime fact, and
// this is the one place it is named.
const DefaultSettleStrategy = "path_disjoint"

// Settle disposes of one proposal through the lane's existing settlement
// runtime (yaah-0u0v.13). This is an adapter, not a second implementation:
// the scheduler resolved the refs, enforced linearity, and picked the verb;
// the door only translates the request into dispatch.Settle and translates
// the Settlement back into the facts the reducer reads. Every conflict
// detail dispatch.Settle reports — overlap paths, strategy — is preserved,
// because on_conflict: stop must halt with a machine-readable conflict.
func (rt *Runtime) Settle(ctx context.Context, req exec.SettleRequest) (exec.Settled, error) {
	out := exec.Settled{ProposalID: req.ProposalID, Producer: req.Producer, Action: req.Verb}
	if rt.Retained == nil || rt.Retained.Proposals == nil {
		out.Status = "error"
		out.Error = "workflow/host: settle without a retained proposal registry"
		out.LeftOpen = true
		return out, errors.New(out.Error)
	}
	var verb dispatch.SettleVerb
	switch req.Verb {
	case "select":
		verb = dispatch.SettleSelect
	case "apply":
		verb = dispatch.SettleApply
	case "release":
		verb = dispatch.SettleRelease
	case "discard":
		verb = dispatch.SettleDiscard
	default:
		out.Status = "error"
		out.Error = fmt.Sprintf("workflow/host: %q is not a settlement verb; the vocabulary is select|apply|release|discard", req.Verb)
		out.LeftOpen = true
		return out, errors.New(out.Error)
	}
	strategy := string(req.Policy)
	if strategy == "" {
		strategy = DefaultSettleStrategy
	}
	s, err := dispatch.Settle(ctx, rt.Retained.Proposals, req.ProposalID, verb, dispatch.SettleOpts{Strategy: strategy})
	env := s.Envelope()
	out.Status = env.Status
	out.Outcome = s.Outcome.String()
	out.Files = env.Files
	for _, c := range env.Conflicts {
		out.Conflicts = append(out.Conflicts, c.Paths...)
	}
	if env.Status != "settled" || err != nil {
		// A conflict or error leaves the proposal undisposed; the reducer
		// decides what that means for the run under its on_conflict policy.
		out.LeftOpen = true
	}
	if err != nil {
		if out.Error == "" {
			out.Error = err.Error()
		}
		return out, err
	}
	return out, nil
}

// Backoff waits out a node's declared retry delay.
func (rt *Runtime) Backoff(ctx context.Context, d time.Duration) error {
	if rt.Wait != nil {
		return rt.Wait(ctx, d)
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Now reads the door clock.
func (rt *Runtime) Now() time.Time {
	if rt.Clock != nil {
		return rt.Clock()
	}
	return time.Now()
}

// Requests returns the dispatch requests the door has submitted, in start
// order. Tests read the rendered prompt and the resolved overrides back out of
// it, which is the only way to check what the lane was actually asked for.
func (rt *Runtime) Requests() []dispatch.DispatchRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]dispatch.DispatchRequest(nil), rt.requests...)
}

// store records one completion and wakes any waiting Block. The scheduler never
// polls a dispatch it did not start, so a completion arriving after a cancel is
// dropped by the reducer's own state; keeping the record is harmless and lets a
// late envelope be observed in a test.
func (rt *Runtime) store(id string, c exec.Completion) {
	rt.mu.Lock()
	rt.completions[id] = c
	rt.unnotified++
	close(rt.wake)
	rt.wake = make(chan struct{})
	rt.mu.Unlock()
}

// judgeFor returns the judge payload of a node that dispatches its judge as
// the attempt: a select or gate whose policy/declaration sends a verdict
// delegate. The scheduler hands such a node to the door with Node still
// pointing at the container, so the door must read the prompt and role from
// the judge payload, not from the container's own (empty) delegate fields.
func judgeFor(n *ir.Node) *ir.Judge {
	if n == nil || n.Judge == nil {
		return nil
	}
	switch n.Kind {
	case ir.KindSelect, ir.KindGate:
		return n.Judge
	}
	return nil
}

// laneFor resolves the node's lane onto the one lane this door dispatches.
func laneFor(n *ir.Node) (dispatch.Lane, error) {
	switch n.Lane {
	case ir.LaneReport:
		return dispatch.LaneReport, nil
	case "":
		// An undeclared lane runs as a report: the workflow document states
		// no proposal, and a run that sealed one would owe a settlement verb
		// this bead does not have.
		return dispatch.LaneReport, nil
	case ir.LaneProposal:
		return dispatch.LaneProposal, nil
	}
	return "", fmt.Errorf("workflow/host: node %s declares unknown lane %q", n.Path, string(n.Lane))
}

// renderPrompt renders the node's compiled prompt template against the scope
// snapshot the scheduler took at start time. Segments are walked, never
// re-parsed: a malformed reference was a resolve failure (WF001) long before
// it could reach here.
func renderPrompt(d exec.Dispatch) (string, error) {
	prompt := d.Node.Prompt
	if j := judgeFor(d.Node); j != nil {
		prompt = j.Prompt
	}
	if prompt == nil {
		return "", fmt.Errorf("workflow/host: node %s has no prompt to dispatch", d.Node.Path)
	}
	res, err := prompt.Render(d.Scope, ir.RenderOptions{})
	if err != nil {
		return "", fmt.Errorf("workflow/host: node %s prompt: %w", d.Node.Path, err)
	}
	if res.Text == "" {
		return "", fmt.Errorf("workflow/host: node %s prompt renders empty", d.Node.Path)
	}
	return res.Text, nil
}

// seedDeclined reports the delegate bindings this lane cannot honor yet.
func seedDeclined(n *ir.Node) string {
	switch {
	case n.SeedFrom != nil:
		return "seed_from"
	case n.ContinueFrom != nil:
		return "continue_from"
	case n.ForkFrom != nil:
		return "fork_from"
	}
	return ""
}

func descriptionFor(n *ir.Node) string {
	if n.ID != "" {
		return "workflow step " + n.ID
	}
	return "workflow step " + n.Path
}

// paramsFor maps the node's per-dispatch overrides onto the sub-agent params.
// Every field left zero means "the role and configuration decide" — the door
// adds no defaults of its own, which is the same rule exec.Limits follows.
func paramsFor(n *ir.Node) jobs.SubAgentParams {
	role := n.Role
	if j := judgeFor(n); j != nil {
		role = j.Role
	}
	p := jobs.SubAgentParams{
		Role:     role,
		Provider: n.Provider,
		Model:    n.Model,
	}
	if n.TimeoutSeconds != nil && *n.TimeoutSeconds > 0 {
		p.TimeoutSeconds = *n.TimeoutSeconds
	}
	if n.MaxTurns != nil && *n.MaxTurns > 0 {
		p.MaxToolTurns = *n.MaxTurns
	}
	// A declared output schema is the author's statement that the result is
	// structured JSON. It rides the existing response-contract machinery —
	// json_mode sets the provider response_format, the role's contract block
	// shapes the answer, and the envelope's `contract` block carries the
	// verdict (yaah-was9.2). This package runs no second validator: the
	// verdict is mirrored into exec.Completion and recorded as-is.
	if n.Output != nil && n.Output.Schema != "" {
		p.JSONMode = true
	}
	return p
}

// envelopeToCompletion maps the one dispatch result envelope onto the
// scheduler's mirror. The status, stop reason, and recovery block are taken
// verbatim — the scheduler's retry rule reads envelope.recovery.retryable and
// nothing else, so deriving a retryability here would be a second retry brain.
func envelopeToCompletion(res dispatch.DispatchResult, d exec.Dispatch, runErr error) exec.Completion {
	env := res.Envelope()
	c := exec.Completion{
		Status:     env.Status,
		DispatchID: env.DispatchID,
		Role:       env.Role,
		Lane:       env.Lane,
		World:      env.World,
		Model:      env.Model,
		DurationMs: env.DurationMs,
		StopReason: env.StopReason,
		ProposalID: env.ProposalID,
		Result:     env.Result,
		Error:      env.Error,
		Partial:    env.Partial,
	}
	if c.DispatchID == "" {
		c.DispatchID = d.DispatchID
	}
	if c.Role == "" {
		c.Role = d.Node.Role
		if j := judgeFor(d.Node); j != nil {
			c.Role = j.Role
		}
	}
	if c.Lane == "" {
		c.Lane = string(d.Node.Lane)
	}
	if env.Recovery != nil {
		c.Recovery = &exec.Recovery{Retryable: env.Recovery.Retryable, Recommended: env.Recovery.Recommended}
	}
	if env.Contract != nil {
		c.Contract = &exec.Contract{Name: env.Contract.Name, Status: env.Contract.Status}
	}
	if runErr != nil {
		// dispatch.Run returned before any sub-agent ran: a refused input, a
		// missing runner. There is no envelope assertion behind it, so no
		// recovery block is stated — absent is not a licence to retry.
		c.Status = exec.StatusError
		if c.Error == "" {
			c.Error = runErr.Error()
		}
		c.Recovery = nil
	}
	return c
}

// Artifact is one emit node's declared artifact, written by this package after
// the run recorded it.
type Artifact struct {
	// NodePath and Name identify the producing emit node and its output
	// binding.
	NodePath string `json:"node_path"`
	Name     string `json:"name,omitempty"`
	// Declared is the world-relative path as the document wrote it; Path is
	// the host path it was resolved to under the artifact root.
	Declared string `json:"declared_path"`
	Path     string `json:"path"`
	Bytes    int    `json:"bytes"`
}

// Options configures one run.
type Options struct {
	// Graph is the resolved graph. Required.
	Graph *ir.Graph
	// RunID labels the run; empty asks the scheduler to mint one from the
	// graph name.
	RunID string
	// Params binds the workflow's declared parameters. Execute uses these.
	// Resume ignores them: it executes under the checkpoint's recorded
	// parameters, which are the values the run actually started with.
	Params map[string]any
	// Runner is the sub-agent runner the door dispatches through. Required.
	Runner jobs.TaskRunner
	// Retained wires the session's retained-proposal runtime (yaah-0u0v.13).
	// Required for proposal-lane delegates and settle nodes; nil declines both
	// at the door with an honest error rather than sealing proposals nobody
	// can settle.
	Retained *dispatch.RetainedRuntime
	// ArtifactRoot is the directory an emit node's declared artifact path is
	// resolved under. Empty declines every artifact write and says so in
	// Result.Warnings — the run still succeeds, because the value was
	// produced and recorded; only the file was not asked for.
	ArtifactRoot string

	// Store is the durable projection of the run (yaah-0u0v.16). Nil means the
	// run keeps no state between processes, which is what a one-shot run does;
	// a run that has a store can be killed and resumed.
	Store exec.Store
	// SpecHash and SpecPath identify the document the graph was resolved from.
	// The hash is what a resume compares before continuing a run, and the path
	// is what `yaah workflow resume <run_id>` re-reads when it is not given one.
	SpecHash string
	SpecPath string

	// Now and Wait override the door's clock and backoff for deterministic
	// tests.
	Now  func() time.Time
	Wait func(ctx context.Context, d time.Duration) error
}

// Result is one finished workflow run: the scheduler's result, the artifacts
// this package wrote, and the door-side facts the reducer cannot know.
type Result struct {
	Run       exec.Result `json:"run"`
	Artifacts []Artifact  `json:"artifacts"`
	// Warnings are door-side facts: a declined artifact write, a binding the
	// shared report lane could not honor. The reducer's own warnings (an
	// unevaluated `when` guard) are on Run.Warnings.
	Warnings []string `json:"warnings"`

	// Runtime is the door that ran the work, kept so a caller can read the
	// requests it submitted.
	Runtime *Runtime `json:"-"`
}

// Requests returns the dispatch requests the door submitted, in start order.
func (r Result) Requests() []dispatch.DispatchRequest {
	if r.Runtime == nil {
		return nil
	}
	return r.Runtime.Requests()
}

// Execute runs the graph to a terminal state and then writes the artifacts the
// run declared. The scheduler's ordering, budgets, retries, and event log are
// entirely exec's; nothing here mutates run state.
func Execute(ctx context.Context, opts Options) (Result, error) {
	if opts.Graph == nil {
		return Result{}, errors.New("workflow/host: nil graph")
	}
	if opts.Runner == nil {
		return Result{}, errors.New("workflow/host: no sub-agent runner configured")
	}
	rt := NewRuntime(opts.Runner)
	rt.Retained = opts.Retained
	rt.Clock = opts.Now
	rt.Wait = opts.Wait
	cfg := exec.Config{
		RunID:    opts.RunID,
		Params:   opts.Params,
		Limits:   exec.LimitsFromBudgets(opts.Graph.Budgets),
		SpecHash: opts.SpecHash,
		SpecPath: opts.SpecPath,
		Store:    opts.Store,
		// Guard stays nil: `when` selectors are evaluated by the value-plumbing
		// bead (yaah-0u0v.13). exec.New records that every guarded node was
		// scheduled as if its guard passed in Result.Warnings, so the gap is
		// visible in the run's own output rather than being papered over with
		// a half-evaluator here.
	}
	run, err := exec.New(opts.Graph, rt, cfg)
	if err != nil {
		return Result{}, err
	}
	res, err := run.Execute(ctx)
	out := Result{
		Run:     res,
		Runtime: rt,
	}
	if err != nil {
		return out, err
	}
	return finish(opts, out)
}

// Resume continues a run from its durable state (yaah-0u0v.16). The nodes that
// had terminated stay terminated, the ones that were waiting stay waiting, and
// only the work the record does not already show is dispatched — which is what
// makes a killed fan-out resumable without duplicating dispatches or
// settlements.
//
// The document must be the one the run was started from: exec.Resume compares
// opts.SpecHash against the recorded hash and refuses a mismatch with
// exec.ErrSpecHashMismatch. Parameters come from the run's own record, not from
// opts: templates already rendered against them, and a run resumed under other
// values would be a different run with the same id. opts.RunID is ignored for the
// same reason — the id is the identity of the run being resumed.
func Resume(ctx context.Context, opts Options, runID string) (Result, error) {
	if opts.Graph == nil {
		return Result{}, errors.New("workflow/host: nil graph")
	}
	if opts.Runner == nil {
		return Result{}, errors.New("workflow/host: no sub-agent runner configured")
	}
	if opts.Store == nil {
		return Result{}, errors.New("workflow/host: resume needs a run store; no run state is readable without one")
	}
	cp, err := opts.Store.LoadCheckpoint(ctx, runID)
	if err != nil {
		return Result{}, err
	}
	rt := NewRuntime(opts.Runner)
	rt.Retained = opts.Retained
	rt.Clock = opts.Now
	rt.Wait = opts.Wait
	cfg := exec.Config{
		RunID:    runID,
		Limits:   exec.LimitsFromBudgets(opts.Graph.Budgets),
		SpecHash: opts.SpecHash,
		SpecPath: opts.SpecPath,
		Store:    opts.Store,
	}
	run, xerr := exec.Resume(opts.Graph, rt, cfg, cp)
	if xerr != nil {
		return Result{}, xerr
	}
	res, xerr := run.Execute(ctx)
	out := Result{
		Run:     res,
		Runtime: rt,
	}
	if xerr != nil {
		return out, xerr
	}
	return finish(opts, out)
}

// finish appends the door-side facts a resumed run shares with a fresh one: the
// declines the lane could not honor and the artifacts the run declared. Writing
// artifacts again on a resume is idempotent by construction — the file is
// rewritten from the value the run recorded — so a partially written artifact set
// is repaired rather than duplicated.
func finish(opts Options, out Result) (Result, error) {
	// A node declined at the door for a seed binding, a proposal lane, or an
	// unrenderable prompt is already reported as a runtime_error on the node;
	// the warnings below cover what ran but did not get everything it asked
	// for.
	out.Warnings = append(out.Warnings, declinedNodes(opts.Graph, out.Run)...)
	arts, warns, werr := writeArtifacts(opts.ArtifactRoot, out.Run)
	out.Artifacts = arts
	out.Warnings = append(out.Warnings, warns...)
	if werr != nil {
		return out, werr
	}
	return out, nil
}

// declinedNodes names the nodes the document asked this lane to run that carry
// a binding .12 does not honor yet. A node that failed at the door already has
// its reason on its state; this covers the silent-degradation cases: a world,
// grants, or a network posture a shared report dispatch cannot provide.
func declinedNodes(g *ir.Graph, res exec.Result) []string {
	byPath := map[string]*exec.NodeState{}
	for _, n := range res.Nodes {
		byPath[n.Path] = n
	}
	var out []string
	for _, n := range g.Steps {
		if n == nil || n.Kind != ir.KindDelegate {
			continue
		}
		st := byPath[n.Path]
		if st == nil || st.State != exec.StateSucceeded {
			continue
		}
		if f := seedDeclined(n); f != "" {
			out = append(out, fmt.Sprintf("%s: %s needs the retained lane and the settlement verbs (yaah-0u0v.13); the node ran without it", n.Path, f))
		}
		var unenforced []string
		if n.World != "" {
			unenforced = append(unenforced, "world: "+n.World)
		}
		if n.Network != "" {
			unenforced = append(unenforced, "network: "+string(n.Network))
		}
		if len(n.Grants) > 0 {
			unenforced = append(unenforced, fmt.Sprintf("%d grant(s)", len(n.Grants)))
		}
		if len(unenforced) > 0 {
			out = append(out, fmt.Sprintf("%s: ran on the shared report lane, so %s could not be enforced",
				n.Path, strings.Join(unenforced, ", ")))
		}
	}
	return out
}

// writeArtifacts writes one file per succeeded emit node that declared an
// artifact path, and nothing else. The restriction is the point: the paths come
// from the resolved document, are resolved lexically under root, and must still
// name a path inside it after cleaning, so a declared `../../etc/passwd` is a
// refusal and not a write. An empty root declines every write and reports it —
// silently skipping an artifact the author declared is the failure mode this
// package exists to avoid.
func writeArtifacts(root string, res exec.Result) ([]Artifact, []string, error) {
	succeeded := map[string]bool{}
	for _, n := range res.Nodes {
		succeeded[n.Path] = n.State == exec.StateSucceeded
	}
	var (
		arts  []Artifact
		warns []string
		first error
	)
	for _, o := range res.Outputs {
		if o.Artifact == "" || !succeeded[o.NodePath] {
			continue
		}
		if root == "" {
			warns = append(warns, fmt.Sprintf(
				"%s: artifact %s not written — the run has no artifact root configured", o.NodePath, o.Artifact))
			continue
		}
		target, err := resolveArtifact(root, o.Artifact)
		if err != nil {
			if first == nil {
				first = err
			}
			warns = append(warns, fmt.Sprintf("%s: %v", o.NodePath, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := os.WriteFile(target, []byte(o.Value), 0o644); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		arts = append(arts, Artifact{
			NodePath: o.NodePath,
			Name:     o.Name,
			Declared: o.Artifact,
			Path:     target,
			Bytes:    len(o.Value),
		})
	}
	return arts, warns, first
}

// resolveArtifact turns a declared world-relative path into a host path under
// root, refusing absolute paths, an empty path, and anything that resolves
// outside root. Symlinks are not followed to an outside target by name here:
// the run writes at the cleaned path it was given, and the artifact vocabulary
// is a document-authoring surface, not a sandbox.
func resolveArtifact(root, declared string) (string, error) {
	if declared == "" {
		return "", errors.New("artifact path is empty")
	}
	if filepath.IsAbs(declared) || strings.HasPrefix(declared, "/") || len(declared) > 1 && declared[1] == ':' {
		return "", fmt.Errorf("artifact path %q is absolute; a workflow declares world-relative paths", declared)
	}
	base := filepath.Clean(root)
	target := filepath.Join(base, filepath.FromSlash(declared))
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q escapes the artifact root %s", declared, root)
	}
	return target, nil
}
