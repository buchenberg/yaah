package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestProposalList_OverlapAndParentDrift pins the Phase 0 list contract
// (yaah-9bq): open proposals name every shared path and every path the parent
// changed since their dispatch basis, before either is settled.
func TestProposalList_OverlapAndParentDrift(t *testing.T) {
	runner := writingRunner(t, "README.md")
	rt := newRetainedTestEnv(t, runner)

	first, err := startRetainedRun(context.Background(), rt, "first approach", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	second, err := startRetainedRun(context.Background(), rt, "second approach", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	firstEnv := decodeProposalEnvelope(t, first)
	secondEnv := decodeProposalEnvelope(t, second)

	// The parent moves after both dispatch bases were captured.
	writeTestFile(t, rt.RepoPath, "host-only.txt", "host change\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Proposals) != 2 {
		t.Fatalf("proposals = %+v, want the two open proposals", listed.Proposals)
	}
	if len(listed.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one pairwise overlap", listed.Conflicts)
	}
	conflict := listed.Conflicts[0]
	if conflict.Kind != proposalConflictPathOverlap {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictPathOverlap)
	}
	if got := strings.Join(conflict.Paths, ","); got != "README.md" {
		t.Errorf("conflict paths = %v, want README.md", conflict.Paths)
	}
	if !containsString(conflict.Proposals, firstEnv.ProposalID) || !containsString(conflict.Proposals, secondEnv.ProposalID) {
		t.Errorf("conflict proposals = %v, want both %s and %s", conflict.Proposals, firstEnv.ProposalID, secondEnv.ProposalID)
	}

	for _, summary := range listed.Proposals {
		if len(summary.Conflicts) != 1 {
			t.Errorf("summary %s conflicts = %+v, want the pair entry", summary.ProposalID, summary.Conflicts)
		}
		if !containsString(summary.ParentDrift, "host-only.txt") {
			t.Errorf("summary %s parent_drift = %v, want host-only.txt", summary.ProposalID, summary.ParentDrift)
		}
		if summary.ParentDriftError != "" {
			t.Errorf("summary %s parent_drift_error = %q, want none", summary.ProposalID, summary.ParentDriftError)
		}
	}
}

// TestProposalList_DisjointHasNoConflicts pins the negative case: disjoint
// proposals must not emit a conflict record, and the list still carries an
// explicit empty array.
func TestProposalList_DisjointHasNoConflicts(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), fmt.Sprintf("out-%d.txt", call), "work\n")
	}
	rt := newRetainedTestEnv(t, runner)

	for i := 0; i < 2; i++ {
		if _, err := startRetainedRun(context.Background(), rt, fmt.Sprintf("attempt %d", i), SubAgentParams{Role: "worker"}, 0, false); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(fields["conflicts"])); got != "[]" {
		t.Errorf("conflicts = %s, want explicit []", got)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	for _, summary := range listed.Proposals {
		if len(summary.Conflicts) != 0 {
			t.Errorf("summary %s conflicts = %+v, want none", summary.ProposalID, summary.Conflicts)
		}
		if len(summary.ParentDrift) != 0 {
			t.Errorf("summary %s parent_drift = %v, want none", summary.ProposalID, summary.ParentDrift)
		}
	}
}

// TestProposalTool_ApplyConflictIsStructured pins the machine-readable apply
// refusal: the tool returns a conflict envelope (not a prose error), names the
// overlap, and leaves the proposal open.
func TestProposalTool_ApplyConflictIsStructured(t *testing.T) {
	runner := writingRunner(t, "README.md")
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "change the readme", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	writeTestFile(t, rt.RepoPath, "README.md", "# host version\n")

	tool := &ProposalTool{Proposals: rt.Proposals}
	out, err := tool.Execute(context.Background(), `{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("apply refusal must be a structured result, got tool error: %v", err)
	}
	var got proposalEnvelope
	if uerr := json.Unmarshal([]byte(out), &got); uerr != nil {
		t.Fatalf("apply refusal is not JSON: %v\n%s", uerr, out)
	}
	if got.Status != "conflict" {
		t.Errorf("status = %q, want conflict", got.Status)
	}
	if got.ProposalID != env.ProposalID || got.Action != "apply" {
		t.Errorf("envelope = %+v, want the refused proposal and apply action", got)
	}
	if !strings.Contains(got.Error, "path-disjoint") || !strings.Contains(got.Error, "README.md") {
		t.Errorf("error = %q, want a path-disjoint refusal naming README.md", got.Error)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one apply conflict", got.Conflicts)
	}
	conflict := got.Conflicts[0]
	if conflict.Kind != proposalConflictApply {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictApply)
	}
	if len(conflict.Paths) != 1 || conflict.Paths[0] != "README.md" {
		t.Errorf("conflict paths = %v, want README.md", conflict.Paths)
	}
	if len(conflict.Proposals) != 1 || conflict.Proposals[0] != env.ProposalID {
		t.Errorf("conflict proposals = %v, want %s", conflict.Proposals, env.ProposalID)
	}

	// Refusal must not consume the proposal: the orchestrator can still
	// discard or retry after resolving the parent overlap.
	if _, err := rt.Proposals.get(env.ProposalID); err != nil {
		t.Errorf("a refused apply must leave the proposal open: %v", err)
	}
}

// TestProposalList_ContainerOverlapAndParentDrift covers the container lane's
// list view: its manifest paths and persisted basis feed the same report as a
// worktree proposal.
func TestProposalList_ContainerOverlapAndParentDrift(t *testing.T) {
	f := newDeltaFixture(t)
	f.add(t, "proposal:container-one", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# one\n"),
	}}})
	f.add(t, "proposal:container-two", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# two\n"),
	}}})

	writeTestFile(t, f.repo, "host-only.txt", "host change\n")

	out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 1 || len(listed.Conflicts[0].Paths) != 1 || listed.Conflicts[0].Paths[0] != "README.md" {
		t.Fatalf("conflicts = %+v, want one README.md overlap", listed.Conflicts)
	}
	for _, summary := range listed.Proposals {
		if !containsString(summary.ParentDrift, "host-only.txt") {
			t.Errorf("summary %s parent_drift = %v, want host-only.txt", summary.ProposalID, summary.ParentDrift)
		}
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
