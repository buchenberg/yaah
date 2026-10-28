package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime/effect"
	"github.com/buchenberg/yaah/internal/prompts"
)

// apply_out.go is the accept seam of the isolated workspace (plan §2.3,
// decision D1): copy-in at session start, capture/diff during, apply-out
// on accept. The bootstrap verbs landed with the D1 step; this tool is the
// session surface that finally invokes ApplyOut, so changes do not leave
// the container until a human accepts them.
//
// The accept decision belongs to the user, not the model: the tool
// implements DangerClassifier, so the actual apply is gated behind the
// approval flow (approval mode "ask" prompts; "deny" strips the call). A
// dry run is never dangerous — the model can always show the user what
// would land before asking them to accept it.
//
// The approval prompt shows only the tool name and arguments, not the
// delta — so Execute refuses an apply that was not preceded by a dry run
// in this session, and refuses one whose preview has gone stale: the user
// approves exactly the diff they were shown, or nothing. A mutation that
// lands in the microseconds between the staleness check and the backend's
// own capture is the residual window; closing it needs backend-level
// capture-and-apply atomicity, which is settlement's problem, not this
// tool's.
//
// It is deliberately NOT a filesystem tool: it must not be routed through
// the sandbox workspace it is the door out of, and it must not carry the
// hostOnly() marker either — it is the one tool whose whole job is to
// write the host, registered only in isolated mode.

// EffectSink carries what recording typed workspace effects needs. It is
// optional and late-bound: ApplyOutTool is registered during tool wiring, well
// before the trace store, effect bus and root scope exist, so wiring attaches
// this afterwards. A nil sink means the session has no tracing, and the accept
// behaves exactly as it did before effects existed.
type EffectSink struct {
	// Appender records each effect as a capture fact on its owner path.
	// *goatherd.Scope satisfies effect.FactAppender with methods it already
	// exports; naming the interface here keeps this package off the runtime
	// root.
	Appender effect.FactAppender
	// Bus publishes the effects for live supervision. Nil appends the durable
	// record without publishing, which is correct for a session with no
	// supervisor attached.
	Bus *shepherdtrace.EffectBus
	// OpID attributes the effects to an operation, becoming each record's
	// caused_by and the bus event's causal parent.
	//
	// This is the session's root scope, not an individual tool call: Execute
	// receives no call identifier, and inventing one here would produce an ID
	// nothing else in the trace recognises.
	//
	// Two consequences worth stating plainly rather than letting the field name
	// imply otherwise. The attribution is coarser than the Python reference's
	// per-intent caused_by. And because the session baseline is captured at
	// bootstrap and never advanced, the effects describe the whole session delta
	// as of this accept — not just what changed since the previous one — so two
	// accepts in one session record overlapping sets. Execute clears its preview
	// after an apply, which stops an accept repeating without a fresh
	// user-visible dry run, but that bounds the repetition; it does not make the
	// delta incremental.
	OpID string
}

// ApplyOutTool applies the session sandbox's outward delta onto the host
// working tree, or previews it.
type ApplyOutTool struct {
	// Sandbox is the session's sandbox; its baseline (from the D1
	// bootstrap) is the anchor the delta is computed against.
	Sandbox *SessionSandbox
	// HostDir is the host working tree the delta lands in — the source
	// the bootstrap copied in.
	HostDir string
	// effects, when set, records what the accept landed as typed workspace
	// effects after the host write succeeds. See EffectSink.
	//
	// Unexported so the mutex actually guards it. An exported field lets any
	// caller assign around SetEffects and skip the lock that recordEffects reads
	// under — which is the exact race the setter was added to close, and a guard
	// that can be walked around is documentation, not enforcement. Wiring and
	// tests both go through SetEffects.
	effects *EffectSink

	// mu serializes this tool's calls and guards preview. Tool calls can
	// dispatch in parallel, so two apply_out calls — or a dry run racing
	// an apply — must not interleave.
	mu sync.Mutex
	// preview is the exact diff text of the last dry run; "" when no
	// preview has been shown in this session.
	preview string
}

func (t *ApplyOutTool) Name() string        { return "apply_out" }
func (t *ApplyOutTool) Description() string { return prompts.ToolDescription("apply_out") }

func (t *ApplyOutTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"dry_run": {
				"type": "boolean",
				"description": "Preview the diff that would be applied without touching the host (default false)"
			}
		}
	}`)
}

type applyOutParams struct {
	DryRun bool `json:"dry_run"`
}

// Execute previews (dry_run) or applies the session's delta onto the host.
// The diff is unbounded on purpose: it is both what the user sees and the
// token the apply is verified against, so truncating it would let two
// different workspaces compare equal past the cut. Oversized results are
// the pipeline's to spill.
func (t *ApplyOutTool) Execute(ctx context.Context, args string) (string, error) {
	var p applyOutParams
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", fmt.Errorf("apply_out: parse arguments: %w", err)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	diff, files, err := t.Sandbox.DiffSinceBaseline(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("apply_out: %w", err)
	}
	if len(files) == 0 {
		t.preview = "" // nothing to apply, so nothing is approved either
		return "No changes in the isolated workspace — the host tree and the container match.", nil
	}

	sort.Strings(files)
	if p.DryRun {
		t.preview = diff
		return fmt.Sprintf("Dry run — %d file(s) would be applied to the host working tree; the host is untouched.\n\n%s",
			len(files), diff), nil
	}

	// The approval prompt shows only "apply_out" and its arguments — the
	// user must have seen the delta somewhere, and the only somewhere is
	// a dry run's result in this session.
	if t.preview == "" {
		return "", fmt.Errorf("apply_out: no preview yet — call with dry_run: true first and show the user the diff; the approval prompt cannot show it")
	}
	if t.preview != diff {
		return "", fmt.Errorf("apply_out: the workspace changed since the preview — call with dry_run: true again and show the user the updated diff")
	}

	if err := t.Sandbox.ApplyOut(ctx, t.HostDir); err != nil {
		return "", fmt.Errorf("apply_out: %w", err)
	}

	// The preview is consumed by this apply. The user's approval was for that
	// specific diff landing on the host, and it has now landed — so the approval
	// is spent, and a further apply must be preceded by a fresh dry run.
	//
	// Without this the tool re-applies on a second call with no new dry run:
	// ApplyOut does not change the sandbox, so the recomputed diff still equals
	// the stored preview and the staleness check passes. That was already wrong
	// for the host write, and became worse with effects wired up — a repeated
	// accept would append the same effect records again and republish them on
	// the bus the advisory supervisor consumes.
	//
	// Note this bounds the repetition; it does not make effects per-accept
	// deltas. The baseline is captured at bootstrap and never advanced, so each
	// accept records the whole session delta as of that moment. Advancing it
	// would need a Capture, and croft's Capture commits a snapshot layer and
	// pins it under a lease — reintroducing per-accept resource growth that
	// ApplyOut deliberately shed. Recorded as a follow-up rather than paid for
	// here.
	t.preview = ""

	msg := fmt.Sprintf("Applied %d file(s) onto the host working tree:\n  %s\n\nThe changes are in the host's working tree — not committed. Review and commit with the user's normal git flow.",
		len(files), strings.Join(files, "\n  "))

	// Recording effects is best-effort and must not fail the call. The host
	// write already happened, so returning an error here would tell the model
	// the accept failed when it succeeded — and a retry would apply the delta a
	// second time. A trace gap is reported in the result rather than hidden or
	// treated as a failed apply.
	//
	// The count is reported only when it is known. When reading the change set
	// is what failed, the size of the gap is unknown and saying "0 changes"
	// would read as "nothing was lost" — the opposite of the truth.
	if n, err := t.recordEffects(ctx); err != nil {
		if n == effectsUnknown {
			msg += fmt.Sprintf("\n\n(note: the host tree is updated, but the workspace effects could not be recorded in the trace and how many changes that lost is unknown: %v)", err)
		} else {
			msg += fmt.Sprintf("\n\n(note: the host tree is updated, but %d workspace change(s) could not be recorded as trace effects: %v)", n, err)
		}
	}
	return msg, nil
}

// SetEffects late-binds the trace sink.
//
// Wiring registers this tool long before the trace store, effect bus and root
// scope exist, so the sink cannot be a constructor argument. The setter takes
// the same mutex Execute reads t.Effects under. Today wiring completes before
// any turn can dispatch, so the ordering is safe either way — but that safety
// is incidental, and a future reorder or a sink attached from another goroutine
// would be a data race that -race only catches when the timing cooperates.
// Taking the lock makes the ordering explicit instead.
func (t *ApplyOutTool) SetEffects(s *EffectSink) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.effects = s
}

// effectsUnknown is recordEffects' returned count when the backend call that
// would report the change set is itself what failed, so the size of the gap
// cannot be known. Reporting 0 there would read as "nothing was lost", which is
// the opposite of the truth: everything the accept changed is unrecorded, and
// how much that is was never determined.
const effectsUnknown = -1

// recordEffects writes the accepted delta into the trace as typed workspace
// effects and returns how many were recorded. On failure the count is how many
// workspace changes went unrecorded — or effectsUnknown when the change set
// could not be read at all — so the caller can say how large the gap is rather
// than just that there is one.
//
// It is a silent no-op — not an error — when the session has no trace sink, or
// when the backend has no typed change set to give (the git lane, and the
// tests' in-memory fake). Only a backend that reports the capability and then
// fails is an error worth surfacing.
//
// Mapping is all-or-nothing: effect.FromChanges rejects a change kind it does
// not know, and this does not record the mappable remainder. That is deliberate
// — a partial trace that looks complete is worse than a reported gap, and the
// count in the returned error says how much was lost. In practice the adapter
// rejects an unknown croft kind first, so this path needs a backend that
// produces a kind the runtime's vocabulary has never seen.
//
// Called after ApplyOut succeeds, so the effects describe changes that actually
// reached the host. A failed apply records nothing: claiming effects for work
// that did not land would be worse than recording none. The residual gap is a
// *partial* apply — copyOut writes file by file, so an error part way through
// leaves some changes on the host and none in the trace. Closing that needs
// either per-file emission or an apply that reports what it landed; both are
// larger changes than this seam warrants, and the tool result already names the
// paths it applied.
//
// Must be called with t.mu held, which Execute does. It reads t.effects without
// taking the lock itself because Go mutexes are not reentrant — acquiring it
// again here would deadlock. That is also why the field is unexported: the only
// writer is SetEffects, which takes the lock, so every access is ordered.
func (t *ApplyOutTool) recordEffects(ctx context.Context) (int, error) {
	sink := t.effects
	if sink == nil || sink.Appender == nil {
		return 0, nil
	}
	if !t.Sandbox.SupportsEffects() {
		return 0, nil
	}
	changes, err := t.Sandbox.EffectChanges(ctx)
	if err != nil {
		return effectsUnknown, err
	}
	effects, err := effect.FromChanges(changes)
	if err != nil {
		return len(changes), err
	}
	if len(effects) == 0 {
		return 0, nil
	}
	if err := effect.Emit(ctx, sink.Appender, sink.Bus, sink.OpID, effects); err != nil {
		return len(effects), err
	}
	return len(effects), nil
}

// IsDangerous gates the real apply behind user approval. A dry run never
// touches the host, so it is never dangerous; unparseable arguments are
// treated as an apply — failing closed on the approval gate.
func (t *ApplyOutTool) IsDangerous(argsJSON string) bool {
	var p applyOutParams
	if argsJSON == "" {
		return true
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return true
	}
	return !p.DryRun
}
