package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// supervised_session.go implements the supervised review session: an
// interactive checkpoint/review/verdict loop between the orchestrating
// agent and one sub-agent, built on shepherd git checkpoints and tree
// states.
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
}

// reviewVariant holds one fork branch's captured outcome.
type reviewVariant struct {
	Tree    *shepherd.TreeState
	Conv    []types.Message
	Result  string
	Diff    string
	Files   []string
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
	// e.g. mid-fork or after a cancelled consume). unitHead is the HEAD
	// SHA at unit start, used as the diff base.
	checkpointID string
	unitHead     string

	unit     int             // completed+dispatched unit counter
	lastConv []types.Message // conversation after the last dispatched unit

	state sessionState

	// fork state, set while awaiting choose.
	forkTree *shepherd.TreeState
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

	id := fmt.Sprintf("supervised:%s:%d", role, time.Now().UnixNano())
	scope, err := mgr.Create(id)
	if err != nil {
		return "", fmt.Errorf("supervised_task: create scope: %w", err)
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
	cp, err := mgr.CreateCheckpoint(s.scopeID, s.runtime.RepoPath, nil)
	if err != nil {
		closeReviewSession(s)
		return "", fmt.Errorf("supervised_task: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitHead = cp.HeadSHA

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

	if diff, files, err := shepherd.DiffSince(s.runtime.RepoPath, s.unitHead, reviewDiffMaxLines); err == nil {
		env.Diff = diff
		env.Files = files
	} else if env.Error == "" {
		env.Error = "diff unavailable: " + err.Error()
	}

	env.Next = nextActionsFor(s.state)
	return marshalReviewEnvelope(env), nil
}

// runUnitLocked invokes the sub-agent runner, releasing the session
// mutex for the (long) call. The mutex is re-acquired via defer so a
// runner panic cannot leave the session permanently unlocked.
func (s *supervisedSession) runUnitLocked(ctx context.Context, prompt string, seed []types.Message) (result string, captured []types.Message, restores int, runErr error) {
	var restoreStats jobs.TurnRestoreStats
	runCtx := jobs.WithConversationCapture(ctx, &captured)
	runCtx = jobs.WithTurnRestoreStats(runCtx, &restoreStats)

	var cancel context.CancelFunc
	if s.timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, s.timeout)
	}

	s.mu.Unlock()
	defer s.mu.Lock()

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
		mgr.PruneCheckpoints(s.scopeID)
		s.checkpointID = ""
	}

	prompt := "SUPERVISOR REVIEW: your previous work unit was accepted. Proceed with the next unit.\n\nGuidance:\n" + guidance

	cp, err := mgr.CreateCheckpoint(s.scopeID, s.runtime.RepoPath, marshalMessages(s.lastConv))
	if err != nil {
		return "", fmt.Errorf("supervisor: continue: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitHead = cp.HeadSHA

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

	snap, err := mgr.RestoreCheckpoint(s.checkpointID)
	if err != nil {
		return "", fmt.Errorf("supervisor: rollback: restore: %w", err)
	}
	s.checkpointID = ""
	seed := unmarshalMessages(snap)

	prompt := "SUPERVISOR CORRECTION: your previous work unit was rejected and its changes were rolled back. Follow the revised, more specific instructions below.\n\n" + guidance

	cp, err := mgr.CreateCheckpoint(s.scopeID, s.runtime.RepoPath, marshalMessages(seed))
	if err != nil {
		return "", fmt.Errorf("supervisor: rollback: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitHead = cp.HeadSHA

	return s.dispatchLocked(ctx, prompt, seed)
}

// reviewDiff re-fetches the current diff and report without running
// anything.
func (s *supervisedSession) reviewDiff() (string, error) {
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
		if diff, files, err := shepherd.DiffSince(s.runtime.RepoPath, s.unitHead, reviewDiffMaxLines); err == nil {
			env.Diff = diff
			env.Files = files
		} else {
			env.Error = "diff unavailable: " + err.Error()
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
	return reviewVariantOut{Result: trimResult(v.Result), Diff: v.Diff, Files: v.Files, Error: v.RunErr}
}

// forkVariants rewinds to the unit-start checkpoint and runs two prompt
// variants from that exact state, capturing each variant's tree so the
// winner can be re-applied by choose.
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
	snap, err := mgr.RestoreCheckpoint(s.checkpointID)
	if err != nil {
		return "", fmt.Errorf("supervisor: fork: restore: %w", err)
	}
	s.checkpointID = ""
	forkConv := unmarshalMessages(snap)

	forkTree, err := scope.CaptureTree(s.runtime.RepoPath)
	if err != nil {
		return "", fmt.Errorf("supervisor: fork: capture fork point: %w", err)
	}
	s.forkTree = forkTree
	s.forkConv = forkConv

	env := reviewEnvelope{
		Status:    "awaiting_choose",
		SessionID: s.id,
		Variants:  map[string]reviewVariantOut{},
		Next:      nextActionsFor(sessionAwaitChoose),
	}

	for _, v := range []struct {
		label  string
		prompt string
		slot   **reviewVariant
	}{
		{"a", promptA, &s.varA},
		{"b", promptB, &s.varB},
	} {
		if ctx.Err() != nil {
			// Parent cancelled between variants: rewind to the fork
			// point so the workspace is not left mid-experiment.
			if applyErr := scope.ApplyTree(s.runtime.RepoPath, forkTree); applyErr == nil {
				s.unitHead = forkTree.HeadSHA
			}
			env.Status = "cancelled"
			env.Unit = s.unit
			env.Error = ctx.Err().Error()
			env.Next = nextActionsFor(s.state)
			return marshalReviewEnvelope(env), nil
		}

		result := s.runVariant(ctx, scope, v.prompt, forkConv)
		*v.slot = result

		env.Variants[v.label] = variantOut(result)
		env.Restores += result.Restore
		if result.RunErr != "" && env.Error == "" {
			env.Error = fmt.Sprintf("variant %s: %s", v.label, result.RunErr)
		}

		// Reset the workspace to the fork point for the next variant
		// (or to leave a clean post-fork state after variant B).
		if err := scope.ApplyTree(s.runtime.RepoPath, forkTree); err != nil {
			return "", fmt.Errorf("supervisor: fork: reset to fork point after variant %s: %w", v.label, err)
		}
	}

	s.state = sessionAwaitChoose
	env.Unit = s.unit
	env.Next = nextActionsFor(s.state)
	return marshalReviewEnvelope(env), nil
}

// runVariant dispatches one fork variant and captures its tree state,
// diff, and conversation. It must be called with s.mu held; the lock is
// released around the runner call and re-acquired before returning.
func (s *supervisedSession) runVariant(ctx context.Context, scope *shepherd.Scope, prompt string, forkConv []types.Message) *reviewVariant {
	s.mu.Unlock()
	defer s.mu.Lock()

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
	result, runErr := s.runtime.Runner(runCtx, prompt, subParams)
	if cancel != nil {
		cancel()
	}

	v := &reviewVariant{
		Conv:    captured,
		Result:  result,
		Restore: restoreStats.Restores,
	}
	if runErr != nil {
		v.RunErr = runErr.Error()
	}
	if tree, err := scope.CaptureTree(s.runtime.RepoPath); err == nil {
		v.Tree = tree
	}
	if diff, files, err := shepherd.DiffSince(s.runtime.RepoPath, s.forkTree.HeadSHA, reviewDiffMaxLines); err == nil {
		v.Diff = diff
		v.Files = files
	}
	return v
}

// chooseVariant applies the winning fork variant's tree and
// conversation, takes a fresh unit-start checkpoint, and returns the
// session to the review state. The losing variant is discarded.
func (s *supervisedSession) chooseVariant(winner string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionAwaitChoose {
		return "", fmt.Errorf("supervisor: session %s is %s — choose needs a fork awaiting a decision", s.id, s.state)
	}
	var v *reviewVariant
	switch winner {
	case "a":
		v = s.varA
	case "b":
		v = s.varB
	case "":
		return "", fmt.Errorf("supervisor: choose requires winner: \"a\" or \"b\"")
	default:
		return "", fmt.Errorf("supervisor: winner must be \"a\" or \"b\", got %q", winner)
	}
	if v == nil {
		return "", fmt.Errorf("supervisor: variant %q missing — fork did not complete", winner)
	}

	mgr := SharedScopeManager
	scope, ok := mgr.Get(s.scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", s.scopeID)
	}

	if err := scope.ApplyTree(s.runtime.RepoPath, v.Tree); err != nil {
		return "", fmt.Errorf("supervisor: choose: apply winner tree: %w", err)
	}
	s.lastConv = v.Conv

	// Fresh unit-start checkpoint over the winner's state so the review
	// cycle (continue/rollback/fork) works on it immediately.
	cp, err := mgr.CreateCheckpoint(s.scopeID, s.runtime.RepoPath, marshalMessages(v.Conv))
	if err != nil {
		return "", fmt.Errorf("supervisor: choose: checkpoint: %w", err)
	}
	s.checkpointID = cp.ID
	s.unitHead = cp.HeadSHA

	// Fork state is consumed.
	s.forkTree = nil
	s.forkConv = nil
	s.varA = nil
	s.varB = nil
	s.state = sessionReview

	env := reviewEnvelope{
		Status:    "chosen",
		SessionID: s.id,
		Unit:      s.unit,
		Result:    trimResult(v.Result),
		Diff:      v.Diff,
		Files:     v.Files,
		Error:     v.RunErr,
		Restores:  v.Restore,
		Next:      nextActionsFor(s.state),
	}
	return marshalReviewEnvelope(env), nil
}

// accept keeps the last unit's work, releases the checkpoint, and
// closes the session.
func (s *supervisedSession) accept() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == sessionAwaitChoose {
		return "", fmt.Errorf("supervisor: session %s is awaiting choose — pick a winner or abort", s.id)
	}

	SharedScopeManager.PruneCheckpoints(s.scopeID)

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

// abort rewinds the unaccepted work (last unit via checkpoint, or the
// whole fork via the fork tree) and closes the session.
func (s *supervisedSession) abort(restore bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mgr := SharedScopeManager
	var rewindErr error
	rewound := false

	if restore {
		switch {
		case s.state == sessionAwaitChoose:
			if scope, ok := mgr.Get(s.scopeID); ok && s.forkTree != nil {
				if err := scope.ApplyTree(s.runtime.RepoPath, s.forkTree); err != nil {
					rewindErr = err
				} else {
					rewound = true
				}
			}
		case s.checkpointID != "":
			if _, err := mgr.RestoreCheckpoint(s.checkpointID); err != nil {
				rewindErr = err
			} else {
				rewound = true
			}
		default:
			// A fork may be partially complete (error between variants)
			// while the session is still in review: rewind to the fork
			// tree if we have one.
			if scope, ok := mgr.Get(s.scopeID); ok && s.forkTree != nil {
				if err := scope.ApplyTree(s.runtime.RepoPath, s.forkTree); err != nil {
					rewindErr = err
				} else {
					rewound = true
				}
			}
		}
	}

	mgr.PruneCheckpoints(s.scopeID)

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
