package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// supervised_session.go implements the supervised review session: an
// interactive checkpoint/review/verdict loop between the orchestrating
// agent and one sub-agent, built on shepherd workspace checkpoints and
// backend-neutral workspace states.
//
// Lifecycle (each orchestrator tool call blocks through exactly one
// unit):
//
//	start (supervised_task review:true) → unit 1 → review envelope
//	  ├─ continue  → accept unit, next unit with guidance
//	  ├─ rollback  → restore unit-start checkpoint (files + conversation),
//	  │              next unit with a more specific prompt
//	  ├─ fork      → restore checkpoint, run two prompt variants from the
//	  │              same tree state, return both for comparison
//	  │    └─ choose → apply the winner's tree + conversation, resume review
//	  ├─ review_diff → re-fetch the current diff/report
//	  ├─ accept    → keep the work, close the session
//	  └─ abort     → rewind the unaccepted unit, close the session
//
// One review session may be open at a time (the blocking tools serialize
// orchestrator calls anyway); starting a second returns an error naming
// the active session so the model can abort it first.

// Session lifecycle states.
const (
	sessionReview      = "review"          // awaiting a verdict on the last unit
	sessionAwaitChoose = "awaiting_choose" // fork ran; awaiting choose(a|b)
	sessionClosed      = "closed"
)

// reviewDiffMaxLines bounds the unified diff included in review
// envelopes. The changed-file list is never truncated, so a truncated
// diff still tells the orchestrator what to re-inspect via git tools.
const reviewDiffMaxLines = 2000

// supervisedSessionRuntime carries the tool-level wiring a session needs
// to dispatch sub-agent units. It mirrors the relevant SupervisedTaskTool
// fields so sessions stay decoupled from the tool struct.
type supervisedSessionRuntime struct {
	Runner   jobs.TaskRunner
	RepoPath string

	// RootScopeID is the session's root scope (adoption plan C1): the
	// session's scope forks from it, which is what gives the merge gate
	// its parent and fork baseline — without a root scope there is no
	// gate, so review sessions refuse to start rather than run
	// ungated. Empty only on the warned degraded path (root scope
	// creation failed with tracing on).
	RootScopeID string

	// MergePolicy composes the session's own approve verdict in the
	// merge gate's reviewer (adoption plan C1). The interactive loop is
	// the review — every envelope carried the unit's diff — and accept
	// is its approve verdict; a policy composes on top of it (a deny
	// keeps today's behavior: checkpoint restore + the reason
	// injected). Nil in production wiring.
	MergePolicy func(proposal shepherd.MergeProposal) error

	// WorktreeRoot is the parent directory for variant worktrees. Empty
	// defaults to a "shepherd-worktrees" directory beside the repository.
	WorktreeRoot string

	// WorktreeBootstrap runs inside each variant worktree after creation. A git
	// worktree checks out tracked files only, so a repo whose build needs
	// gitignored artifacts (node_modules, build caches, .env) recreates them
	// here.
	WorktreeBootstrap string
}

// reviewVariant holds one fork branch's captured outcome (adoption plan
// C2): the variant ran in an isolated child scope that owns its
// worktree, and its workspace is sealed as a retained output — frozen
// against the fork baseline, backing the review UI, and settle-able
// exactly once. The sealed state outlives the worktree (git worktrees
// share the repository's object store), so the settlement owns the
// teardown: Merge on select/apply, Discard on discard.
type reviewVariant struct {
	// ScopeID is the variant's isolated child scope; "" when the
	// worktree or the fork itself failed before the scope existed.
	ScopeID string

	// Output is the sealed retained output; nil when the variant errored
	// before sealing (provisioning, seeding, bootstrap). The review UI
	// reads its Changes/Diff; choose settles it.
	Output *shepherd.RetainedOutput

	Conv    []types.Message
	Result  string
	RunErr  string
	Restore int
}

// supervisedSession is one interactive review session.
type supervisedSession struct {
	mu sync.Mutex

	id      string
	runtime supervisedSessionRuntime
	subBase jobs.SubAgentParams // role + clamped limits; SeedMessages set per dispatch
	timeout time.Duration

	scopeID string

	// checkpointID is the live unit-start checkpoint ("" when none —
	// e.g. mid-fork or after a cancelled consume). unitState is the workspace
	// state at unit start and is the diff base; unitSnap is the
	// checkpoint's conversation snapshot — the denial path's seed,
	// kept because a merge-gate denial discards the scope and the
	// checkpoint can no longer be restored through it.
	checkpointID string
	unitState    shepherdtrace.WorkspaceState
	unitSnap     []byte

	unit     int             // completed+dispatched unit counter
	lastConv []types.Message // conversation after the last dispatched unit

	state sessionState

	// fork state, set while awaiting choose (adoption plan C2): the
	// variants' shared conversation seed and the two sealed branches.
	// The tree needs no fork state — the variants ran in isolated
	// worktrees, and the parent tree sits at the fork point.
	forkConv []types.Message
	varA     *reviewVariant
	varB     *reviewVariant
}

type sessionState string

// --- Registry ---

var supervisedSessions = struct {
	mu       sync.Mutex
	sessions map[string]*supervisedSession
}{sessions: make(map[string]*supervisedSession)}

// resetReviewSessions clears the session registry (tests only).
func resetReviewSessions() {
	supervisedSessions.mu.Lock()
	defer supervisedSessions.mu.Unlock()
	supervisedSessions.sessions = make(map[string]*supervisedSession)
}

// openReviewSessionIDs returns the IDs of all non-closed sessions. State
// is read under each session's mutex, taken after releasing the registry
// lock so the registry→session lock order never inverts with the
// session→registry order used by accept/abort.
func openReviewSessionIDs() []string {
	supervisedSessions.mu.Lock()
	open := make([]*supervisedSession, 0, len(supervisedSessions.sessions))
	for _, s := range supervisedSessions.sessions {
		open = append(open, s)
	}
	supervisedSessions.mu.Unlock()

	var ids []string
	for _, s := range open {
		if sessionStateOf(s) != sessionClosed {
			ids = append(ids, s.id)
		}
	}
	return ids
}

// sessionStateOf reads a session's lifecycle state under its mutex.
func sessionStateOf(s *supervisedSession) sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func getReviewSession(id string) (*supervisedSession, error) {
	supervisedSessions.mu.Lock()
	s, ok := supervisedSessions.sessions[id]
	supervisedSessions.mu.Unlock()
	if !ok || sessionStateOf(s) == sessionClosed {
		return nil, fmt.Errorf("supervisor: review session %q not found (it may be closed — start one with supervised_task review:true)", id)
	}
	return s, nil
}

func closeReviewSession(s *supervisedSession) {
	supervisedSessions.mu.Lock()
	defer supervisedSessions.mu.Unlock()
	s.state = sessionClosed
	delete(supervisedSessions.sessions, s.id)
}

// --- Envelope ---

// reviewVariantOut is the JSON projection of a fork variant.
type reviewVariantOut struct {
	Result string   `json:"result,omitempty"`
	Diff   string   `json:"diff,omitempty"`
	Files  []string `json:"files,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// reviewEnvelope is the uniform JSON result for every supervised review
// interaction. Status is one of: review (unit done — verdict needed),
// empty (unit produced no output — verdict needed), cancelled (parent
// cancelled; session stays resumable), awaiting_choose (fork ran — pick
// a winner), chosen (winner applied — verdict on it next), accepted
// (session closed, work kept), aborted (session closed, work rewound).
type reviewEnvelope struct {
	Status    string                      `json:"status"`
	SessionID string                      `json:"session_id"`
	Unit      int                         `json:"unit"`
	Result    string                      `json:"result,omitempty"`
	Diff      string                      `json:"diff,omitempty"`
	Files     []string                    `json:"files,omitempty"`
	Error     string                      `json:"error,omitempty"`
	Restores  int                         `json:"restores,omitempty"`
	Variants  map[string]reviewVariantOut `json:"variants,omitempty"`
	Next      []string                    `json:"next,omitempty"`
}

func nextActionsFor(state sessionState) []string {
	if state == sessionAwaitChoose {
		return []string{"choose", "review_diff", "abort"}
	}
	return []string{"continue", "rollback", "fork", "review_diff", "accept", "abort"}
}

func marshalReviewEnvelope(env reviewEnvelope) string {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Sprintf(`{"status":%q,"session_id":%q}`, env.Status, env.SessionID)
	}
	return string(data)
}

// --- Message snapshot helpers ---

func marshalMessages(msgs []types.Message) []byte {
	if len(msgs) == 0 {
		return nil
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil
	}
	return data
}

func unmarshalMessages(data []byte) []types.Message {
	if len(data) == 0 {
		return nil
	}
	var msgs []types.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil
	}
	return msgs
}

// --- Session operations ---

// startReviewSession creates a session, takes the unit-start checkpoint,
// dispatches the first unit, and returns the review envelope.
func startReviewSession(ctx context.Context, runtime supervisedSessionRuntime, prompt, role string, subParams jobs.SubAgentParams, timeout time.Duration) (string, error) {
	if open := openReviewSessionIDs(); len(open) > 0 {
		return "", fmt.Errorf("supervised_task: a review session is already open (%s) — continue/rollback/accept/abort it before starting another", open[0])
	}

	mgr := SharedScopeManager
	if mgr == nil {
		return "", fmt.Errorf("supervised_task: shepherd tracing not enabled (set shepherd_trace_dir in config)")
	}
	if runtime.RootScopeID == "" {
		return "", fmt.Errorf("supervised_task: no shepherd root scope — review sessions fork their scope from it (the merge gate needs its parent and baseline); see the tracing startup warning")
	}

	id := fmt.Sprintf("supervised:%s:%d", role, time.Now().UnixNano())
	// The session scope forks from the root (adoption plan C1), sharing
	// the root's in-place git sandbox: the fork gives the scope a parent
	// and a baseline, which is what the accept path's merge gate
	// proposes and merges against.
	scope, err := mgr.Fork(runtime.RootScopeID, id, nil)
	if err != nil {
		return "", fmt.Errorf("supervised_task: fork session scope: %w", err)
	}

	s := &supervisedSession{
		id:      id,
		runtime: runtime,
		subBase: subParams,
		timeout: timeout,
		scopeID: scope.ID(),
		state:   sessionReview,
	}
	supervisedSessions.mu.Lock()
	supervisedSessions.sessions[id] = s
	supervisedSessions.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Unit-start checkpoint for unit 1: no seed conversation yet, so the
	// snapshot is empty.
	// Session checkpoints are recovery points: they must exist even when
	// the caller's context is already cancelled (a cancelled unit is exactly
	// the case the checkpoint exists for), so their creation runs
	// cancel-free.
	cp, err := mgr.CreateCheckpoint(context.WithoutCancel(ctx), s.scopeID, nil)
	if err != nil {
		closeReviewSession(s)
		return "", fmt.Errorf("supervised_task: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitState = cp.Workspace
	s.unitSnap = cp.Snapshot

	return s.dispatchLocked(ctx, prompt, nil)
}

// dispatchLocked runs one work unit and returns the review envelope.
// seed is the conversation to continue from (nil = fresh). The session
// mutex must be held; the runner call releases it and re-acquires it.
func (s *supervisedSession) dispatchLocked(ctx context.Context, prompt string, seed []types.Message) (string, error) {
	s.unit++

	result, captured, restores, runErr := s.runUnitLocked(ctx, prompt, seed)

	s.lastConv = captured

	env := reviewEnvelope{
		SessionID: s.id,
		Unit:      s.unit,
		Restores:  restores,
	}
	if runErr != nil {
		env.Error = runErr.Error()
	}
	if trimmed := trimResult(result); trimmed != "" {
		env.Result = trimmed
	}

	switch {
	case ctx.Err() != nil:
		env.Status = "cancelled"
	default:
		if trimmed := trimResult(result); trimmed == "" && runErr == nil {
			env.Status = "empty"
		} else {
			env.Status = "review"
		}
	}

	if scope, ok := SharedScopeManager.Get(s.scopeID); ok {
		if diff, files, err := scope.DiffWorkspace(ctx, s.unitState, reviewDiffMaxLines); err == nil {
			env.Diff = diff
			env.Files = files
		} else if env.Error == "" {
			env.Error = "diff unavailable: " + err.Error()
		}
	}

	env.Next = nextActionsFor(s.state)
	return marshalReviewEnvelope(env), nil
}

// runUnitLocked invokes the sub-agent runner. The session mutex is held
// for the duration of the call: a concurrent verdict action blocks on the
// mutex instead of rewinding a workspace under active modification.
func (s *supervisedSession) runUnitLocked(ctx context.Context, prompt string, seed []types.Message) (result string, captured []types.Message, restores int, runErr error) {
	var restoreStats jobs.TurnRestoreStats
	runCtx := jobs.WithConversationCapture(ctx, &captured)
	runCtx = jobs.WithTurnRestoreStats(runCtx, &restoreStats)

	var cancel context.CancelFunc
	if s.timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, s.timeout)
	}

	subParams := s.subBase
	subParams.SeedMessages = seed
	result, runErr = s.runtime.Runner(runCtx, prompt, subParams)
	if cancel != nil {
		cancel()
	}
	return result, captured, restoreStats.Restores, runErr
}

// continueUnit accepts the last unit's work and dispatches the next one
// with the orchestrator's guidance.
func (s *supervisedSession) continueUnit(ctx context.Context, guidance string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionReview {
		return "", fmt.Errorf("supervisor: session %s is %s — only a session awaiting a verdict can continue", s.id, s.state)
	}

	mgr := SharedScopeManager
	if s.checkpointID != "" {
		mgr.PruneCheckpoints(context.WithoutCancel(ctx), s.scopeID)
		s.checkpointID = ""
	}

	prompt := "SUPERVISOR REVIEW: your previous work unit was accepted. Proceed with the next unit.\n\nGuidance:\n" + guidance

	cp, err := mgr.CreateCheckpoint(context.WithoutCancel(ctx), s.scopeID, marshalMessages(s.lastConv))
	if err != nil {
		return "", fmt.Errorf("supervisor: continue: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitState = cp.Workspace
	s.unitSnap = cp.Snapshot

	return s.dispatchLocked(ctx, prompt, s.lastConv)
}

// rollbackUnit restores the unit-start checkpoint (files AND
// conversation) and dispatches a corrected unit built from the
// orchestrator's more specific prompt.
func (s *supervisedSession) rollbackUnit(ctx context.Context, guidance string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionReview {
		return "", fmt.Errorf("supervisor: session %s is %s — only a session awaiting a verdict can roll back", s.id, s.state)
	}
	if guidance == "" {
		return "", fmt.Errorf("supervisor: rollback requires guidance (the more specific prompt for the corrected attempt)")
	}

	mgr := SharedScopeManager
	if s.checkpointID == "" {
		return "", fmt.Errorf("supervisor: session %s has no live checkpoint to roll back to", s.id)
	}

	snap, err := mgr.RestoreCheckpoint(ctx, s.checkpointID)
	if err != nil {
		return "", fmt.Errorf("supervisor: rollback: restore: %w", err)
	}
	s.checkpointID = ""
	seed := unmarshalMessages(snap)

	prompt := "SUPERVISOR CORRECTION: your previous work unit was rejected and its changes were rolled back. Follow the revised, more specific instructions below.\n\n" + guidance

	cp, err := mgr.CreateCheckpoint(context.WithoutCancel(ctx), s.scopeID, marshalMessages(seed))
	if err != nil {
		return "", fmt.Errorf("supervisor: rollback: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitState = cp.Workspace
	s.unitSnap = cp.Snapshot

	return s.dispatchLocked(ctx, prompt, seed)
}

// reviewDiff re-fetches the current diff and report without running
// anything.
func (s *supervisedSession) reviewDiff(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.state {
	case sessionReview:
		env := reviewEnvelope{
			Status:    "review_diff",
			SessionID: s.id,
			Unit:      s.unit,
			Result:    lastUnitResult(s),
			Next:      nextActionsFor(s.state),
		}
		if scope, ok := SharedScopeManager.Get(s.scopeID); ok {
			if diff, files, err := scope.DiffWorkspace(ctx, s.unitState, reviewDiffMaxLines); err == nil {
				env.Diff = diff
				env.Files = files
			} else {
				env.Error = "diff unavailable: " + err.Error()
			}
		}
		return marshalReviewEnvelope(env), nil
	case sessionAwaitChoose:
		return marshalReviewEnvelope(reviewEnvelope{
			Status:    "review_diff",
			SessionID: s.id,
			Unit:      s.unit,
			Variants: map[string]reviewVariantOut{
				"a": variantOut(s.varA),
				"b": variantOut(s.varB),
			},
			Next: nextActionsFor(s.state),
		}), nil
	default:
		return "", fmt.Errorf("supervisor: session %s is %s", s.id, s.state)
	}
}

func variantOut(v *reviewVariant) reviewVariantOut {
	if v == nil {
		return reviewVariantOut{}
	}
	out := reviewVariantOut{Result: trimResult(v.Result), Error: v.RunErr}
	if v.Output != nil {
		// The sealed output backs the review UI (adoption plan C2):
		// frozen at seal time, no re-capture of trees.
		out.Diff = v.Output.Diff()
		out.Files = changesPaths(v.Output.Changes())
	}
	return out
}

// forkVariants rewinds to the unit-start checkpoint and runs two prompt
// variants from that exact state (adoption plan C2): each variant is an
// isolated child scope owning its own worktree, sealed as a retained
// output whose frozen Changes/Diff back the review envelope. The parent
// tree is never touched — it sits at the fork point — and choose
// settles the winner on kernel mechanics.
func (s *supervisedSession) forkVariants(ctx context.Context, promptA, promptB string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionReview {
		return "", fmt.Errorf("supervisor: session %s is %s — fork needs a session awaiting a verdict", s.id, s.state)
	}
	if promptA == "" || promptB == "" {
		return "", fmt.Errorf("supervisor: fork requires prompt_a and prompt_b")
	}
	if s.checkpointID == "" {
		return "", fmt.Errorf("supervisor: session %s has no live checkpoint to fork from", s.id)
	}

	mgr := SharedScopeManager
	scope, ok := mgr.Get(s.scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", s.scopeID)
	}

	// Rewind to the unit start; the checkpoint is consumed by design.
	snap, err := mgr.RestoreCheckpoint(ctx, s.checkpointID)
	if err != nil {
		return "", fmt.Errorf("supervisor: fork: restore: %w", err)
	}
	s.checkpointID = ""
	forkConv := unmarshalMessages(snap)

	// The fork point: the tree the variants fork from — the restored
	// unit-start state. Each variant's isolated child scope baselines
	// on it (captured by the fork), and its worktree is seeded with it
	// (a worktree checks out tracked files only).
	forkPoint, err := scope.CaptureWorkspace(ctx)
	if err != nil {
		return "", fmt.Errorf("supervisor: fork: capture fork point: %w", err)
	}
	s.forkConv = forkConv

	env := reviewEnvelope{
		Status:    "awaiting_choose",
		SessionID: s.id,
		Variants:  map[string]reviewVariantOut{},
		Next:      nextActionsFor(sessionAwaitChoose),
	}

	variants := []struct {
		label  string
		prompt string
		slot   **reviewVariant
	}{
		{"a", promptA, &s.varA},
		{"b", promptB, &s.varB},
	}

	for i, v := range variants {
		if ctx.Err() != nil {
			// Parent cancelled between variants: the parent tree sits at
			// the fork point untouched (the variants ran in isolated
			// worktrees); the started branches are torn down and the
			// session stays resumable.
			if derr := s.discardVariantLocked(context.WithoutCancel(ctx), s.varA); derr != nil && env.Error == "" {
				env.Error = "variant a teardown: " + derr.Error()
			}
			if derr := s.discardVariantLocked(context.WithoutCancel(ctx), s.varB); derr != nil && env.Error == "" {
				env.Error = "variant b teardown: " + derr.Error()
			}
			s.varA, s.varB = nil, nil
			env.Status = "cancelled"
			env.Unit = s.unit
			if env.Error == "" {
				env.Error = ctx.Err().Error()
			}
			env.Next = nextActionsFor(s.state)
			return marshalReviewEnvelope(env), nil
		}

		result := s.runIsolatedVariant(ctx, i, v.label, v.prompt, forkConv, forkPoint)

		*v.slot = result
		env.Variants[v.label] = variantOut(result)
		env.Restores += result.Restore
		if result.RunErr != "" && env.Error == "" {
			env.Error = fmt.Sprintf("variant %s: %s", v.label, result.RunErr)
		}
	}

	s.state = sessionAwaitChoose
	env.Unit = s.unit
	env.Next = nextActionsFor(s.state)
	return marshalReviewEnvelope(env), nil
}

// discardVariantLocked ends one fork branch (adoption plan C2): a
// sealed variant is settled with SettleDiscard — consume-once, and the
// kernel's discard destroys the worktree the variant scope owns; an
// unsealed variant (it errored before sealing) is discarded through
// its scope directly. Best-effort by design: the caller decides how a
// teardown failure is reported.
func (s *supervisedSession) discardVariantLocked(ctx context.Context, v *reviewVariant) error {
	if v == nil {
		return nil
	}
	mgr := SharedScopeManager
	if v.Output != nil {
		if err := v.Output.Settle(ctx, shepherd.SettleDiscard); err != nil && !errors.Is(err, shepherd.ErrOutputConsumed) {
			return err
		}
		return nil
	}
	if v.ScopeID == "" {
		return nil
	}
	scope, ok := mgr.Get(v.ScopeID)
	if !ok {
		return nil
	}
	parent, ok := mgr.Get(s.scopeID)
	if !ok {
		return fmt.Errorf("session scope %s not found to discard variant %s from", s.scopeID, v.ScopeID)
	}
	return parent.Discard(scope)
}

// runIsolatedVariant creates a worktree for one variant, seeds it with the fork
// state, runs the variant inside it, and captures the outcome.
//
// The worktree is always removed, including on error: the captured workspace
// state survives in the repository's shared object store, so the winner can
// still be applied after teardown. The sub-agent receives the worktree as its
// Workdir, which confines its file tools and shell to the checkout.
// runIsolatedVariant provisions one fork branch (adoption plan C2): a
// worktree owned by an isolated child scope of the session scope,
// seeded with the fork point, bootstrapped, run, and sealed as a
// retained output. The worktree is NOT torn down here — the settlement
// owns the teardown (Merge on select/apply, Discard on discard), and
// the sealed state outlives it through the repository's shared object
// store. A variant that fails before sealing carries its error instead;
// the fork continues so the healthy branch stays choosable.
func (s *supervisedSession) runIsolatedVariant(
	ctx context.Context,
	index int,
	label, prompt string,
	forkConv []types.Message,
	forkPoint shepherdtrace.WorkspaceState,
) *reviewVariant {
	v := &reviewVariant{}
	wtPath := s.worktreePath(index, label)
	sb := shepherdsandbox.NewWorktreeSandbox(s.runtime.RepoPath, wtPath)

	if err := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); err != nil {
		// A worktree left behind by a killed run can occupy the path. Only
		// clear it when it is actually a worktree registered with this
		// repository: the worktree root is user-configurable, the occupant
		// may be unrelated data, and Destroy falls back to RemoveAll.
		if isRegisteredWorktree(ctx, s.runtime.RepoPath, wtPath) {
			_ = sb.Destroy(context.WithoutCancel(ctx))
		}
		if retryErr := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); retryErr != nil {
			v.RunErr = fmt.Sprintf("create worktree %s: %v (first attempt: %v)", wtPath, retryErr, err)
			return v
		}
	}

	// From here the worktree belongs to the variant scope: the
	// settlement owns its teardown, so the scope must exist before
	// anything can strand the worktree. A fork failure destroys it
	// here — no scope, no owner.
	mgr := SharedScopeManager
	scope, err := mgr.ForkIsolated(s.scopeID, fmt.Sprintf("%s:var-%s", s.id, label), nil, sb)
	if err != nil {
		_ = sb.Destroy(context.WithoutCancel(ctx))
		v.RunErr = "fork variant scope: " + err.Error()
		return v
	}
	v.ScopeID = scope.ID()

	// Seed the checkout with the parent's exact state at the fork point
	// before bootstrapping. The bootstrap installs from tracked lockfiles
	// that Apply restores, so running it against HEAD would provision the
	// wrong revision, and Apply would then overwrite the gitignored
	// artifacts bootstrap created.
	if err := sb.Apply(ctx, forkPoint); err != nil {
		v.RunErr = "seed fork state: " + err.Error()
		return v
	}

	if s.runtime.WorktreeBootstrap != "" {
		if err := runWorktreeBootstrap(ctx, wtPath, s.runtime.WorktreeBootstrap); err != nil {
			v.RunErr = "bootstrap: " + err.Error()
			return v
		}
	}

	s.runVariantPrompt(ctx, v, wtPath, prompt, forkConv)

	// Seal the variant's workspace against the fork baseline (the
	// scope's own): the frozen Changes/Diff back the review envelope,
	// and settle consumes the output exactly once. Sealing happens even
	// after a run error — the partial work is reviewable.
	out, err := mgr.Seal(ctx, scope.ID())
	if err != nil {
		if v.RunErr == "" {
			v.RunErr = "seal variant: " + err.Error()
		}
		return v
	}
	v.Output = out
	return v
}

// isRegisteredWorktree reports whether path is currently registered as a git
// worktree of repoPath. A worktree left behind by a killed run still appears
// in `git worktree list`; unrelated data in a user-configured worktree root
// does not, which is what makes clearing it safe.
func isRegisteredWorktree(ctx context.Context, repoPath, path string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		wt, err := filepath.Abs(strings.TrimPrefix(line, "worktree "))
		if err != nil {
			continue
		}
		// Windows paths differ in case; a lexical compare there would miss
		// the registered entry and skip a legitimate cleanup.
		if runtime.GOOS == "windows" {
			if strings.EqualFold(filepath.Clean(wt), filepath.Clean(abs)) {
				return true
			}
			continue
		}
		if filepath.Clean(wt) == filepath.Clean(abs) {
			return true
		}
	}
	return false
}

// worktreePath returns a deterministic worktree path for a variant. The session
// id is sanitized because it contains a colon and a nanosecond timestamp.
func (s *supervisedSession) worktreePath(index int, label string) string {
	root := s.runtime.WorktreeRoot
	if root == "" {
		root = filepath.Join(filepath.Dir(s.runtime.RepoPath), "shepherd-worktrees")
	}
	// A configured or derived root may be relative (RepoPath comes straight
	// from config); git worktree add and the sub-agent's cwd need absolute.
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	safe := strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(s.id)
	return filepath.Join(root, fmt.Sprintf("%s-%d-%s", safe, index, label))
}

// runWorktreeBootstrap runs the configured bootstrap command inside a fresh
// variant worktree. A worktree contains tracked files only, so this is where a
// repo recreates gitignored build inputs.
func runWorktreeBootstrap(ctx context.Context, dir, command string) error {
	shell, shellArg := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, shellArg = "pwsh", "-Command"
		if _, err := exec.LookPath("pwsh"); err != nil {
			shell, shellArg = "powershell", "-Command"
		}
	}
	cmd := exec.CommandContext(ctx, shell, shellArg, command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runVariantIn dispatches one fork variant against a workspace and captures its
// state, diff, and conversation. It must be called with s.mu held; the lock is
// held across the runner call so concurrent verdicts serialize.
// runVariantPrompt dispatches one fork variant's prompt in workdir and
// fills the variant's conversation, result, and restore count. It must
// be called with s.mu held; the lock is held across the runner call so
// concurrent verdicts serialize. The workspace side (capture, diff,
// freeze) is Seal's — this is the run.
func (s *supervisedSession) runVariantPrompt(ctx context.Context, v *reviewVariant, workdir, prompt string, forkConv []types.Message) {
	// Seed with the fork-point conversation only. The prompt is passed
	// as the runner's user input and appended by the loop's
	// initMessages — appending it here too would duplicate it.
	seed := append([]types.Message(nil), forkConv...)

	var captured []types.Message
	var restoreStats jobs.TurnRestoreStats
	runCtx := jobs.WithConversationCapture(ctx, &captured)
	runCtx = jobs.WithTurnRestoreStats(runCtx, &restoreStats)
	var cancel context.CancelFunc
	if s.timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, s.timeout)
	}
	subParams := s.subBase
	subParams.SeedMessages = seed
	subParams.Workdir = workdir
	result, runErr := s.runtime.Runner(runCtx, prompt, subParams)
	if cancel != nil {
		cancel()
	}

	v.Conv = captured
	v.Result = result
	v.Restore = restoreStats.Restores
	if runErr != nil {
		v.RunErr = runErr.Error()
	}
}

// chooseVariant settles the winning fork branch on kernel mechanics
// (adoption plan C2) and returns the session to the review state. The
// winner's sealed output is settled — Select when the parent tree is
// still at the fork baseline (the common case; the variants ran in
// isolated worktrees, so the parent never moved), Apply on
// path-disjoint parent drift — and the causal merge is the winner's
// worktree teardown. The loser is settled with SettleDiscard, which
// destroys its worktree. The kernel's consume-once contract backs the
// already-chose guard; an apply conflict refuses without consuming, so
// the session stays awaiting-choose and the orchestrator can retry.
func (s *supervisedSession) chooseVariant(ctx context.Context, winner string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionAwaitChoose {
		return "", fmt.Errorf("supervisor: session %s is %s — choose needs a fork awaiting a decision", s.id, s.state)
	}
	var v, loser *reviewVariant
	switch winner {
	case "a":
		v, loser = s.varA, s.varB
	case "b":
		v, loser = s.varB, s.varA
	case "":
		return "", fmt.Errorf("supervisor: choose requires winner: \"a\" or \"b\"")
	default:
		return "", fmt.Errorf("supervisor: winner must be \"a\" or \"b\", got %q", winner)
	}
	if v == nil {
		return "", fmt.Errorf("supervisor: variant %q missing — fork did not complete", winner)
	}
	if v.Output == nil {
		return "", fmt.Errorf("supervisor: choose: variant %q has no sealed output (%s) — cannot settle it", winner, v.RunErr)
	}

	mgr := SharedScopeManager
	scope, ok := mgr.Get(s.scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", s.scopeID)
	}

	// The settlement action follows the parent (plan D4): Select needs
	// the parent bit-identical to the fork baseline; drifted means
	// Apply (three-way, path-disjoint — an overlap refuses below).
	// DiffWorkspace is a query: the same baseline the kernel settles
	// against, no capture record.
	action := shepherd.SettleSelect
	if _, parentFiles, err := scope.DiffWorkspace(ctx, v.Output.Baseline(), 0); err != nil {
		return "", fmt.Errorf("supervisor: choose: %w", err)
	} else if len(parentFiles) > 0 {
		action = shepherd.SettleApply
	}
	if err := mgr.Settle(ctx, v.Output.ID(), action); err != nil {
		if errors.Is(err, shepherd.ErrOutputConsumed) {
			return "", fmt.Errorf("supervisor: choose: this fork was already chosen — the sealed output is consumed")
		}
		if errors.Is(err, shepherd.ErrApplyConflict) {
			// The output stays unconsumed and the session stays
			// awaiting-choose: resolve the drift and choose again.
			return "", fmt.Errorf("supervisor: choose: the parent workspace changed paths the sealed output also changes (%s); resolve the overlap and choose again", err)
		}
		return "", fmt.Errorf("supervisor: choose: %w", err)
	}

	// The winner settled: its tree is the parent's and its worktree is
	// torn down by the merge. The loser is discarded — its worktree
	// teardown — best effort; a failure lands in the envelope, not a
	// half-chosen session.
	var loserErr error
	if derr := s.discardVariantLocked(ctx, loser); derr != nil {
		loserErr = derr
	}

	s.lastConv = v.Conv

	// Fresh unit-start checkpoint over the winner's state so the review
	// cycle (continue/rollback/fork) works on it immediately.
	cp, err := mgr.CreateCheckpoint(context.WithoutCancel(ctx), s.scopeID, marshalMessages(v.Conv))
	if err != nil {
		return "", fmt.Errorf("supervisor: choose: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitState = cp.Workspace
	s.unitSnap = cp.Snapshot

	// Fork state is consumed.
	s.forkConv = nil
	s.varA = nil
	s.varB = nil
	s.state = sessionReview

	env := reviewEnvelope{
		Status:    "chosen",
		SessionID: s.id,
		Unit:      s.unit,
		Result:    trimResult(v.Result),
		Diff:      v.Output.Diff(),
		Files:     changesPaths(v.Output.Changes()),
		Error:     v.RunErr,
		Restores:  v.Restore,
		Next:      nextActionsFor(s.state),
	}
	if loserErr != nil {
		env.Error = strings.TrimSpace("losing variant teardown: " + loserErr.Error() + "; " + env.Error)
	}
	return marshalReviewEnvelope(env), nil
}

// changesPaths projects a sealed output's frozen changes as the changed
// file list the envelopes carry.
func changesPaths(changes []shepherd.ProposedChange) []string {
	if len(changes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	return paths
}

// accept keeps the last unit's work through the kernel's merge gate
// (adoption plan C1): the session's approve verdict runs CommitMerge,
// which records the proposal and the decision causally and refuses
// scope states that cannot merge. The merge itself is causal — the
// work already stands in the shared tree — so approval lands as the
// existing post-accept flow (prune checkpoints, close). A denial keeps
// today's behavior: checkpoint restore + the denial reason injected.
func (s *supervisedSession) accept(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == sessionAwaitChoose {
		return "", fmt.Errorf("supervisor: session %s is awaiting choose — pick a winner or abort", s.id)
	}

	mgr := SharedScopeManager
	if err := mgr.CommitMerge(ctx, s.scopeID, s.mergeReviewer()); err != nil {
		var denied *shepherd.SupervisorDeniedError
		if errors.As(err, &denied) {
			// The gate joins its denial with a hard failure when the
			// decision recording or the discard itself fails, and
			// errors.As matches those too — but a denial without a
			// completed discard is not a denial the retry path can
			// build on (the scope is still active, and no decision
			// fact may exist). Only a scope that reached
			// ScopeDiscarded takes the denial path; anything else
			// surfaces the whole error.
			if scope, ok := mgr.Get(s.scopeID); ok && scope.State() == shepherd.ScopeDiscarded {
				return s.denyAndRetryLocked(ctx, denied)
			}
		}
		return "", fmt.Errorf("supervisor: accept: %w", err)
	}

	// Approval: the work stands, the decision is recorded. Existing
	// post-accept flow — the session closes over the kept work.
	mgr.PruneCheckpoints(context.WithoutCancel(ctx), s.scopeID)

	env := reviewEnvelope{
		Status:    "accepted",
		SessionID: s.id,
		Unit:      s.unit,
		Result:    lastUnitResult(s),
		Next:      []string{"(session closed)"},
	}
	closeReviewSession(s)
	return marshalReviewEnvelope(env), nil
}

// mergeReviewer is the session's verdict as the gate's MergeReviewer
// (adoption plan C1). The interactive loop is the review — every
// envelope carried the unit's diff — and accept is its approve verdict;
// the runtime's MergePolicy composes on top of it and is the one
// denial source (nil in production wiring, so the gate's refusal
// semantics — isolated child, no baseline, non-active scope — are the
// other ways accept can fail loudly).
func (s *supervisedSession) mergeReviewer() shepherd.MergeReviewer {
	return func(p shepherd.MergeProposal) error {
		if s.runtime.MergePolicy != nil {
			return s.runtime.MergePolicy(p)
		}
		return nil
	}
}

// denyAndRetryLocked maps a merge-gate denial onto the session's
// existing rollback UX (adoption plan C1): the kernel's Discard was
// causal-only — the session scope shares the root's sandbox, so the
// denied work still stands in the tree — and the physical rollback is
// ours, with the denial reason injected into the re-run the way
// rollback injects the orchestrator's guidance.
//
// The restore target is the proposal's own diff base — the session
// scope's fork baseline — because the gate's proposal is the
// cumulative session work: a denial covers every continued unit's
// changes, not just the last unit's. A unit-checkpoint restore would
// leave earlier units' denied work in the tree and baseline it into
// the retry, hiding it from the next review.
//
// The discarded scope cannot carry the restore (its workspace methods
// refuse non-active scopes), so the tree is reset through the root
// BEFORE the retry scope forks: the fresh fork's baseline is then the
// restored tree, and the next accept proposes only the re-done work.
// The conversation survives the rollback — it is context, not work
// product — so the retry re-does the work with every unit's report
// still in reach.
func (s *supervisedSession) denyAndRetryLocked(ctx context.Context, denied *shepherd.SupervisorDeniedError) (string, error) {
	mgr := SharedScopeManager
	scope, ok := mgr.Get(s.scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: accept denied (%s) but the session scope vanished — the tree was left as the unit left it; abort or roll back manually", denied.Reason)
	}
	base, hasBase := scope.Baseline()
	if !hasBase {
		// Unreachable in practice — the gate cannot propose without a
		// baseline — but a denial must never silently proceed without a
		// rollback target: fail loudly instead of lying to the agent.
		return "", fmt.Errorf("supervisor: accept denied (%s) but the session has no fork baseline to roll back to — the tree was left as the unit left it; abort or roll back manually", denied.Reason)
	}
	root, ok := mgr.Get(s.runtime.RootScopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: accept denied (%s) but the root scope vanished — the tree was left as the unit left it; abort or roll back manually", denied.Reason)
	}
	if err := root.ApplyWorkspace(ctx, base); err != nil {
		return "", fmt.Errorf("supervisor: accept denied (%s) but the rollback to the session start failed: %w — the tree was left as the unit left it; abort or roll back manually", denied.Reason, err)
	}
	if s.checkpointID != "" {
		mgr.PruneCheckpoints(context.WithoutCancel(ctx), s.scopeID)
	}
	// Straggler fork state is superseded by the retry (a cancelled fork
	// already tore its branches down; its experiments were never
	// chosen, and the denial rolled the tree past them).
	s.forkConv = nil
	s.varA = nil
	s.varB = nil

	scope, err := mgr.Fork(s.runtime.RootScopeID, fmt.Sprintf("%s:retry:%d", s.id, time.Now().UnixNano()), nil)
	if err != nil {
		return "", fmt.Errorf("supervisor: accept denied (%s) but the retry scope could not fork: %w", denied.Reason, err)
	}
	s.scopeID = scope.ID()
	s.checkpointID = ""
	seed := s.lastConv
	cp, err := mgr.CreateCheckpoint(context.WithoutCancel(ctx), s.scopeID, marshalMessages(seed))
	if err != nil {
		return "", fmt.Errorf("supervisor: accept denied (%s) but the retry checkpoint failed: %w", denied.Reason, err)
	}
	s.checkpointID = cp.ID
	s.unitState = cp.Workspace
	s.unitSnap = cp.Snapshot

	prompt := "SUPERVISOR CORRECTION: the merge gate denied the session's work and the changes were rolled back to the session start. The gate's reason:\n" +
		denied.Reason + "\n\nRedo the work addressing the denial so the next review passes the gate."
	out, err := s.dispatchLocked(ctx, prompt, seed)
	if err != nil {
		return "", err
	}
	// The retry is a normal review dispatch; surface the denial so the
	// orchestrator can see why the unit re-ran.
	var env reviewEnvelope
	if jerr := json.Unmarshal([]byte(out), &env); jerr == nil && env.Error == "" {
		env.Error = "accept denied by the merge gate: " + denied.Reason
		return marshalReviewEnvelope(env), nil
	}
	return out, nil
}

// abort rewinds the unaccepted work and closes the session (adoption
// plan C2): the last unit via checkpoint restore; an open fork via the
// settlement's discard — the branches' worktree teardown — because the
// variants ran in isolated worktrees and the parent tree sits at the
// fork point, needing no rewind.
func (s *supervisedSession) abort(ctx context.Context, restore bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mgr := SharedScopeManager
	var rewindErr error
	rewound := false

	switch {
	case s.state == sessionAwaitChoose:
		// The worktrees are session resources and must not outlive the
		// session, whatever restore says; the tree itself never moved.
		if err := s.discardVariantLocked(ctx, s.varA); err != nil {
			rewindErr = err
		}
		if err := s.discardVariantLocked(ctx, s.varB); err != nil && rewindErr == nil {
			rewindErr = err
		}
		s.varA, s.varB = nil, nil
		rewound = rewindErr == nil
	case s.checkpointID != "":
		if !restore {
			break
		}
		if _, err := mgr.RestoreCheckpoint(ctx, s.checkpointID); err != nil {
			rewindErr = err
		} else {
			rewound = true
		}
	default:
		// A cancelled fork left the session in review with its branches
		// already torn down; the tree sits at the last restore point —
		// nothing to rewind.
		rewound = true
	}

	mgr.PruneCheckpoints(context.WithoutCancel(ctx), s.scopeID)

	env := reviewEnvelope{
		Status:    "aborted",
		SessionID: s.id,
		Unit:      s.unit,
		Result:    lastUnitResult(s),
		Next:      []string{"(session closed)"},
	}
	if rewindErr != nil {
		env.Error = "abort: rewind failed: " + rewindErr.Error()
	} else if restore && rewound {
		env.Diff = "" // workspace rewound; nothing pending
	}
	closeReviewSession(s)
	return marshalReviewEnvelope(env), nil
}

// lastUnitResult returns a trimmed final report from the last captured
// conversation's assistant message, falling back to the stored seed.
func lastUnitResult(s *supervisedSession) string {
	for i := len(s.lastConv) - 1; i >= 0; i-- {
		if m := s.lastConv[i]; m.Role == "assistant" && trimResult(m.Content) != "" {
			return trimResult(m.Content)
		}
	}
	return ""
}

// trimResult normalizes a sub-agent report for envelopes: trims
// whitespace and caps length.
func trimResult(s string) string {
	const maxResult = 4000
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if len(s) > maxResult {
		return s[:maxResult] + "\n...[truncated]"
	}
	return s
}
