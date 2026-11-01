package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"

	goatherd "github.com/buchenberg/goatherd-runtime"
)

// proposal_conflict.go implements the typed, content-aware conflict model for
// retained proposals (yaah-6xyl.1). It compares each proposal's changed paths
// and post-images, not only the paths:
//
//   - identical post-images are duplicates, reported separately from conflicts;
//   - different post-images are same_path_divergent;
//   - a delete against a non-delete is delete_modify;
//   - a proposal path the parent changed since its basis is parent_overlap.
//
// The comparison is advisory. Settlement remains authoritative and path-disjoint;
// a proposal can still refuse apply because the parent moved after the list was
// rendered. When it does, the refusal is typed so the tool can render a
// machine-readable conflict result instead of only prose.
//
// Container proposals carry their post-images in the GitDelta manifest. A
// worktree proposal carries only a diff; its post-images are read from the live
// worktree while the proposal is open. A path whose content cannot be inspected
// falls back to a path-only conflict rather than claiming the writes are equal.

const (
	// proposalConflictPathOverlap is the fallback when at least one proposal's
	// post-image could not be inspected. It preserves the Phase 0 warning
	// instead of inventing an identity it cannot prove.
	proposalConflictPathOverlap = "path_overlap"
	// proposalConflictSamePathIdentical is two proposals producing the same
	// post-image. It is emitted in the duplicates view, not the conflicts view,
	// because apply is path-disjoint and only one side can land.
	proposalConflictSamePathIdentical = "same_path_identical"
	// proposalConflictSamePathDivergent is two proposals producing different
	// post-images (content or mode) for one path.
	proposalConflictSamePathDivergent = "same_path_divergent"
	// proposalConflictDeleteModify is a delete from one proposal against a
	// create/modify from another.
	proposalConflictDeleteModify = "delete_modify"
	// proposalConflictParentOverlap is a proposal path the parent changed since
	// the dispatch basis. Apply is path-disjoint, so this blocks apply even when
	// the resulting content would be identical.
	proposalConflictParentOverlap = "parent_overlap"
	// proposalConflictApply is retained as the apply-refusal name; the wire kind
	// is parent_overlap so list and settlement speak the same vocabulary.
	proposalConflictApply = proposalConflictParentOverlap

	// SchemaProposalConflict records a detected conflict on the parent owner
	// path. It is runtime-level proposal metadata, like the sealed/settled
	// facts, not a kernel ABI.
	SchemaProposalConflict = "yaah.proposal.conflict.v1"

	// proposalWorktreeMaxHashBytes bounds the worktree post-image read used
	// for conflict comparison. It matches the container manifest's total
	// content cap, so one large proposed file cannot make list/show allocate
	// without limit. A larger file falls back to path-only classification.
	proposalWorktreeMaxHashBytes = 32 << 20
)

// proposalChange is one normalized path change with the metadata needed to
// compare proposals without re-reading their worlds. BaseDigest is the
// workspace basis both proposals were cut from; Digest is the post-image content
// digest (empty for a delete); Mode is the post-image mode when available.
type proposalChange struct {
	ProposalID string `json:"proposal_id,omitempty"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	BaseDigest string `json:"base_digest,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// proposalConflict is one machine-readable conflict fact. Changes carries the
// per-proposal change metadata that produced the classification.
type proposalConflict struct {
	Kind      string           `json:"kind"`
	Paths     []string         `json:"paths"`
	Proposals []string         `json:"proposals"`
	Changes   []proposalChange `json:"changes,omitempty"`
}

// proposalApplyConflict is the settlement refusal that carries enough data for
// ProposalTool to return a structured conflict envelope. It is an error because
// a direct registry caller must not mistake a refused apply for success.
type proposalApplyConflict struct {
	ProposalID string
	Paths      []string
	// Changes carries the proposal's metadata for the overlapping paths when
	// it could be inspected; it is optional because the refusal must still be
	// machine-readable when the worktree or manifest is already gone.
	Changes []proposalChange
	// Cause preserves the settlement sentinel for direct registry callers.
	// ProposalTool renders the structured envelope; Unwrap keeps
	// errors.Is(err, goatherd.ErrApplyConflict) working underneath it.
	Cause error
}

func (e *proposalApplyConflict) Unwrap() error { return e.Cause }

func (e *proposalApplyConflict) Error() string {
	paths := strings.Join(e.Paths, ", ")
	if paths == "" {
		paths = "overlap paths unavailable"
	}
	return fmt.Sprintf(
		"proposal %s: the parent changed paths this proposal also changes (%s); apply is path-disjoint only — resolve the overlap, then apply or discard",
		e.ProposalID,
		paths,
	)
}

// proposalAnalysis is one open proposal's change and drift view for a single
// list/show call. Both are computed once so the envelope and its conflicts see
// the same snapshot.
type proposalAnalysis struct {
	proposal   *retainedProposal
	changes    []proposalChange
	changesErr error
	drift      []string
	driftErr   error
}

// effectiveChanges uses the typed changes when available and falls back to
// path-only changes when the post-images could not be inspected. The fallback
// keeps a real path collision visible but classifies it as path_overlap.
func (a proposalAnalysis) effectiveChanges() []proposalChange {
	if len(a.changes) > 0 {
		return a.changes
	}
	if a.changesErr == nil {
		return nil
	}
	files := a.proposal.changedFiles()
	out := make([]proposalChange, 0, len(files))
	for _, f := range files {
		if norm := normalizeProposalPath(f); norm != "" {
			out = append(out, proposalChange{ProposalID: a.proposal.id, Path: norm})
		}
	}
	return out
}

// proposalChanges projects one open proposal's changes into the comparison
// model. Container post-images come from the manifest. Worktree post-images come
// from the live worktree; the sealed diff is not a reliable content digest.
func (p *retainedProposal) proposalChanges(ctx context.Context) ([]proposalChange, error) {
	switch {
	case p.delta != nil:
		changes := make([]proposalChange, 0, len(p.delta.Changes))
		var inspectErrs []string
		for _, c := range p.delta.Changes {
			rel, err := safeDeltaPath(normalizeProposalPath(c.Path))
			if err != nil {
				inspectErrs = append(inspectErrs, err.Error())
				continue
			}
			change := proposalChange{
				ProposalID: p.id,
				Path:       rel,
				Kind:       normalizeProposalKind(c.Kind),
				BaseDigest: p.basisDigest,
			}
			if change.Kind != "delete" {
				change.Digest = proposalContentDigest(c.Content)
				change.Mode = c.Mode.String()
			}
			changes = append(changes, change)
		}
		sortProposalChanges(changes)
		if len(inspectErrs) > 0 {
			return changes, fmt.Errorf("proposal %s: inspect post-images: %s", p.id, strings.Join(inspectErrs, "; "))
		}
		return changes, nil

	case p.output != nil:
		if p.workdir == "" {
			return nil, fmt.Errorf("proposal %s has no live worktree to inspect", p.id)
		}
		baseline, err := p.output.Baseline().Digest()
		if err != nil {
			return nil, fmt.Errorf("proposal %s: digest dispatch basis: %w", p.id, err)
		}
		pv := NewPathValidator(p.workdir, false, nil)
		pv.WorkDir = pv.WorkspaceRoot
		ws := newLocalWorkspace(pv)
		changes := make([]proposalChange, 0, len(p.output.Changes()))
		var inspectErrs []string
		for _, c := range p.output.Changes() {
			rel, err := safeDeltaPath(normalizeProposalPath(c.Path))
			if err != nil {
				inspectErrs = append(inspectErrs, err.Error())
				continue
			}
			change := proposalChange{
				ProposalID: p.id,
				Path:       rel,
				Kind:       normalizeProposalKind(c.Kind),
				BaseDigest: baseline,
			}
			if change.Kind != "delete" {
				mode, digest, err := worktreePostImage(ctx, ws, rel)
				if err != nil {
					// Keep the path and its known kind so this path falls
					// back to path_overlap without discarding metadata for the
					// proposal's other, readable paths.
					inspectErrs = append(inspectErrs, fmt.Sprintf("%s: %v", rel, err))
				} else {
					change.Mode = mode
					change.Digest = digest
				}
			}
			changes = append(changes, change)
		}
		sortProposalChanges(changes)
		if len(inspectErrs) > 0 {
			return changes, fmt.Errorf("proposal %s: inspect post-images: %s", p.id, strings.Join(inspectErrs, "; "))
		}
		return changes, nil

	default:
		return nil, fmt.Errorf("proposal %s has no sealed changes", p.id)
	}
}

// proposalChangesForPaths filters a proposal's change metadata down to the
// paths named by an apply refusal. A failure to inspect is not allowed to hide
// the refusal itself, so it yields no changes rather than an error.
func proposalChangesForPaths(ctx context.Context, p *retainedProposal, paths []string) []proposalChange {
	changes, _ := p.proposalChanges(ctx)
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[pathKey(normalizeProposalPath(p))] = true
	}
	out := make([]proposalChange, 0, len(paths))
	for _, change := range changes {
		if want[pathKey(normalizeProposalPath(change.Path))] {
			out = append(out, change)
		}
	}
	return out
}

// normalizeProposalKind makes the worktree and container vocabularies
// comparable. Unknown kinds are preserved so classification can fall back to a
// path-only overlap rather than guessing.
func normalizeProposalKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "add", "create", "created":
		return "create"
	case "modify", "modified", "patch", "patched":
		return "modify"
	case "delete", "deleted":
		return "delete"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

func sortProposalChanges(changes []proposalChange) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Kind < changes[j].Kind
	})
}

func proposalContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// worktreePostImage reads one live worktree file through its workspace and
// returns the post-image mode and content digest. ResolvePath rejects a symlink
// that escapes the worktree; a link inside it is hashed by its target content.
// A special file gets no digest because FileMode does not identify its device
// peer; classification falls back to path_overlap rather than claiming two
// distinct devices are identical. Reads are capped so list/show stay bounded.
func worktreePostImage(ctx context.Context, ws Workspace, rel string) (mode, digest string, err error) {
	abs, err := ws.ResolvePath(filepath.FromSlash(rel))
	if err != nil {
		return "", "", err
	}
	info, err := ws.Lstat(ctx, abs)
	if err != nil {
		return "", "", err
	}
	mode = info.Mode().String()
	if !info.Mode().IsRegular() {
		// FileMode does not identify a device's rdev or a FIFO's peer, so
		// hashing the mode would make distinct special files look identical.
		// An empty digest deliberately falls back to path_overlap.
		return mode, "", nil
	}
	if info.Size() > proposalWorktreeMaxHashBytes {
		return "", "", fmt.Errorf("post-image is %d bytes, exceeding the %d-byte digest cap", info.Size(), proposalWorktreeMaxHashBytes)
	}
	data, err := ws.ReadFile(ctx, abs)
	if err != nil {
		return "", "", err
	}
	return mode, proposalContentDigest(data), nil
}

// classifyProposalChanges compares two post-images for one path.
func classifyProposalChanges(a, b proposalChange) string {
	switch {
	case a.Kind == "" || b.Kind == "":
		return proposalConflictPathOverlap
	case a.Kind == "delete" && b.Kind == "delete":
		return proposalConflictSamePathIdentical
	case a.Kind == "delete" || b.Kind == "delete":
		return proposalConflictDeleteModify
	case a.Digest == "" || b.Digest == "":
		return proposalConflictPathOverlap
	case a.Digest == b.Digest && a.Mode == b.Mode:
		return proposalConflictSamePathIdentical
	default:
		return proposalConflictSamePathDivergent
	}
}

// analyzeProposals computes the change and parent-drift view for every open
// proposal. Drift is cached per basis, but only successful results: an error
// names the proposal that produced it and must not be replayed under another.
func (r *ProposalRegistry) analyzeProposals(ctx context.Context) []proposalAnalysis {
	ps := r.all()
	out := make([]proposalAnalysis, 0, len(ps))
	driftCache := make(map[string][]string)
	for _, p := range ps {
		analysis := proposalAnalysis{proposal: p}
		analysis.changes, analysis.changesErr = p.proposalChanges(ctx)
		key := p.basisKey()
		if cached, ok := driftCache[key]; ok && key != "" {
			analysis.drift = cached
		} else {
			drift, err := p.parentDrift(ctx)
			if err != nil {
				analysis.driftErr = err
			} else {
				analysis.drift = drift
				if key != "" {
					driftCache[key] = drift
				}
			}
		}
		out = append(out, analysis)
	}
	return out
}

// proposalConflictBucket is one kind/pair group while conflicts are assembled.
type proposalConflictBucket struct {
	kind      string
	proposals []string
	paths     map[string]bool
	changes   map[string]proposalChange
}

// proposalConflicts classifies pairwise post-images and parent drift for all
// open proposals. Every collision is grouped by kind so a pair with several
// paths gets one entry. Identical post-images are not conflicts; they are
// returned separately because apply is path-disjoint and only one can land.
func proposalConflicts(analyses []proposalAnalysis) (conflicts, duplicates []proposalConflict) {
	conflictBuckets := make(map[string]*proposalConflictBucket)
	duplicateBuckets := make(map[string]*proposalConflictBucket)

	add := func(buckets map[string]*proposalConflictBucket, kind string, proposals []string, changes []proposalChange) {
		proposals = append([]string(nil), proposals...)
		sort.Strings(proposals)
		key := kind + "\x00" + strings.Join(proposals, "\x00")
		bucket := buckets[key]
		if bucket == nil {
			bucket = &proposalConflictBucket{
				kind:      kind,
				proposals: proposals,
				paths:     make(map[string]bool),
				changes:   make(map[string]proposalChange),
			}
			buckets[key] = bucket
		}
		for _, change := range changes {
			change.Path = normalizeProposalPath(change.Path)
			if change.Path == "" {
				continue
			}
			bucket.paths[change.Path] = true
			bucket.changes[change.ProposalID+"\x00"+change.Path] = change
		}
	}

	byPath := make(map[string]map[string]proposalChange)
	for _, analysis := range analyses {
		for _, change := range analysis.effectiveChanges() {
			norm := normalizeProposalPath(change.Path)
			if norm == "" {
				continue
			}
			change.Path = norm
			key := pathKey(norm)
			if byPath[key] == nil {
				byPath[key] = make(map[string]proposalChange)
			}
			byPath[key][analysis.proposal.id] = change
		}
	}

	for _, byProposal := range byPath {
		ids := make([]string, 0, len(byProposal))
		for id := range byProposal {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				a, b := byProposal[ids[i]], byProposal[ids[j]]
				kind := classifyProposalChanges(a, b)
				if kind == proposalConflictSamePathIdentical {
					add(duplicateBuckets, kind, []string{ids[i], ids[j]}, []proposalChange{a, b})
					continue
				}
				add(conflictBuckets, kind, []string{ids[i], ids[j]}, []proposalChange{a, b})
			}
		}
	}

	for _, analysis := range analyses {
		if analysis.driftErr != nil {
			continue
		}
		drift := make(map[string]bool, len(analysis.drift))
		for _, p := range analysis.drift {
			drift[pathKey(normalizeProposalPath(p))] = true
		}
		var changes []proposalChange
		for _, change := range analysis.effectiveChanges() {
			if drift[pathKey(normalizeProposalPath(change.Path))] {
				changes = append(changes, change)
			}
		}
		if len(changes) > 0 {
			add(conflictBuckets, proposalConflictParentOverlap, []string{analysis.proposal.id}, changes)
		}
	}

	return proposalConflictList(conflictBuckets), proposalConflictList(duplicateBuckets)
}

func proposalConflictList(buckets map[string]*proposalConflictBucket) []proposalConflict {
	out := make([]proposalConflict, 0, len(buckets))
	for _, bucket := range buckets {
		paths := make([]string, 0, len(bucket.paths))
		for p := range bucket.paths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		changes := make([]proposalChange, 0, len(bucket.changes))
		for _, change := range bucket.changes {
			changes = append(changes, change)
		}
		sort.Slice(changes, func(i, j int) bool {
			if changes[i].ProposalID != changes[j].ProposalID {
				return changes[i].ProposalID < changes[j].ProposalID
			}
			return changes[i].Path < changes[j].Path
		})
		out = append(out, proposalConflict{
			Kind:      bucket.kind,
			Paths:     paths,
			Proposals: bucket.proposals,
			Changes:   changes,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		pi := strings.Join(out[i].Proposals, "\x00")
		pj := strings.Join(out[j].Proposals, "\x00")
		if pi != pj {
			return pi < pj
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.Join(out[i].Paths, "\x00") < strings.Join(out[j].Paths, "\x00")
	})
	return out
}

// conflictsWith returns the conflict entries that name id. The list envelope
// carries both the global set and this per-proposal view so a caller holding one
// summary does not have to re-correlate ids.
func conflictsWith(conflicts []proposalConflict, id string) []proposalConflict {
	var out []proposalConflict
	for _, c := range conflicts {
		for _, p := range c.Proposals {
			if p == id {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// recordConflictFacts appends one fact per newly observed conflict to the
// parent's owner path. The registry deduplicates by conflict identity; a failed
// append releases the reservation so a later list/show can retry.
func (r *ProposalRegistry) recordConflictFacts(ctx context.Context, analyses []proposalAnalysis, conflicts []proposalConflict) {
	for _, conflict := range conflicts {
		key := conflictFactKey(conflict)
		if !r.reserveConflictFact(key) {
			continue
		}
		scope := conflictParentScope(analyses, conflict)
		if scope == nil {
			r.releaseConflictFact(key)
			continue
		}
		if _, err := scope.AppendFact(ctx, SchemaProposalConflict, "conflict", conflictFactPayload(conflict)); err != nil {
			r.releaseConflictFact(key)
			slog.Debug("proposal conflict fact: append failed", "kind", conflict.Kind, "err", err)
		}
	}
}

func conflictFactKey(conflict proposalConflict) string {
	return conflict.Kind + "\x00" + strings.Join(conflict.Proposals, "\x00") + "\x00" + strings.Join(conflict.Paths, "\x00")
}

func conflictParentScope(analyses []proposalAnalysis, conflict proposalConflict) *goatherd.Scope {
	for _, id := range conflict.Proposals {
		for _, analysis := range analyses {
			p := analysis.proposal
			if p.id != id || p.mgr == nil || p.parentID == "" {
				continue
			}
			if scope, ok := p.mgr.Get(p.parentID); ok {
				return scope
			}
		}
	}
	return nil
}

func conflictFactPayload(conflict proposalConflict) map[string]any {
	changes := make([]map[string]any, 0, len(conflict.Changes))
	for _, change := range conflict.Changes {
		changes = append(changes, map[string]any{
			"proposal_id": change.ProposalID,
			"path":        change.Path,
			"kind":        change.Kind,
			"base_digest": change.BaseDigest,
			"digest":      change.Digest,
			"mode":        change.Mode,
		})
	}
	return map[string]any{
		"kind":      conflict.Kind,
		"paths":     conflict.Paths,
		"proposals": conflict.Proposals,
		"changes":   changes,
	}
}

func (r *ProposalRegistry) reserveConflictFact(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conflictFacts == nil {
		r.conflictFacts = make(map[string]bool)
	}
	if r.conflictFacts[key] {
		return false
	}
	r.conflictFacts[key] = true
	return true
}

func (r *ProposalRegistry) releaseConflictFact(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conflictFacts, key)
}

// basisKey identifies the parent state a proposal was measured against. It is
// empty when the basis is unavailable or cannot be digested; that proposal gets
// its own inspection rather than sharing an unkeyed result.
func (p *retainedProposal) basisKey() string {
	// Include the fields parentDrift reads so a future registry with mixed
	// targets/parents cannot share a result it should not. The manager and
	// session pointers distinguish the same basis digest arriving through
	// different live scopes.
	prefix := fmt.Sprintf("%s\x00%s\x00%p\x00%p\x00", p.target, p.parentID, p.mgr, p.session)
	switch {
	case p.delta != nil:
		return prefix + p.basisDigest
	case p.output != nil:
		if digest, err := p.output.Baseline().Digest(); err == nil {
			return prefix + digest
		}
	}
	return ""
}

// normalizeProposalPath makes a path comparable across the worktree and
// container producers. Both should already emit cleaned slash paths; this keeps
// a malformed or legacy producer from creating a false "disjoint" answer.
func normalizeProposalPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// parentDrift lists the parent paths changed since the proposal's dispatch
// basis. A worktree proposal uses the sealed output's fork baseline; a container
// proposal uses the persisted basis state (host or session target). An error is
// returned rather than an empty list when the basis cannot be inspected, so the
// list envelope can distinguish "no drift" from "unknown".
func (p *retainedProposal) parentDrift(ctx context.Context) ([]string, error) {
	var paths []string
	switch {
	case p.delta != nil:
		if p.target == "session" {
			changed, err := p.sessionChangedSinceBasis(ctx)
			if err != nil {
				return nil, err
			}
			paths = changed
		} else {
			changed, err := p.hostChangedSinceBasis(ctx)
			if err != nil {
				return nil, err
			}
			paths = changed
		}
	case p.output != nil:
		baseline := p.output.Baseline()
		if baseline.Backend == "" {
			return nil, fmt.Errorf("proposal %s has no recorded dispatch basis", p.id)
		}
		if p.mgr == nil || p.parentID == "" {
			return nil, fmt.Errorf("proposal %s: the parent scope is no longer available", p.id)
		}
		parent, ok := p.mgr.Get(p.parentID)
		if !ok {
			return nil, fmt.Errorf("proposal %s: the parent scope is no longer available", p.id)
		}
		sb := parent.Sandbox()
		if sb == nil {
			return nil, fmt.Errorf("proposal %s: the parent has no workspace sandbox", p.id)
		}
		_, files, err := sb.Diff(ctx, baseline, 0)
		if err != nil {
			return nil, fmt.Errorf("proposal %s: diff parent against dispatch basis: %w", p.id, err)
		}
		normalized, err := normalizeHostDiffPaths(sb.Backend(), files)
		if err != nil {
			return nil, fmt.Errorf("proposal %s: %w", p.id, err)
		}
		paths = normalized
	default:
		return nil, fmt.Errorf("proposal %s has no sealed changes", p.id)
	}

	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		norm := normalizeProposalPath(raw)
		if norm == "" {
			continue
		}
		key := pathKey(norm)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, norm)
	}
	sort.Strings(out)
	return out, nil
}
