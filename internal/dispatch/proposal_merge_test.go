package dispatch

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposalTool_ThreeWayApplyMergesNonOverlappingHunks(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\nline4\nline5\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "multi-line readme")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\nline4\nline5\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	// The parent moves the last line, a non-overlapping hunk.
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\nline4\nhost5\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`,"strategy":"three_way"}`)
	if err != nil {
		t.Fatalf("three-way apply: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "settled" || got.Action != "apply" || got.Strategy != proposalApplyThreeWay {
		t.Fatalf("apply envelope = %+v, want settled apply/three_way", got)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read merged README: %v", err)
	}
	text := string(data)
	for _, want := range []string{"proposal1", "line2", "line3", "line4", "host5"} {
		if !strings.Contains(text, want) {
			t.Errorf("merged README missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<<<<<<<") || strings.Contains(text, "=======") {
		t.Errorf("merged README contains conflict markers:\n%s", text)
	}
}

func TestGitMergeFile_MultipleConflictHunks(t *testing.T) {
	base := []byte("a\nb\nc\nd\ne\n")
	current := []byte("a1\nb\nc\nd\ne1\n")
	proposed := []byte("a2\nb\nc\nd\ne2\n")
	merged, ok, err := gitMergeFile(context.Background(), base, current, proposed)
	if err != nil {
		t.Fatalf("gitMergeFile: %v", err)
	}
	if ok || merged != nil {
		t.Fatal("two conflict hunks must fail closed")
	}
}

// TestProposalPathDriftedIgnoresFalseNegativeHint pins the invariant behind
// yaah-610z: the parent drift list is a hint, and a hint that misses a path
// must never authorize a whole-path overwrite of the parent's own edit.
func TestProposalPathDriftedIgnoresFalseNegativeHint(t *testing.T) {
	base := []byte("line1\nline2\nline3\n")
	host := []byte("host1\nline2\nline3\n")
	tests := []struct {
		name          string
		hinted        bool
		base          []byte
		baseExists    bool
		current       []byte
		currentExists bool
		want          bool
	}{
		{name: "hint only, parent unmoved", hinted: true, base: base, baseExists: true, current: base, currentExists: true, want: true},
		{name: "no hint, parent unmoved", base: base, baseExists: true, current: base, currentExists: true, want: false},
		{name: "no hint, same-size parent rewrite", base: base, baseExists: true, current: host, currentExists: true, want: true},
		{name: "no hint, parent deleted the path", base: base, baseExists: true, current: nil, currentExists: false, want: true},
		{name: "no hint, parent created the path", base: nil, baseExists: false, current: host, currentExists: true, want: true},
		{name: "no hint, path on neither side", base: nil, baseExists: false, current: nil, currentExists: false, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := proposalPathDrifted(tc.hinted, "notes.txt", nil, tc.base, tc.baseExists, tc.current, tc.currentExists); got != tc.want {
				t.Errorf("proposalPathDrifted = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProposalPathDriftedFoldsCRLF pins the EOL fix (PR #824 review): base is a
// raw tree blob while current is the working-tree file, so under core.autocrlf
// or an eol smudge filter an unchanged path is LF in the blob and CRLF on disk.
// A byte-for-byte comparison called every such path drifted and forced a
// three-way merge on a path the parent never touched.
//
// Folding unconditionally went too far the other way (PR #824 review, finding
// 3): in a repo that does not normalize, an EOL-only difference is a real
// parent edit, and folding it away let the no-drift fast path overwrite the
// parent's whole-path content. So the fold is asked for by the repo's policy.
func TestProposalPathDriftedFoldsCRLF(t *testing.T) {
	lf := []byte("line1\nline2\nline3\n")
	// An autocrlf checkout: git is expected to have smudged the worktree file.
	normalizing := &eolNormalization{autocrlf: true}
	// A repo with no autocrlf and no .gitattributes rule.
	raw := &eolNormalization{}

	t.Run("crlf worktree file against an lf blob is not drifted", func(t *testing.T) {
		crlf := []byte("line1\r\nline2\r\nline3\r\n")
		if proposalPathDrifted(false, "README.md", normalizing, lf, true, crlf, true) {
			t.Error("EOL translation made an unchanged path look drifted")
		}
	})

	t.Run("eol-only edit in a non-normalizing repo is drifted", func(t *testing.T) {
		crlf := []byte("line1\r\nline2\r\nline3\r\n")
		if !proposalPathDrifted(false, "README.md", raw, lf, true, crlf, true) {
			t.Error("a repo that does not normalize must treat an EOL-only difference as a parent edit")
		}
	})

	t.Run("an actual edit is still drifted under crlf", func(t *testing.T) {
		edited := []byte("line1\r\nhost2\r\nline3\r\n")
		if !proposalPathDrifted(false, "README.md", normalizing, lf, true, edited, true) {
			t.Error("a changed line must still classify as drifted")
		}
	})

	t.Run("binary content is compared byte for byte", func(t *testing.T) {
		base := []byte("a\r\nb\x00\x01")
		if !proposalPathDrifted(false, "img.bin", normalizing, base, true, []byte("a\nb\x00\x01"), true) {
			t.Error("a binary blob must not be folded into equality")
		}
	})

	t.Run("a lone CR is content, not an eol form", func(t *testing.T) {
		oldMac := []byte("line1\rline2\rline3\r")
		if !proposalPathDrifted(false, "README.md", normalizing, lf, true, oldMac, true) {
			t.Error("folding must only drop the CR of a CRLF pair")
		}
	})
}

// TestProposalPathDriftedProbesEOLPolicyLazily pins PR #824 review round 3,
// finding 2: the .gitattributes probe is a subprocess, and the answer can only
// change the verdict when the raw bytes differ while the folded content
// already matches. So a byte-identical path — the ordinary case in a settle —
// and a path whose edit is more than line endings must not probe at all, and
// the one path that does probe is cached.
func TestProposalPathDriftedProbesEOLPolicyLazily(t *testing.T) {
	lf := []byte("line1\nline2\nline3\n")
	crlf := []byte("line1\r\nline2\r\nline3\r\n")
	edited := []byte("line1\nhost2\nline3\n")
	binary := []byte("line1\nline2\x00\x01")

	// policy counts attribute probes and always answers with rule.
	policy := func(rule eolRule, probes *int) *eolNormalization {
		return &eolNormalization{lookup: func(string) eolRule {
			(*probes)++
			return rule
		}}
	}

	t.Run("byte-identical path probes nothing", func(t *testing.T) {
		probes := 0
		if proposalPathDrifted(false, "README.md", policy(eolRuleNormalizes, &probes), lf, true, lf, true) {
			t.Fatal("identical bytes are not drift")
		}
		if probes != 0 {
			t.Errorf("byte-identical path ran %d attribute probe(s), want 0", probes)
		}
	})

	t.Run("an edit beyond line endings probes nothing", func(t *testing.T) {
		probes := 0
		if !proposalPathDrifted(false, "README.md", policy(eolRuleNormalizes, &probes), lf, true, edited, true) {
			t.Fatal("a changed line is drift")
		}
		if probes != 0 {
			t.Errorf("a non-EOL edit ran %d attribute probe(s), want 0", probes)
		}
	})

	t.Run("binary content probes nothing", func(t *testing.T) {
		probes := 0
		folded := []byte("line1\r\nline2\r\nline3\r\n")
		if !proposalPathDrifted(false, "img.bin", policy(eolRuleNormalizes, &probes), binary, true, folded, true) {
			t.Fatal("binary content is compared byte for byte")
		}
		if probes != 0 {
			t.Errorf("binary path ran %d attribute probe(s), want 0", probes)
		}
	})

	t.Run("an eol-only difference probes once and is not drift when normalizing", func(t *testing.T) {
		probes := 0
		p := policy(eolRuleNormalizes, &probes)
		if proposalPathDrifted(false, "README.md", p, lf, true, crlf, true) {
			t.Fatal("a normalized EOL-only difference is not drift")
		}
		if proposalPathDrifted(false, "README.md", p, lf, true, crlf, true) {
			t.Fatal("the second question must get the same answer")
		}
		if probes != 1 {
			t.Errorf("probed %d time(s), want the per-path answer cached after 1", probes)
		}
	})

	t.Run("an eol-only difference on a -text path is drift", func(t *testing.T) {
		probes := 0
		if !proposalPathDrifted(false, "blob.bin", policy(eolRuleRaw, &probes), lf, true, crlf, true) {
			t.Fatal("a raw path treats an EOL-only difference as a parent edit")
		}
		if probes != 1 {
			t.Errorf("probed %d time(s), want 1", probes)
		}
	})
}

func TestProposalModeFromString(t *testing.T) {
	tests := []fs.FileMode{
		0o644,
		0o600,
		0,
		0o755 | fs.ModeSetuid,
		0o755 | fs.ModeSetgid | fs.ModeSetuid,
		0o755 | fs.ModeSticky,
	}
	for _, want := range tests {
		in := want.String()
		got, ok := proposalModeFromString(in)
		if !ok || got != want {
			t.Errorf("proposalModeFromString(%q) = %04o/%v, want %04o/true", in, got, ok, want)
		}
	}
	if got, ok := proposalModeFromString(""); ok || got != 0 {
		t.Errorf("malformed mode = %04o/%v, want 0/false", got, ok)
	}
	if got, ok := proposalModeFromString("----------"); !ok || got != 0 {
		t.Errorf("recorded 0000 mode = %04o/%v, want 0/true", got, ok)
	}
}

func TestGitTreePathSizeDistinguishesMissingAndInvalid(t *testing.T) {
	_, repo, _ := newSessionFixtures(t)
	if _, ok, err := gitTreePathSize(context.Background(), repo, "HEAD", "README.md"); err != nil || !ok {
		t.Fatalf("existing path: ok=%v err=%v", ok, err)
	}
	if _, ok, err := gitTreePathSize(context.Background(), repo, "HEAD", "does-not-exist"); err != nil || ok {
		t.Fatalf("missing path: ok=%v err=%v", ok, err)
	}
	if _, _, err := gitTreePathSize(context.Background(), repo, "not-a-tree", "README.md"); err == nil {
		t.Fatal("invalid tree must be an error, not a missing path")
	}
}

func TestProposalTool_ThreeWayApplyFailsClosedOnConflict(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "three-line readme")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	writeTestFile(t, repo, "README.md", "host1\nline2\nline3\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`,"strategy":"three_way"}`)
	if err != nil {
		t.Fatalf("three-way refusal must be a structured result: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "conflict" || got.Strategy != proposalApplyThreeWay {
		t.Fatalf("apply envelope = %+v, want conflict/three_way", got)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil || string(data) != "host1\nline2\nline3\n" {
		t.Fatalf("a refused three-way apply must leave the host untouched: %q err=%v", data, err)
	}
	if _, err := rt.Proposals.get(env.ProposalID); err != nil {
		t.Errorf("refused apply must leave the proposal open: %v", err)
	}
}

// TestProposalTool_ThreeWayApplyOversizedBaseConflicts pins the fail-closed but
// non-fatal handling of a base that cannot be read (PR #824 review). A basis
// blob over the merge cap has no usable merge input: the no-drift fast path
// would overwrite the parent whole-path with no proof it is unmoved, and a
// three-way merge has no base at all. It must not abort the settle either —
// that strands every other path of the proposal — so the path is reported as a
// conflict and the proposal stays open for the human.
func TestProposalTool_ThreeWayApplyOversizedBaseConflicts(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "big.txt", strings.Repeat("x", proposalMergeMaxBytes+1))
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "oversized basis blob")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "big.txt", "proposal\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "shrink the big file", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	// The parent drops the path, so the working-tree side is a delete and the
	// merge cap on a working-tree read is not what fires: the basis blob itself
	// is too large to read.
	if err := os.Remove(filepath.Join(repo, "big.txt")); err != nil {
		t.Fatalf("remove parent big.txt: %v", err)
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`,"strategy":"three_way"}`)
	if err != nil {
		t.Fatalf("an unreadable basis must be a structured conflict, not a failed apply: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "conflict" || got.Strategy != proposalApplyThreeWay {
		t.Fatalf("apply envelope = %+v, want conflict/three_way", got)
	}
	if len(got.Conflicts) != 1 || len(got.Conflicts[0].Paths) != 1 || got.Conflicts[0].Paths[0] != "big.txt" {
		t.Fatalf("conflict must name the unbasisable path, got %+v", got.Conflicts)
	}
	if !strings.Contains(got.Error, "no merge basis") || !strings.Contains(got.Error, "merge cap") {
		t.Errorf("refusal must say why the base could not be read: %q", got.Error)
	}
	if _, err := os.Stat(filepath.Join(repo, "big.txt")); !os.IsNotExist(err) {
		t.Errorf("a conflicted path must be left untouched, stat err = %v", err)
	}
	if _, err := rt.Proposals.get(env.ProposalID); err != nil {
		t.Errorf("refused apply must leave the proposal open: %v", err)
	}
}

func TestProposalTool_ThreeWayApplyRejectsContainerLane(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:three-way-container", &GitDelta{Changes: []DeltaChange{{
		Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("container\n"),
	}}})
	out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(p.id)+`,"strategy":"three_way"}`)
	if err == nil {
		t.Fatalf("container three-way apply must be refused, got %s", out)
	}
	if !strings.Contains(err.Error(), "worktree-lane only") {
		t.Errorf("error = %v, want worktree-lane refusal", err)
	}
	if _, err := f.registry.get(p.id); err != nil {
		t.Errorf("refused strategy must leave the proposal open: %v", err)
	}
}

func TestProposalTool_DefaultApplyStillPathDisjoint(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:default-apply", &GitDelta{Changes: []DeltaChange{{
		Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("proposal\n"),
	}}})
	writeTestFile(t, f.repo, "README.md", "host\n")
	out, err := (&ProposalTool{Proposals: f.registry}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(p.id)+`}`)
	if err != nil {
		t.Fatalf("default apply refusal must be structured: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "conflict" || got.Strategy != proposalApplyPathDisjoint {
		t.Fatalf("envelope = %+v, want path_disjoint conflict", got)
	}
}

func TestThreeWayApplyRecordsStrategyFact(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "one\ntwo\nthree\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "three-line")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal-one\ntwo\nthree\n")
	}
	rt := RetainedRuntime{Runner: runner.run(), RepoPath: repo, ScopeManager: fx.mgr, RootScopeID: rootID, WorktreeRoot: t.TempDir(), Proposals: NewProposalRegistry()}
	res, err := StartRetainedRun(context.Background(), rt, "edit first", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	writeTestFile(t, repo, "README.md", "one\ntwo\nhost-three\n")
	if _, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`,"strategy":"three_way"}`); err != nil {
		t.Fatalf("apply: %v", err)
	}
	parent, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatal("root scope missing")
	}
	facts := findRetainedFacts(t, fx.store, parent.OwnerID(), SchemaProposalSettled)
	found := false
	for _, fact := range facts {
		var payload struct {
			Action   string `json:"action"`
			Strategy string `json:"strategy"`
		}
		data, _ := json.Marshal(fact.Body.Payload)
		if json.Unmarshal(data, &payload) == nil && payload.Action == "apply" && payload.Strategy == proposalApplyThreeWay {
			found = true
		}
	}
	if !found {
		t.Fatalf("no settled fact recorded apply/three_way: %+v", facts)
	}
}

// TestProposalTool_PathDisjointApplyRefusesParentEdit pins yaah-gkll.2: an
// uncommitted parent edit to a proposal-owned path must never be silently
// overwritten by a path_disjoint apply. Pre-fix, the worktree lane trusted
// the kernel's drift capture — documented unreliable for working-tree
// rewrites (yaah-610z) — and the no-conflict path wrote whole-path
// post-images over the parent's edit, reporting a clean settle.
func TestProposalTool_PathDisjointApplyRefusesParentEdit(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\n")
	writeTestFile(t, repo, "OTHER.txt", "other base\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "base")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	// The parent edits the proposal-owned path AFTER the dispatch.
	writeTestFile(t, repo, "README.md", "parent-edit\nline2\nline3\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "conflict" || got.Action != "apply" || got.Strategy != proposalApplyPathDisjoint {
		t.Fatalf("apply envelope = %+v, want a conflict apply/path_disjoint", got)
	}
	var named []string
	for _, c := range got.Conflicts {
		named = append(named, c.Paths...)
	}
	if !containsPath(named, "README.md") {
		t.Errorf("conflict does not name the drifted proposal path; paths: %v", named)
	}
	// The parent's edit must still be on disk — the refusal happened
	// pre-write.
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(data), "parent-edit") {
		t.Errorf("the parent's edit was overwritten despite the refusal:\n%s", data)
	}
}

// TestProposalTool_PathDisjointApplyWritesOnUnrelatedParentEdit pins the
// other side of the guard: a parent edit to a path the proposal does NOT
// own is a move onto a moved tree, exactly what path_disjoint apply is for.
func TestProposalTool_PathDisjointApplyWritesOnUnrelatedParentEdit(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\n")
	writeTestFile(t, repo, "OTHER.txt", "other base\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "base")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	// Parent edits an UNOWNED path after the dispatch.
	writeTestFile(t, repo, "OTHER.txt", "parent moved elsewhere\n")

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "settled" {
		t.Fatalf("apply envelope = %+v, want settled: an unrelated parent edit must not trip the guard", got)
	}
	readme, _ := os.ReadFile(filepath.Join(repo, "README.md"))
	if !strings.Contains(string(readme), "proposal1") {
		t.Errorf("proposal content missing after apply:\n%s", readme)
	}
	other, _ := os.ReadFile(filepath.Join(repo, "OTHER.txt"))
	if !strings.Contains(string(other), "parent moved elsewhere") {
		t.Errorf("parent's unrelated edit lost:\n%s", other)
	}
}

// TestProposalTool_PathDisjointApplyIgnoresPreExistingDirt pins the basis
// semantics: the dispatch basis captures the dirty working tree, so a
// pre-dispatch parent edit to a proposal-owned path is part of what the
// proposal built on — not drift, not a refusal.
func TestProposalTool_PathDisjointApplyIgnoresPreExistingDirt(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "base")
	// Pre-dispatch dirt on the path the proposal will own.
	writeTestFile(t, repo, "README.md", "dirty base\nline2\nline3\n")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"apply","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got proposalEnvelope
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("apply result is not JSON: %v\n%s", err, out)
	}
	if got.Status != "settled" {
		t.Fatalf("apply envelope = %+v, want settled: pre-dispatch dirt is part of the basis, not drift", got)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want || strings.HasSuffix(strings.TrimPrefix(p, "./"), want) {
			return true
		}
	}
	return false
}

// TestDriftedApplyPathsDecidesOnContent pins the guard's authority: the
// parentDrift hint (kernel-side capture) can only widen the drifted set,
// never narrow it. With the hint machinery unavailable altogether, a
// same-size parent rewrite of a proposal-owned path is still detected —
// the guard cannot be blinded by the unreliable capture (yaah-610z,
// yaah-gkll.2).
func TestDriftedApplyPathsDecidesOnContent(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	writeTestFile(t, repo, "README.md", "line1\nline2\nline3\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "base")

	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		writeTestFile(t, runner.workdirFor(t, call), "README.md", "proposal1\nline2\nline3\n")
	}
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := StartRetainedRun(context.Background(), rt, "change the first line", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	p, err := rt.Proposals.get(env.ProposalID)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}

	// No parent movement: nothing drifted.
	drifted, err := p.driftedApplyPaths(context.Background())
	if err != nil || len(drifted) != 0 {
		t.Fatalf("clean parent: drifted=%v err=%v, want none", drifted, err)
	}

	// Same-size parent rewrite of the owned path...
	writeTestFile(t, repo, "README.md", "hostx\nline2\nline3\n")
	// ...with the hint machinery taken away entirely.
	savedMgr := p.mgr
	p.mgr = nil
	defer func() { p.mgr = savedMgr }()
	if _, err := p.parentDrift(context.Background()); err == nil {
		t.Fatal("fixture expected parentDrift to be unavailable with a nil scope manager")
	}
	drifted, err = p.driftedApplyPaths(context.Background())
	if err != nil {
		t.Fatalf("guard errored: %v", err)
	}
	if !containsPath(drifted, "README.md") {
		t.Fatalf("guard missed a content drift with the hint unavailable: %v (yaah-610z fail-open)", drifted)
	}
}
