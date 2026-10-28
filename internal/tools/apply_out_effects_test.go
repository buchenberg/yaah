package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime/effect"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// apply_out_effects_test.go pins the trace half of the accept seam: that an
// accept records what it landed as typed workspace effects, and that failing to
// record never fails the accept.
//
// The in-memory fake has no typed change set — only the containerd adapter
// offers EffectChanges — so these tests wrap it in a double that does. That
// keeps the emission path exercisable without a daemon, and exercises the same
// structural type assertion production uses rather than a containerd-specific
// one.

// effectSandbox adds the optional typed-change capability to the in-memory fake.
type effectSandbox struct {
	goatherdsandbox.Sandbox
	changes []effect.Change
	err     error
	calls   int
}

func (e *effectSandbox) EffectChanges(context.Context, shepherdtrace.WorkspaceState) ([]effect.Change, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return e.changes, nil
}

// effectOpener mirrors cleanOpener but hands back sandboxes that also report
// typed changes. The first sandbox opened is the image-contract probe, which
// needs the permissive ExecHook; the rest are session sandboxes.
func effectOpener(changes []effect.Change, changesErr error) (SandboxOpener, func() []*effectSandbox) {
	var opened []*effectSandbox
	open := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		fake := sandboxfake.New()
		if len(opened) == 0 {
			fake.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
		}
		es := &effectSandbox{Sandbox: fake, changes: changes, err: changesErr}
		opened = append(opened, es)
		return es, nil
	}
	return open, func() []*effectSandbox { return opened }
}

// recordingAppender is a stand-in for *goatherd.Scope: it satisfies
// effect.FactAppender and keeps what was appended.
type recordingAppender struct {
	ownerID string
	refs    []string
	labels  []string
	payload []map[string]any
	err     error
	seq     int
}

func (r *recordingAppender) AppendFact(_ context.Context, schemaRef, kindLabel string, payload map[string]any) (shepherdtrace.AppendReceipt, error) {
	r.seq++
	if r.err != nil {
		return shepherdtrace.AppendReceipt{}, r.err
	}
	r.refs = append(r.refs, schemaRef)
	r.labels = append(r.labels, kindLabel)
	r.payload = append(r.payload, payload)
	return shepherdtrace.AppendReceipt{
		AppendIntentID: fmt.Sprintf("intent:%d", r.seq),
		FactIDs:        []string{fmt.Sprintf("fact:%d", r.seq)},
	}, nil
}

func (r *recordingAppender) OwnerID() string { return r.ownerID }

// sampleChanges is a create, a modify and a delete — one per effect schema.
func sampleChanges() []effect.Change {
	return []effect.Change{
		{Path: "new.txt", Kind: effect.KindCreated, Mode: 0o644, Size: 5},
		{Path: "readme.txt", Kind: effect.KindPatched, Mode: 0o600, Size: 9},
		{Path: "gone.txt", Kind: effect.KindDeleted},
	}
}

// newEffectSession bootstraps a real session over the effect-capable double and
// makes one change, so the accept has a non-empty delta to apply.
func newEffectSession(t *testing.T, changes []effect.Change, changesErr error) (*SessionSandbox, *effectSandbox, string) {
	t.Helper()
	src := gitHostRepo(t)
	open, opened := effectOpener(changes, changesErr)
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	t.Cleanup(func() { _ = ss.Destroy(context.Background()) })

	ws := ss.Workspace()
	readme, rerr := ws.ResolvePath("readme.txt")
	if rerr != nil {
		t.Fatalf("ResolvePath: %v", rerr)
	}
	if err := ws.WriteFile(context.Background(), readme, []byte("edited in the session\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// opened() is the accessor: [0] is the image-contract probe, [1] the session
	// sandbox whose EffectChanges the accept consults.
	sbs := opened()
	if len(sbs) < 2 {
		t.Fatalf("expected a probe and a session sandbox, got %d", len(sbs))
	}
	return ss, sbs[1], src
}

func TestSessionSandbox_SupportsEffects(t *testing.T) {
	// The capability is discovered structurally, so a backend that offers it is
	// used and one that does not is skipped — the git lane and the in-memory
	// fake have no typed change set, and that must not be an error.
	withCapability, _, _ := newEffectSession(t, sampleChanges(), nil)
	if !withCapability.SupportsEffects() {
		t.Error("a sandbox offering EffectChanges must be reported as supporting effects")
	}

	src := gitHostRepo(t)
	open, _ := cleanOpener()
	plain, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	t.Cleanup(func() { _ = plain.Destroy(context.Background()) })
	if plain.SupportsEffects() {
		t.Error("the in-memory fake has no EffectChanges and must not be reported as supporting effects")
	}
	if _, err := plain.EffectChanges(context.Background()); err == nil {
		t.Error("EffectChanges on a backend without the capability must fail, so a caller cannot mistake it for an empty change set")
	}
}

func TestSessionSandbox_EffectChangesRequiresBaseline(t *testing.T) {
	// Without a bootstrap there is no anchor to diff against, so there is no
	// meaningful change set — the same precondition ApplyOut and
	// DiffSinceBaseline already enforce.
	ss := &SessionSandbox{sb: &effectSandbox{Sandbox: sandboxfake.New(), changes: sampleChanges()}}
	if _, err := ss.EffectChanges(context.Background()); err == nil {
		t.Fatal("EffectChanges without a baseline must fail")
	} else if !strings.Contains(err.Error(), "baseline") {
		t.Errorf("the error must name the missing baseline, got: %v", err)
	}
}

func TestApplyOutTool_RecordsEffectsOnAccept(t *testing.T) {
	ss, sb, src := newEffectSession(t, sampleChanges(), nil)
	// ownerID and OpID are deliberately different strings. In production they
	// are distinct values — TraceOwnerID comes from the scope's OwnerID() (the
	// session) while caused_by/CausalParents come from the sink's OpID (the root
	// scope) — so a test that used one string for both would still pass if Emit
	// sourced either output from the wrong field.
	const ownerID, opID = "session:abc", "scope:root-1"
	appender := &recordingAppender{ownerID: ownerID}
	bus := shepherdtrace.NewEffectBus(16)
	events := bus.Subscribe("test")

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src, Effects: &EffectSink{
		Appender: appender,
		Bus:      bus,
		OpID:     opID,
	}}

	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(appender.refs) != 0 {
		t.Errorf("a dry run must record no effects, got %v", appender.refs)
	}

	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(out, "could not be recorded") {
		t.Errorf("the result reports a recording failure that did not happen:\n%s", out)
	}

	want := []struct{ ref, label string }{
		{effect.SchemaFileCreated, effect.LabelFileCreated},
		{effect.SchemaFilePatched, effect.LabelFilePatched},
		{effect.SchemaFileDeleted, effect.LabelFileDeleted},
	}
	if len(appender.refs) != len(want) {
		t.Fatalf("recorded %d effects, want %d: %v", len(appender.refs), len(want), appender.refs)
	}
	for i, w := range want {
		if appender.refs[i] != w.ref || appender.labels[i] != w.label {
			t.Errorf("effect %d = (%q, %q), want (%q, %q)", i, appender.refs[i], appender.labels[i], w.ref, w.label)
		}
		if got := appender.payload[i]["caused_by"]; got != opID {
			t.Errorf("effect %d caused_by = %v, want the sink's OpID %q", i, got, opID)
		}
	}

	// Each effect is also published for live supervision. The receive is
	// bounded: a bare channel read would block until the test binary's deadline
	// if Emit published fewer events than expected — a subscriber buffer that
	// filled and dropped, say — turning a clear failure into a hung run.
	published := 0
	for i := 0; i < len(want); i++ {
		var ev shepherdtrace.EffectEvent
		select {
		case ev = <-events:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for effect event %d of %d; published %d so far", i+1, len(want), published)
		}
		published++
		if len(ev.CausalParents) != 1 || ev.CausalParents[0] != opID {
			t.Errorf("event %d CausalParents = %v, want [%s] — the sink's OpID", i, ev.CausalParents, opID)
		}
		if ev.TraceOwnerID != ownerID {
			t.Errorf("event %d TraceOwnerID = %q, want %q — the appender's owner path", i, ev.TraceOwnerID, ownerID)
		}
	}
	if sb.calls == 0 {
		t.Error("the backend's EffectChanges was never consulted")
	}
}

func TestApplyOutTool_EmitFailureDoesNotFailTheApply(t *testing.T) {
	// The host write already happened by the time recording runs, so returning
	// an error would tell the model the accept failed when it succeeded — and a
	// retry would apply the delta a second time. The gap is reported in the
	// result instead.
	ss, _, src := newEffectSession(t, sampleChanges(), nil)
	appender := &recordingAppender{ownerID: "session:abc", err: errors.New("store unavailable")}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src, Effects: &EffectSink{Appender: appender, OpID: "session:abc"}}
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("a recording failure must not fail the accept: %v", err)
	}
	if !strings.Contains(out, "could not be recorded") {
		t.Errorf("the result must say the effects were not recorded, got:\n%s", out)
	}
	if !strings.Contains(out, "Applied") {
		t.Errorf("the result must still report the successful apply, got:\n%s", out)
	}
	// And the host really was written.
	raw, rerr := os.ReadFile(filepath.Join(src, "readme.txt"))
	if rerr != nil || string(raw) != "edited in the session\n" {
		t.Errorf("host readme.txt = %q, %v — the apply must have landed despite the recording failure", raw, rerr)
	}
}

func TestApplyOutTool_BackendWithoutEffectsIsANoOp(t *testing.T) {
	// The git lane and the in-memory fake have no typed change set. A sink being
	// configured must not turn that into an error on every accept.
	src := gitHostRepo(t)
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	t.Cleanup(func() { _ = ss.Destroy(context.Background()) })

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	appender := &recordingAppender{ownerID: "session:abc"}
	tool := &ApplyOutTool{Sandbox: ss, HostDir: src, Effects: &EffectSink{Appender: appender, OpID: "session:abc"}}
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(out, "could not be recorded") {
		t.Errorf("a backend without the capability is not a failure, got:\n%s", out)
	}
	if len(appender.refs) != 0 {
		t.Errorf("recorded %d effects from a backend that reports none", len(appender.refs))
	}
}

func TestApplyOutTool_NilSinkIsANoOp(t *testing.T) {
	// A session with no tracing must accept exactly as it did before effects
	// existed: no sink, no recording, no note in the result.
	ss, _, src := newEffectSession(t, sampleChanges(), nil)
	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}

	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(out, "could not be recorded") {
		t.Errorf("no sink means nothing to record, so nothing to report, got:\n%s", out)
	}
}

func TestApplyOutTool_BackendErrorIsReportedNotFatal(t *testing.T) {
	// A backend that advertises the capability and then fails is a real fault,
	// but still not a reason to fail an accept that succeeded.
	ss, _, src := newEffectSession(t, nil, errors.New("daemon read failed"))
	appender := &recordingAppender{ownerID: "session:abc"}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src, Effects: &EffectSink{Appender: appender, OpID: "session:abc"}}
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("a backend effect failure must not fail the accept: %v", err)
	}
	if !strings.Contains(out, "could not be recorded") {
		t.Errorf("the result must report the failure, got:\n%s", out)
	}
	if !strings.Contains(out, "daemon read failed") {
		t.Errorf("the result must carry the underlying cause, got:\n%s", out)
	}
	if len(appender.refs) != 0 {
		t.Errorf("recorded %d effects despite the backend failing", len(appender.refs))
	}
}
