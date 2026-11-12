package tools

// toolbelt_test.go pins yaah-6mi9.5's mechanism at the registry it
// flows through: mounting registers through the belt (per-member
// Admit + Register, the MCP seam), a family whose every member is
// refused fails atomically (nothing registers, the belt's refusal set
// is exactly what it was), mounts are idempotent, the mounted fact
// records the family/​tools/refused triple, and a direct call to a
// stub-named tool answers with the one-turn recovery instead of the
// generic unknown tool. The family map itself is pinned against the
// curation in cmd/yaah/orchestrator_belt_test.go.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	kernel "github.com/buchenberg/goatherd-kernel"
)

// newToolbeltTestRegistry builds the .5 startup shape: a belt that
// admits the working surface and refuses the mountable families — the
// curated default's fold — plus the loader itself.
func newToolbeltTestRegistry(t *testing.T) (*ToolbeltTool, *Registry, *Belt) {
	t.Helper()
	refused := []string{}
	for _, f := range toolFamilies {
		refused = append(refused, f.Tools...)
	}
	allowed := []string{"bash", "read", "write", "edit", "ls", "grep", "glob", "toolbelt"}
	belt := NewBelt(allowed)
	reg := NewRegistryWithBelt(belt)
	// Simulate the startup refusals the registry records for names the
	// belt refused at construction, so Withdraw restores this state.
	for _, name := range refused {
		if reg.Get(name) != nil {
			t.Fatalf("test belt admits %q, which must be folded", name)
		}
	}
	tt := &ToolbeltTool{Reg: reg}
	reg.Register(tt)
	return tt, reg, belt
}

// TestToolbeltMountRegistersThroughTheBelt pins the seam: a mount
// registers every member and the belt's refusal set loses exactly
// those names — the widening primitive working as designed, never a
// registration alongside the belt.
func TestToolbeltMountRegistersThroughTheBelt(t *testing.T) {
	tt, reg, belt := newToolbeltTestRegistry(t)
	for _, name := range []string{"append", "mkdir"} {
		if reg.Get(name) != nil {
			t.Fatalf("%s registered before mount", name)
		}
	}
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"file_ops"}`); err != nil {
		t.Fatalf("mount file_ops: %v", err)
	}
	fam, ok := familyByName("file_ops")
	if !ok {
		t.Fatal("file_ops family missing from the map")
	}
	for _, name := range fam.Tools {
		if reg.Get(name) == nil {
			t.Errorf("%s not registered after mount", name)
		}
		if belt.Refused() != nil && slices.Contains(belt.Refused(), name) {
			t.Errorf("%s: mount left a stale belt refusal behind", name)
		}
	}
}

// TestToolbeltMountIsIdempotent pins stickiness: a second mount of the
// same family registers nothing, answers "already mounted", and
// records no second fact.
func TestToolbeltMountIsIdempotent(t *testing.T) {
	tt, _, _ := newToolbeltTestRegistry(t)
	sink := &mountFactRecorder{}
	tt.SetSink(&MountSink{Appender: sink})
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`); err != nil {
		t.Fatalf("first mount: %v", err)
	}
	out, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`)
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	if !strings.Contains(out, "already fully mounted") {
		t.Errorf("remount answer = %q, want the already-mounted no-op", out)
	}
	if sink.facts != 1 {
		t.Errorf("recorded %d facts, want 1 (remount appends nothing)", sink.facts)
	}
}

// TestToolbeltMountAtomicFailure pins decision 3: a family whose every
// member the session refuses mounts NOTHING — the belt admissions
// revert (the refusal set is exactly what it was), the answer names
// the refusing origin, and no fact records.
func TestToolbeltMountAtomicFailure(t *testing.T) {
	tt, reg, belt := newToolbeltTestRegistry(t)
	before := belt.Refused()
	slices.Sort(before)
	tt.Admits = func(name string) (bool, string) { return false, "persona" }
	_, err := tt.Execute(context.Background(), `{"action":"mount","family":"net"}`)
	if err == nil {
		t.Fatal("mount of an all-refused family succeeded; the atomic rule must fail it")
	}
	if !strings.Contains(err.Error(), "persona") || !strings.Contains(err.Error(), "http") {
		t.Errorf("refusal answer = %q, want the refusing origin and member names", err)
	}
	if reg.Get("http") != nil || reg.Get("webfetch") != nil {
		t.Error("atomic failure registered a member anyway")
	}
	after := belt.Refused()
	slices.Sort(after)
	if !slices.Equal(before, after) {
		t.Errorf("belt refusals after failed mount = %v, want exactly %v (widening must revert)", after, before)
	}
}

// TestToolbeltPartialRefusalMountsSurvivors pins the per-member rule:
// a session that refuses some members still mounts the family — the
// survivors register, the refusal's origin rides the answer and the
// fact.
func TestToolbeltPartialRefusalMountsSurvivors(t *testing.T) {
	tt, reg, _ := newToolbeltTestRegistry(t)
	tt.Admits = func(name string) (bool, string) {
		if name == "diff" {
			return false, "mode_floor"
		}
		return true, ""
	}
	out, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`)
	if err != nil {
		t.Fatalf("partial mount: %v", err)
	}
	if !strings.Contains(out, "mode_floor") || !strings.Contains(out, "diff") {
		t.Errorf("partial mount answer = %q, want diff (mode_floor) named as withheld", out)
	}
	if reg.Get("calculate") == nil || reg.Get("json_query") == nil {
		t.Error("surviving members did not register")
	}
	if !strings.Contains(out, "diff") && reg.Get("diff") == nil {
		t.Log("diff withheld and reported: correct")
	}
}

// TestToolbeltMountRecordsFact pins the audit trail: a real mount
// appends yaah.tool.mounted.v1 with the family, the mounted tools,
// and the per-member refusals.
func TestToolbeltMountRecordsFact(t *testing.T) {
	tt, _, _ := newToolbeltTestRegistry(t)
	sink := &mountFactRecorder{}
	tt.SetSink(&MountSink{Appender: sink, OpID: "op-1"})
	tt.Admits = func(name string) (bool, string) {
		if name == "json_query" {
			return false, "mode_floor"
		}
		return true, ""
	}
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if len(sink.payloads) != 1 {
		t.Fatalf("recorded %d facts, want 1", len(sink.payloads))
	}
	if sink.schema != "yaah.tool.mounted.v1" || sink.kind != "tool" {
		t.Errorf("fact schema/kind = %q/%q, want yaah.tool.mounted.v1/tool", sink.schema, sink.kind)
	}
	p := sink.payloads[0]
	if p["family"] != "analysis" {
		t.Errorf("fact family = %v, want analysis", p["family"])
	}
	mounted := p["tools"].([]string)
	if !slices.Equal(mounted, []string{"calculate", "file_info", "diff", "bisect"}) {
		t.Errorf("fact tools = %v, want the mounted members", mounted)
	}
	refused := p["refused"].(map[string]string)
	if refused["json_query"] != "mode_floor" {
		t.Errorf("fact refusals = %v, want json_query: mode_floor", refused)
	}
}

// TestToolbeltListAnswersMembership pins the list action: every family
// with state, and full membership — the authoritative surface for a
// model that lost the stub block.
func TestToolbeltListAnswersMembership(t *testing.T) {
	tt, reg, _ := newToolbeltTestRegistry(t)
	out, err := tt.Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, f := range toolFamilies {
		if !strings.Contains(out, f.Name) {
			t.Errorf("list answer missing family %q", f.Name)
		}
		for _, name := range f.Tools {
			if !strings.Contains(out, name) {
				t.Errorf("list answer missing member %q", name)
			}
		}
	}
	if !strings.Contains(out, "not mounted") {
		t.Errorf("list answer = %q, want per-family mount state", out)
	}
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"net"}`); err != nil {
		t.Fatalf("mount net: %v", err)
	}
	out, err = tt.Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "mounted") {
		t.Errorf("list after mount = %q, want the mounted state reported", out)
	}
	_ = reg
}

// TestRegistryNotMountedError pins the one-turn recovery: calling a
// folded family member that this session can still mount answers with
// the family and the mount instruction, and still matches the
// ToolNotFoundError sentinel so existing consumers keep working.
func TestRegistryNotMountedError(t *testing.T) {
	tt, reg, _ := newToolbeltTestRegistry(t)
	_, err := reg.Execute(context.Background(), "diff", `{}`)
	if err == nil {
		t.Fatal("call to unmounted diff succeeded")
	}
	var nme ToolNotMountedError
	if !errors.As(err, &nme) {
		t.Fatalf("err = %v, want ToolNotMountedError", err)
	}
	if nme.Family != "analysis" {
		t.Errorf("error family = %q, want analysis", nme.Family)
	}
	if !strings.Contains(err.Error(), "toolbelt") {
		t.Errorf("error = %q, want the mount instruction", err)
	}
	if !errors.Is(err, ToolNotFoundError{}) {
		t.Error("ToolNotMountedError must still match the ToolNotFoundError sentinel")
	}
	_, err = reg.Execute(context.Background(), "no_such_tool", `{}`)
	if !errors.Is(err, ToolNotFoundError{}) {
		t.Errorf("err = %v, want the plain ToolNotFoundError for a name no family knows", err)
	}
	// Without the loader registered, the same folded name answers with
	// the generic refusal: a session whose belt did not admit toolbelt
	// never teaches the mount it cannot make.
	reg2 := NewRegistry()
	_, err = reg2.Execute(context.Background(), "checksum", `{}`)
	if _, ok := err.(ToolNotMountedError); ok {
		t.Errorf("registry without toolbelt answered ToolNotMountedError: %v", err)
	}
	_ = tt
}

// TestToolbeltFamiliesContainNoHostOnlyTools pins the isolation
// invariant: mounting is mid-session registration, after
// enforceIsolatedWorkspace ran, so nothing a mount can register may
// be host-only — the SetWorkspace gate never sees it. A host-only
// tool joining a family must fail here first.
func TestToolbeltFamiliesContainNoHostOnlyTools(t *testing.T) {
	hostOnly := map[string]bool{}
	for _, name := range HostOnlyLeafTools() {
		hostOnly[name] = true
	}
	for _, f := range toolFamilies {
		for _, name := range f.Tools {
			if hostOnly[name] {
				t.Errorf("family %q carries host-only member %q — a mount would smuggle it past the isolation gate", f.Name, name)
			}
			if NewLeafTool(name) == nil {
				t.Errorf("family %q carries %q, but the leaf factory knows no such tool", f.Name, name)
			}
		}
	}
}

// familyByName is the test-side lookup the tool keeps unexported.
func familyByName(name string) (*ToolFamily, bool) {
	for i := range toolFamilies {
		if toolFamilies[i].Name == name {
			return &toolFamilies[i], true
		}
	}
	return nil, false
}

// mountFactRecorder is a fake MountFactAppender.
type mountFactRecorder struct {
	facts    int
	schema   string
	kind     string
	payloads []map[string]any
}

func (m *mountFactRecorder) AppendFact(ctx context.Context, schemaRef, kindLabel string, payload map[string]any) (kernel.AppendReceipt, error) {
	m.facts++
	m.schema = schemaRef
	m.kind = kindLabel
	m.payloads = append(m.payloads, payload)
	return kernel.AppendReceipt{}, nil
}

// TestToolbeltListPartialState pins review #4557526042: a family with
// some members on the belt reports "partially mounted" with counts,
// never a bare "not mounted" over a mount that succeeded.
func TestToolbeltListPartialState(t *testing.T) {
	tt, reg, _ := newToolbeltTestRegistry(t)
	tt.Admits = func(name string) (bool, string) {
		if name == "json_query" || name == "bisect" {
			return false, "mode_floor"
		}
		return true, ""
	}
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`); err != nil {
		t.Fatalf("partial mount: %v", err)
	}
	out, err := tt.Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "partially mounted: 3 of 5") {
		t.Errorf("list after partial mount = %q, want the counted partial state", out)
	}
	if reg.Get("calculate") == nil {
		t.Error("partial mount's surviving member missing")
	}
}

// TestToolbeltMountRequiresFamily pins review #4557526053's runtime
// half: a mount action without a family names the requirement and the
// known families instead of landing in the generic unknown-family
// error.
func TestToolbeltMountRequiresFamily(t *testing.T) {
	tt, _, _ := newToolbeltTestRegistry(t)
	_, err := tt.Execute(context.Background(), `{"action":"mount"}`)
	if err == nil {
		t.Fatal("mount without family succeeded")
	}
	if !strings.Contains(err.Error(), "family is required for mount") || !strings.Contains(err.Error(), "file_ops") {
		t.Errorf("err = %q, want the requirement and the family catalog", err)
	}
}

// TestToolbeltRemountStillNamesWithheld pins the review bug: a remount
// of a partially refused family must keep the refusal line instead of
// answering "already fully mounted" over members the session withheld.
func TestToolbeltRemountStillNamesWithheld(t *testing.T) {
	tt, _, _ := newToolbeltTestRegistry(t)
	tt.Admits = func(name string) (bool, string) {
		if name == "json_query" {
			return false, "mode_floor"
		}
		return true, ""
	}
	if _, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`); err != nil {
		t.Fatalf("first mount: %v", err)
	}
	out, err := tt.Execute(context.Background(), `{"action":"mount","family":"analysis"}`)
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	if strings.Contains(out, "already fully mounted") {
		t.Errorf("remount answer claims full mount over a withheld member: %q", out)
	}
	if !strings.Contains(out, "json_query") || !strings.Contains(out, "mode_floor") {
		t.Errorf("remount answer = %q, want the withheld member named with its origin", out)
	}
}
