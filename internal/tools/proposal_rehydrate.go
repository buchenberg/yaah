package tools

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	goatherd "github.com/buchenberg/goatherd-runtime"
)

// LoadProposalRegistry rebuilds the container-proposal registry from the
// durable artifacts under <repo>/.yaah/proposals. It is scoped to one session
// by the durable session id recorded at seal (the old parent scope id is the
// fallback for artifacts written before that field existed), so two sessions in
// the same repository never see each other's proposals. The rehydrated
// proposal is bound to the *current* root scope, because a restart gives the
// session a new one while the host basis is persisted with the manifest.
//
// Bad artifacts are reported and skipped, never applied: a manifest without a
// matching basis, a digest mismatch, or an interrupted-settlement marker is a
// reason to stop and inspect, not to guess. A proposal that was already settled
// (settled marker present, or its directory removed after the host write) is
// consumed rather than resurrected.
func LoadProposalRegistry(repoPath string, mgr *goatherd.ScopeManager, rootScopeID, sessionID string) (*ProposalRegistry, []string) {
	reg := NewProposalRegistry()
	if strings.TrimSpace(repoPath) == "" {
		return reg, []string{"proposal rehydrate: no repository path — container proposals are not visible"}
	}
	if rootScopeID == "" {
		return reg, []string{"proposal rehydrate: no root scope — container proposals are not visible"}
	}
	root := filepath.Join(repoPath, ".yaah", "proposals")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return reg, []string{fmt.Sprintf("proposal rehydrate: read %s: %v", root, err)}
	}

	var warnings []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		p, warn, err := loadGitDeltaProposal(dir, repoPath, mgr, rootScopeID, sessionID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("proposal rehydrate: %s: %v", dir, err))
			continue
		}
		if p == nil {
			if warn != "" {
				warnings = append(warnings, warn)
			}
			continue
		}
		reg.add(p)
	}
	return reg, warnings
}

// loadGitDeltaProposal rehydrates one artifact directory. A nil proposal with
// no error means the directory was intentionally consumed (settled marker) and
// is not a problem; a nil proposal with a warning means it was quarantined.
func loadGitDeltaProposal(dir, repoPath string, mgr *goatherd.ScopeManager, rootScopeID, sessionID string) (*retainedProposal, string, error) {
	var meta proposalMeta
	if err := readJSON(filepath.Join(dir, proposalMetaFile), &meta); err != nil {
		return nil, "", fmt.Errorf("read metadata: %w", err)
	}
	if meta.ID == "" {
		return nil, "", errors.New("metadata has no proposal id")
	}
	if meta.World != "container" {
		return nil, fmt.Sprintf("proposal %s: world %q is not a container proposal — not rehydrated", meta.ID, meta.World), nil
	}
	// A proposal belongs to the session that sealed it. The parent scope id
	// changes across a restart, so the durable session id is the key; the old
	// parent id is the fallback for artifacts sealed before this field existed.
	switch {
	case sessionID != "" && meta.SessionID != "":
		if meta.SessionID != sessionID {
			return nil, "", nil
		}
	case sessionID == "" && meta.SessionID != "" && meta.ParentScopeID != rootScopeID:
		return nil, fmt.Sprintf("proposal %s: session id unavailable and the root scope differs; not rehydrated", meta.ID), nil
	case meta.SessionID == "" && meta.ParentScopeID != rootScopeID:
		// A legacy artifact from another session: not ours to settle.
		return nil, "", nil
	}
	if want := proposalArtifactDir(repoPath, meta.ID); filepath.Clean(want) != filepath.Clean(dir) {
		return nil, "", fmt.Errorf("metadata id %q does not match its directory name", meta.ID)
	}

	if _, err := os.Stat(filepath.Join(dir, proposalSettled)); err == nil {
		// The host write already happened; the crash happened between the
		// marker and the removal. Consume the leftovers instead of
		// resurrecting a proposal that could be applied twice.
		if rerr := os.RemoveAll(dir); rerr != nil {
			return nil, fmt.Sprintf("proposal %s: settled marker found but cleanup failed: %v", meta.ID, rerr), nil
		}
		return nil, "", nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("check settled marker: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, proposalApplying)); err == nil {
		return nil, fmt.Sprintf("proposal %s: interrupted settlement marker present at %s — inspect the host tree and remove the artifact manually; not rehydrated", meta.ID, dir), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("check applying marker: %w", err)
	}
	if meta.Target == "session" {
		return nil, fmt.Sprintf("proposal %s: session-target proposals are session-lifetime (the container they settle into is gone) — not rehydrated", meta.ID), nil
	}

	deltaData, err := os.ReadFile(filepath.Join(dir, proposalDeltaFile))
	if err != nil {
		return nil, "", fmt.Errorf("read manifest: %w", err)
	}
	sum := sha256.Sum256(deltaData)
	if meta.ManifestDigest == "" || fmt.Sprintf("%x", sum) != meta.ManifestDigest {
		return nil, "", fmt.Errorf("manifest digest does not match metadata (persisted %s, computed %x)", meta.ManifestDigest, sum)
	}
	var delta GitDelta
	if err := json.Unmarshal(deltaData, &delta); err != nil {
		return nil, "", fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateDelta(&delta); err != nil {
		return nil, "", fmt.Errorf("validate manifest: %w", err)
	}

	var basis proposalBasis
	if err := readJSON(filepath.Join(dir, proposalBasisFile), &basis); err != nil {
		return nil, "", fmt.Errorf("read host basis: %w", err)
	}
	if basis.State.Backend != "git" {
		return nil, "", fmt.Errorf("host basis backend is %q; container settlement needs the git host root", basis.State.Backend)
	}
	computed, derr := basis.State.Digest()
	if derr != nil {
		return nil, "", fmt.Errorf("digest host basis: %w", derr)
	}
	if basis.Digest == "" || computed != basis.Digest {
		return nil, "", fmt.Errorf("host basis digest %s does not match the persisted state (%s)", basis.Digest, computed)
	}

	review := ""
	if data, err := os.ReadFile(filepath.Join(dir, proposalReviewFile)); err == nil {
		review = string(data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("read review: %w", err)
	}

	return &retainedProposal{
		id:          meta.ID,
		delta:       &delta,
		review:      review,
		result:      meta.Result,
		runErr:      meta.RunErr,
		parentID:    rootScopeID,
		mgr:         mgr,
		repoPath:    repoPath,
		basisState:  basis.State,
		basisDigest: basis.Digest,
		artifactDir: dir,
		deltaDigest: meta.ManifestDigest,
		world:       "container",
	}, "", nil
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
