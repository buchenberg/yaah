package dispatch

// proposal_eol.go answers the one question the three-way drift check cannot
// answer from bytes alone: for this path, is git expected to translate line
// endings between the basis blob and the parent's working tree?
//
// The drift check folds CRLF to LF, because the two sides it compares come from
// different places — base is a raw tree blob, current is the working-tree file.
// That fold is only correct when git itself does the translation (core.autocrlf,
// or a text/eol rule in .gitattributes): then an unchanged file is LF in the
// blob and CRLF on disk, and calling it drifted would force a three-way merge on
// a path the parent never touched. In a repo that does not normalize, the same
// EOL-only difference is a real parent edit, and folding it away lets the
// no-drift fast path overwrite the parent's content whole-path — the exact
// fail-open the content check exists to prevent (PR #824 review, finding 3).
//
// core.autocrlf is a default, not an answer: a `-text` rule in .gitattributes
// still opts a path out of normalization inside an autocrlf checkout, so the
// per-path rule is consulted first and only a path with no relevant attribute
// falls back to the repo-wide setting (PR #824 review round 3, finding 1).

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// eolRule is what the parent repo's attributes say about line-ending
// translation for one path.
type eolRule int

const (
	// eolRuleUnspecified means no text or eol attribute applies: the path
	// takes the repo-wide default, core.autocrlf.
	eolRuleUnspecified eolRule = iota
	// eolRuleNormalizes means a text or eol rule is set for the path, so git
	// translates its line endings whatever core.autocrlf says.
	eolRuleNormalizes
	// eolRuleRaw means `-text` (a `text` attribute in the unset state) opted
	// the path out of normalization. It outranks core.autocrlf: git leaves
	// such a path's bytes alone even in an autocrlf checkout.
	eolRuleRaw
)

// eolNormalization is the parent repo's EOL policy for the paths of one settle.
// core.autocrlf is read once, up front, and answers for a path whose
// .gitattributes carry no text/eol rule; the per-path attributes probe runs
// lazily and its answer is cached, so a settle with no EOL-only difference pays
// nothing.
type eolNormalization struct {
	autocrlf bool
	attrs    map[string]eolRule
	// lookup is the .gitattributes probe. A nil lookup means "no attribute
	// rule", which keeps the zero value usable where only core.autocrlf can
	// be modelled.
	lookup func(rel string) eolRule
}

// newEOLNormalization snapshots the EOL policy of the parent repo at repoPath.
// Every failure mode — no git binary, no repo, an unreadable config — reads as
// "no normalization", which classifies an EOL-only difference as a parent edit
// and sends the path to the merge. That direction is the safe one: a needless
// merge resolves to the same content, while a wrongly folded comparison
// overwrites the parent.
func newEOLNormalization(ctx context.Context, repoPath string) *eolNormalization {
	n := &eolNormalization{autocrlf: gitAutocrlfActive(ctx, repoPath)}
	if repoPath != "" {
		n.lookup = func(rel string) eolRule { return gitPathEOLRule(ctx, repoPath, rel) }
	}
	return n
}

// expected reports whether an EOL-only difference on rel is git's own
// translation of an unchanged file rather than an edit by the parent.
//
// core.autocrlf is consulted as the fallback for a path with no relevant
// attribute, never as a short-circuit: a `-text` rule keeps a path raw under an
// autocrlf checkout, so folding it away would hide a real parent edit.
func (n *eolNormalization) expected(rel string) bool {
	if n == nil {
		return false
	}
	switch n.rule(rel) {
	case eolRuleRaw:
		return false
	case eolRuleNormalizes:
		return true
	default:
		return n.autocrlf
	}
}

// rule returns the .gitattributes rule for rel, asking git at most once per
// path. A path with no relevant attribute (or an unreadable attribute stack)
// reads as eolRuleUnspecified and so inherits core.autocrlf.
func (n *eolNormalization) rule(rel string) eolRule {
	if n == nil {
		return eolRuleUnspecified
	}
	if v, ok := n.attrs[rel]; ok {
		return v
	}
	v := eolRuleUnspecified
	if n.lookup != nil {
		v = n.lookup(rel)
	}
	if n.attrs == nil {
		n.attrs = make(map[string]eolRule)
	}
	n.attrs[rel] = v
	return v
}

// gitAutocrlfActive reports whether git is configured to normalize line endings
// in the working tree. core.autocrlf=true converts on checkout and commit;
// =input converts on commit, which still means the blob and the worktree may
// disagree about EOLs. The query covers the repo's own config plus the user's
// global config, which is what git itself would honor.
func gitAutocrlfActive(ctx context.Context, repoPath string) bool {
	if repoPath == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--get", "core.autocrlf").Output()
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(out))) {
	case "true", "input", "yes", "on", "1":
		return true
	}
	return false
}

// gitPathEOLRule reads the .gitattributes rules for rel: a text attribute (set,
// auto, or a value) or an explicit eol attribute means git normalizes the path,
// `-text` means it keeps its bytes whatever core.autocrlf says, and no matching
// attribute leaves the repo-wide default in charge.
func gitPathEOLRule(ctx context.Context, repoPath, rel string) eolRule {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "check-attr", "-z", "text", "eol", "--", filepath.ToSlash(rel))
	out, err := cmd.Output()
	if err != nil {
		// An unreadable attribute stack is no attribute rule: the path falls
		// back to core.autocrlf, which is the answer it had before this probe
		// existed.
		return eolRuleUnspecified
	}
	// -z emits "<path>\0<attribute>\0<value>\0" per requested attribute.
	fields := strings.Split(string(out), "\x00")
	textSet, eolSet, textUnset := false, false, false
	for i := 0; i+2 < len(fields); i += 3 {
		value := fields[i+2]
		switch fields[i+1] {
		case "text":
			switch value {
			case "unspecified":
				// No rule: the path keeps its bytes unless autocrlf says
				// otherwise.
			case "unset":
				textUnset = true
			default: // "set", "auto", or an explicit value
				textSet = true
			}
		case "eol":
			switch value {
			case "unspecified", "unset":
				// No eol rule.
			default: // "lf", "crlf", or a bare `eol` attribute
				eolSet = true
			}
		}
	}
	// git treats `-text` as binary regardless of any eol attribute, so unset
	// wins outright.
	if textUnset {
		return eolRuleRaw
	}
	if textSet || eolSet {
		return eolRuleNormalizes
	}
	return eolRuleUnspecified
}
