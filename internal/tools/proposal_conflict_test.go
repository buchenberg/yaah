package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProposalList_DivergentOverlapAndParentDrift pins the Phase 1 typed model
// (yaah-6xyl.1): two proposals that produce different post-images for the same
// path are same_path_divergent, and the envelope retains the digests/modes that
// prove it.
func TestProposalList_DivergentOverlapAndParentDrift(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", fmt.Sprintf("# approach %d\n", call))
	}
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

	// The parent moves after both dispatch bases were captured; the drift is
	// disjoint from README.md and therefore is not a parent_overlap.
	writeTestFile(t, rt.RepoPath, "host-only.txt", "host change\n")

	tool := &ProposalTool{Proposals: rt.Proposals}
	out, err := tool.Execute(context.Background(), `{"action":"list"}`)
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
		t.Fatalf("conflicts = %+v, want one divergent conflict", listed.Conflicts)
	}
	conflict := listed.Conflicts[0]
	if conflict.Kind != proposalConflictSamePathDivergent {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictSamePathDivergent)
	}
	if got := strings.Join(conflict.Paths, ","); got != "README.md" {
		t.Errorf("conflict paths = %v, want README.md", conflict.Paths)
	}
	if !containsString(conflict.Proposals, firstEnv.ProposalID) || !containsString(conflict.Proposals, secondEnv.ProposalID) {
		t.Errorf("conflict proposals = %v, want both %s and %s", conflict.Proposals, firstEnv.ProposalID, secondEnv.ProposalID)
	}
	if len(conflict.Changes) != 2 {
		t.Fatalf("conflict changes = %+v, want both post-images", conflict.Changes)
	}
	if conflict.Changes[0].Digest == "" || conflict.Changes[1].Digest == "" {
		t.Errorf("conflict changes missing digests: %+v", conflict.Changes)
	}
	if conflict.Changes[0].Digest == conflict.Changes[1].Digest {
		t.Errorf("divergent changes share a digest: %+v", conflict.Changes)
	}
	if conflict.Changes[0].BaseDigest == "" || conflict.Changes[1].BaseDigest == "" {
		t.Errorf("conflict changes missing base digests: %+v", conflict.Changes)
	}
	if len(conflict.Details) != 2 {
		t.Fatalf("conflict details = %+v, want base/current/proposed for both proposals", conflict.Details)
	}
	for _, detail := range conflict.Details {
		if detail.Base == "" || detail.Current == "" || detail.Proposed == "" {
			t.Errorf("detail = %+v, want base/current/proposed content", detail)
		}
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
		if len(summary.Changes) != 1 || summary.Changes[0].Digest == "" || summary.Changes[0].Mode == "" {
			t.Errorf("summary %s changes = %+v, want one digested change with mode", summary.ProposalID, summary.Changes)
		}
		if summary.ChangesError != "" {
			t.Errorf("summary %s changes_error = %q, want none", summary.ProposalID, summary.ChangesError)
		}
	}

	// show must expose the same typed conflict and change metadata.
	showOut, err := tool.Execute(context.Background(), `{"action":"show","proposal_id":`+jsonString(firstEnv.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var shown proposalEnvelope
	if err := json.Unmarshal([]byte(showOut), &shown); err != nil {
		t.Fatalf("show result is not JSON: %v\n%s", err, showOut)
	}
	if len(shown.Changes) != 1 || shown.Changes[0].Digest == "" {
		t.Errorf("show changes = %+v, want one digested change", shown.Changes)
	}
	if len(shown.Conflicts) != 1 || shown.Conflicts[0].Kind != proposalConflictSamePathDivergent {
		t.Errorf("show conflicts = %+v, want the divergent pair", shown.Conflicts)
	}
	if len(shown.Conflicts) == 1 && len(shown.Conflicts[0].Details) != 2 {
		t.Errorf("show conflict details = %+v, want two views", shown.Conflicts[0].Details)
	}
}

// TestProposalList_IdenticalDuplicateIsNotConflict pins the content-aware
// distinction from Phase 0: two proposals producing the same post-image are a
// benign duplicate, not a conflict.
func TestProposalList_IdenticalDuplicateIsNotConflict(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "README.md"))

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

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want identical duplicates to be non-conflicts", listed.Conflicts)
	}
	if len(listed.Duplicates) != 1 || listed.Duplicates[0].Kind != proposalConflictSamePathIdentical {
		t.Fatalf("duplicates = %+v, want one same_path_identical entry", listed.Duplicates)
	}

	var digests []string
	for _, summary := range listed.Proposals {
		if summary.ProposalID != firstEnv.ProposalID && summary.ProposalID != secondEnv.ProposalID {
			t.Fatalf("unexpected summary %+v", summary)
		}
		if len(summary.Conflicts) != 0 {
			t.Errorf("summary %s conflicts = %+v, want none", summary.ProposalID, summary.Conflicts)
		}
		if len(summary.Duplicates) != 1 || summary.Duplicates[0].Kind != proposalConflictSamePathIdentical {
			t.Errorf("summary %s duplicates = %+v, want one same_path_identical entry", summary.ProposalID, summary.Duplicates)
		}
		if len(summary.Changes) != 1 || summary.Changes[0].Digest == "" {
			t.Fatalf("summary %s changes = %+v, want one digested change", summary.ProposalID, summary.Changes)
		}
		digests = append(digests, summary.Changes[0].Digest)
	}
	if len(digests) != 2 || digests[0] != digests[1] {
		t.Errorf("identical duplicate digests = %v, want equal", digests)
	}
}

// TestProposalList_DeleteModify pins the delete/modify classification across
// the worktree lane.
func TestProposalList_DeleteModify(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		wd := runner.workdirFor(t, call)
		if call == 0 {
			if err := os.Remove(filepath.Join(wd, "README.md")); err != nil {
				t.Fatalf("remove README: %v", err)
			}
			return
		}
		writeTestFile(t, wd, "README.md", "# modified\n")
	}
	rt := newRetainedTestEnv(t, runner)

	if _, err := startRetainedRun(context.Background(), rt, "delete it", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("delete dispatch: %v", err)
	}
	if _, err := startRetainedRun(context.Background(), rt, "modify it", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("modify dispatch: %v", err)
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one delete/modify conflict", listed.Conflicts)
	}
	conflict := listed.Conflicts[0]
	if conflict.Kind != proposalConflictDeleteModify {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictDeleteModify)
	}
	kinds := map[string]bool{}
	for _, change := range conflict.Changes {
		kinds[change.Kind] = true
	}
	if !kinds["delete"] || !kinds["modify"] {
		t.Errorf("conflict changes = %+v, want a delete and a modify", conflict.Changes)
	}
}

// TestProposalList_PerPathInspectionFailure pins the per-path fallback: one
// unreadable post-image must not discard the metadata for the proposal's other
// changes.
func TestProposalList_PerPathInspectionFailure(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		wd := runner.workdirFor(t, call)
		writeTestFile(t, wd, "readable.txt", "readable\n")
		writeTestFile(t, wd, "missing.txt", "will disappear\n")
	}
	rt := newRetainedTestEnv(t, runner)
	if _, err := startRetainedRun(context.Background(), rt, "two files", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	proposals := rt.Proposals.all()
	if len(proposals) != 1 {
		t.Fatalf("proposals = %d, want 1", len(proposals))
	}
	if err := os.Remove(filepath.Join(proposals[0].workdir, "missing.txt")); err != nil {
		t.Fatalf("remove worktree file: %v", err)
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Proposals) != 1 {
		t.Fatalf("proposals = %+v, want one", listed.Proposals)
	}
	summary := listed.Proposals[0]
	if summary.ChangesError == "" {
		t.Fatal("changes_error must report the unreadable path")
	}
	byPath := map[string]proposalChange{}
	for _, change := range summary.Changes {
		byPath[change.Path] = change
	}
	if got := byPath["readable.txt"]; got.Digest == "" || got.Mode == "" {
		t.Errorf("readable.txt metadata = %+v, want digest and mode retained", got)
	}
	if got := byPath["missing.txt"]; got.Digest != "" || got.Mode != "" {
		t.Errorf("missing.txt metadata = %+v, want path-only fallback", got)
	}
}

// TestProposalList_WorktreeHashCap pins the bounded worktree read: an oversized
// post-image is not loaded, and its metadata falls back to path-only.
func TestProposalList_WorktreeHashCap(t *testing.T) {
	runner := writingRunner(t, "big.txt")
	rt := newRetainedTestEnv(t, runner)
	if _, err := startRetainedRun(context.Background(), rt, "write a big file", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	proposals := rt.Proposals.all()
	if len(proposals) != 1 {
		t.Fatalf("proposals = %d, want 1", len(proposals))
	}
	if err := os.Truncate(filepath.Join(proposals[0].workdir, "big.txt"), proposalWorktreeMaxHashBytes+1); err != nil {
		t.Fatalf("truncate worktree file: %v", err)
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Proposals) != 1 {
		t.Fatalf("proposals = %+v, want one", listed.Proposals)
	}
	summary := listed.Proposals[0]
	if !strings.Contains(summary.ChangesError, "digest cap") {
		t.Errorf("changes_error = %q, want the digest cap", summary.ChangesError)
	}
	if len(summary.Changes) != 1 || summary.Changes[0].Digest != "" {
		t.Errorf("changes = %+v, want one path-only change for the oversized file", summary.Changes)
	}
}

// TestProposalList_ParentOverlapIsTyped pins parent_overlap: a proposal path the
// parent changed after the dispatch basis is a typed conflict, even when no
// second proposal exists.
func TestProposalList_ParentOverlapIsTyped(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "README.md"))

	res, err := startRetainedRun(context.Background(), rt, "change the readme", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	writeTestFile(t, rt.RepoPath, "README.md", "# host version\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one parent_overlap", listed.Conflicts)
	}
	conflict := listed.Conflicts[0]
	if conflict.Kind != proposalConflictParentOverlap {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictParentOverlap)
	}
	if len(conflict.Proposals) != 1 || conflict.Proposals[0] != env.ProposalID {
		t.Errorf("conflict proposals = %v, want %s", conflict.Proposals, env.ProposalID)
	}
	if len(conflict.Paths) != 1 || conflict.Paths[0] != "README.md" {
		t.Errorf("conflict paths = %v, want README.md", conflict.Paths)
	}
}

// TestProposalList_RecordsConflictFact pins the trace contract: a detected
// conflict records yaah.proposal.conflict.v1 with paths, proposals, kind, and
// per-proposal digests. The registry deduplicates repeated list observations.
func TestProposalList_RecordsConflictFact(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", fmt.Sprintf("# approach %d\n", call))
	}
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	if _, err := startRetainedRun(context.Background(), rt, "first approach", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if _, err := startRetainedRun(context.Background(), rt, "second approach", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}

	tool := &ProposalTool{Proposals: rt.Proposals}
	if _, err := tool.Execute(context.Background(), `{"action":"list"}`); err != nil {
		t.Fatalf("list: %v", err)
	}

	parent, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatalf("root scope %s not found", rootID)
	}
	facts := findRetainedFacts(t, fx.store, parent.OwnerID(), SchemaProposalConflict)
	if len(facts) != 1 {
		t.Fatalf("conflict facts = %d, want 1", len(facts))
	}
	var got struct {
		Kind      string           `json:"kind"`
		Paths     []string         `json:"paths"`
		Proposals []string         `json:"proposals"`
		Changes   []proposalChange `json:"changes"`
	}
	data, err := json.Marshal(facts[0].Body.Payload)
	if err != nil {
		t.Fatalf("marshal conflict payload: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("conflict payload is not JSON: %v\n%s", err, data)
	}
	if got.Kind != proposalConflictSamePathDivergent {
		t.Errorf("fact kind = %q, want %q", got.Kind, proposalConflictSamePathDivergent)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "README.md" {
		t.Errorf("fact paths = %v, want README.md", got.Paths)
	}
	if len(got.Proposals) != 2 {
		t.Errorf("fact proposals = %v, want both", got.Proposals)
	}
	if len(got.Changes) != 2 {
		t.Fatalf("fact changes = %+v, want both post-images", got.Changes)
	}
	for _, change := range got.Changes {
		if change.BaseDigest == "" || change.Digest == "" || change.Mode == "" {
			t.Errorf("fact change missing digest/mode metadata: %+v", change)
		}
	}

	// The same conflict observed again must not append a duplicate fact.
	if _, err := tool.Execute(context.Background(), `{"action":"list"}`); err != nil {
		t.Fatalf("second list: %v", err)
	}
	if facts := findRetainedFacts(t, fx.store, parent.OwnerID(), SchemaProposalConflict); len(facts) != 1 {
		t.Errorf("conflict facts after second list = %d, want 1", len(facts))
	}
}

// TestProposalSeal_RecordsConflictFact pins evidence-at-detection: sealing the
// second divergent proposal records the conflict without requiring a later
// list/show call.
func TestProposalSeal_RecordsConflictFact(t *testing.T) {
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", fmt.Sprintf("# approach %d\n", call))
	}
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{Runner: runner.run(), RepoPath: repo, ScopeManager: fx.mgr, RootScopeID: rootID, WorktreeRoot: t.TempDir(), Proposals: NewProposalRegistry()}
	if _, err := startRetainedRun(context.Background(), rt, "first", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if _, err := startRetainedRun(context.Background(), rt, "second", SubAgentParams{Role: "worker"}, 0, false); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	parent, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatal("root scope missing")
	}
	if facts := findRetainedFacts(t, fx.store, parent.OwnerID(), SchemaProposalConflict); len(facts) != 1 {
		t.Fatalf("sealed conflict facts = %d, want 1 without list/show", len(facts))
	}
}

// TestProposalList_ContainerOverlapAndParentDrift covers the container lane's
// typed conflict view.
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
	if len(listed.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one README.md conflict", listed.Conflicts)
	}
	conflict := listed.Conflicts[0]
	if conflict.Kind != proposalConflictSamePathDivergent {
		t.Errorf("conflict kind = %q, want %q", conflict.Kind, proposalConflictSamePathDivergent)
	}
	if len(conflict.Changes) != 2 || conflict.Changes[0].Digest == "" || conflict.Changes[1].Digest == "" {
		t.Errorf("conflict changes = %+v, want both container post-images", conflict.Changes)
	}
	for _, summary := range listed.Proposals {
		if !containsString(summary.ParentDrift, "host-only.txt") {
			t.Errorf("summary %s parent_drift = %v, want host-only.txt", summary.ProposalID, summary.ParentDrift)
		}
		if len(summary.Changes) != 1 || summary.Changes[0].Digest == "" || summary.Changes[0].Mode == "" {
			t.Errorf("summary %s changes = %+v, want container post-image metadata", summary.ProposalID, summary.Changes)
		}
	}
}

func TestProposalList_ContainerIdenticalAndModeDivergence(t *testing.T) {
	t.Run("identical post-images are silent", func(t *testing.T) {
		f := newDeltaFixture(t)
		for _, id := range []string{"proposal:same-one", "proposal:same-two"} {
			f.add(t, id, &GitDelta{Changes: []DeltaChange{{
				Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# same\n"),
			}}})
		}
		out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(), `{"action":"list"}`)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var listed proposalListEnvelope
		if err := json.Unmarshal([]byte(out), &listed); err != nil {
			t.Fatalf("list result is not JSON: %v\n%s", err, out)
		}
		if len(listed.Conflicts) != 0 {
			t.Fatalf("conflicts = %+v, want identical post-images to be duplicates, not conflicts", listed.Conflicts)
		}
		if len(listed.Duplicates) != 1 || listed.Duplicates[0].Kind != proposalConflictSamePathIdentical {
			t.Fatalf("duplicates = %+v, want one same_path_identical entry", listed.Duplicates)
		}
	})

	t.Run("mode divergence conflicts", func(t *testing.T) {
		f := newDeltaFixture(t)
		f.add(t, "proposal:mode-one", &GitDelta{Changes: []DeltaChange{{
			Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# same\n"),
		}}})
		f.add(t, "proposal:mode-two", &GitDelta{Changes: []DeltaChange{{
			Path: "README.md", Kind: deltaKindModify, Mode: 0o755, Content: []byte("# same\n"),
		}}})
		out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(), `{"action":"list"}`)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var listed proposalListEnvelope
		if err := json.Unmarshal([]byte(out), &listed); err != nil {
			t.Fatalf("list result is not JSON: %v\n%s", err, out)
		}
		if len(listed.Conflicts) != 1 || listed.Conflicts[0].Kind != proposalConflictSamePathDivergent {
			t.Fatalf("conflicts = %+v, want one mode-divergence conflict", listed.Conflicts)
		}
	})
}

func TestProposalList_ContainerDeleteModify(t *testing.T) {
	f := newDeltaFixture(t)
	f.add(t, "proposal:delete", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindDelete, Mode: 0o644,
	}}})
	f.add(t, "proposal:modify", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# modified\n"),
	}}})
	out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 1 || listed.Conflicts[0].Kind != proposalConflictDeleteModify {
		t.Fatalf("conflicts = %+v, want one delete/modify conflict", listed.Conflicts)
	}
}

func TestProposalList_ContainerParentOverlap(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:parent", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# proposal\n"),
	}}})
	writeTestFile(t, f.repo, "README.md", "# host\n")

	out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed proposalListEnvelope
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list result is not JSON: %v\n%s", err, out)
	}
	if len(listed.Conflicts) != 1 || listed.Conflicts[0].Kind != proposalConflictParentOverlap {
		t.Fatalf("conflicts = %+v, want one parent_overlap", listed.Conflicts)
	}
	if len(listed.Conflicts[0].Proposals) != 1 || listed.Conflicts[0].Proposals[0] != p.id {
		t.Errorf("conflict proposals = %v, want %s", listed.Conflicts[0].Proposals, p.id)
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
	if got := strings.TrimSpace(string(fields["duplicates"])); got != "[]" {
		t.Errorf("duplicates = %s, want explicit []", got)
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
	if len(conflict.Changes) != 1 || conflict.Changes[0].Digest == "" {
		t.Errorf("conflict changes = %+v, want the proposal's change metadata", conflict.Changes)
	}
	if len(conflict.Details) != 1 || conflict.Details[0].Current == "" {
		t.Errorf("conflict details = %+v, want the apply refusal's base/current/proposed view", conflict.Details)
	}

	// Refusal must not consume the proposal: the orchestrator can still
	// discard or retry after resolving the parent overlap.
	if _, err := rt.Proposals.get(env.ProposalID); err != nil {
		t.Errorf("a refused apply must leave the proposal open: %v", err)
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
