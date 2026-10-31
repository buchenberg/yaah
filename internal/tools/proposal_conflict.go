package tools

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// proposal_conflict.go implements the Phase 0 visibility half of conflict-aware
// speculation (yaah-9bq): before an orchestrator settles proposals, list the
// paths they share with each other and the paths the parent changed since each
// proposal's basis. It deliberately compares paths, not content; content-aware
// conflict classification belongs to the typed model (yaah-6xyl.1).
//
// The list view is advisory. Settlement remains authoritative and path-disjoint:
// a proposal can still refuse apply because the parent moved after the list was
// rendered. When it does, the refusal is a typed error so the tool can render a
// machine-readable conflict result instead of only prose.

const (
	// proposalConflictPathOverlap is two open proposals changing the same path.
	proposalConflictPathOverlap = "path_overlap"
	// proposalConflictApply is a proposal whose changed paths overlap the
	// parent's changes since the dispatch basis. It blocks apply, not select
	// (select has its own basis check).
	proposalConflictApply = "apply_conflict"
)

// proposalConflict is one machine-readable conflict fact. It is shared by the
// list envelope (proposal-to-proposal overlaps) and an apply refusal (proposal
// against the parent).
type proposalConflict struct {
	Kind      string   `json:"kind"`
	Paths     []string `json:"paths"`
	Proposals []string `json:"proposals"`
}

// proposalApplyConflict is the settlement refusal that carries enough data for
// ProposalTool to return a structured conflict envelope. It is an error because
// a direct registry caller must not mistake a refused apply for success.
type proposalApplyConflict struct {
	ProposalID string
	Paths      []string
	// Cause preserves the settlement sentinel for direct registry callers.
	// ProposalTool renders the structured envelope; Unwrap keeps
	// errors.Is(err, goatherd.ErrApplyConflict) working underneath it.
	Cause error
}

func (e *proposalApplyConflict) Unwrap() error { return e.Cause }

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

// proposalOverlaps lists all pairwise path overlaps across open proposals. A
// pair is reported once even when it shares several paths. The result is stable
// for a stable registry order: proposals by id within a pair, paths sorted.
func proposalOverlaps(ps []*retainedProposal) []proposalConflict {
	type pair struct {
		a string
		b string
	}
	type pairPaths map[string]bool

	// path -> proposal id -> the normalized spelling of the path.
	byPath := make(map[string]map[string]string)
	for _, p := range ps {
		for _, f := range p.changedFiles() {
			norm := normalizeProposalPath(f)
			if norm == "" {
				continue
			}
			key := pathKey(norm)
			if byPath[key] == nil {
				byPath[key] = make(map[string]string)
			}
			byPath[key][p.id] = norm
		}
	}

	overlaps := make(map[pair]pairPaths)
	for _, proposals := range byPath {
		ids := make([]string, 0, len(proposals))
		for id := range proposals {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				k := pair{a: ids[i], b: ids[j]}
				if overlaps[k] == nil {
					overlaps[k] = make(pairPaths)
				}
				// The spelling is equivalent for matching; use the first
				// proposal's spelling for a stable, human-readable value.
				overlaps[k][proposals[ids[i]]] = true
			}
		}
	}

	out := make([]proposalConflict, 0, len(overlaps))
	for k, paths := range overlaps {
		ps := make([]string, 0, len(paths))
		for p := range paths {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		out = append(out, proposalConflict{
			Kind:      proposalConflictPathOverlap,
			Paths:     ps,
			Proposals: []string{k.a, k.b},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proposals[0] != out[j].Proposals[0] {
			return out[i].Proposals[0] < out[j].Proposals[0]
		}
		if out[i].Proposals[1] != out[j].Proposals[1] {
			return out[i].Proposals[1] < out[j].Proposals[1]
		}
		return strings.Join(out[i].Paths, "\x00") < strings.Join(out[j].Paths, "\x00")
	})
	return out
}

// conflictsWith returns the overlap entries that name id. The list envelope
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
