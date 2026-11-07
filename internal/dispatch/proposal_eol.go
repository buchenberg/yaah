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

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// eolNormalization is the parent repo's EOL policy for the paths of one settle.
// core.autocrlf is read once, up front, so the common answer (a Windows checkout
// with autocrlf enabled) costs a single git call for the whole proposal; the
// per-path .gitattributes probe runs only for a path whose bytes differ while
// its folded content matches, and its answer is cached, so a settle with no
// EOL-only difference pays nothing.
type eolNormalization struct {
	autocrlf bool
	attrs    map[string]bool
	// lookup is the .gitattributes probe. A nil lookup means "no attribute
	// rule", which keeps the zero value usable where only core.autocrlf can
	// be modelled.
	lookup func(rel string) bool
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
		n.lookup = func(rel string) bool { return gitPathNormalizesEOL(ctx, repoPath, rel) }
	}
	return n
}

// expected reports whether an EOL-only difference on rel is git's own
// translation of an unchanged file rather than an edit by the parent.
func (n *eolNormalization) expected(rel string) bool {
	if n == nil {
		return false
	}
	if n.autocrlf {
		return true
	}
	if v, ok := n.attrs[rel]; ok {
		return v
	}
	v := false
	if n.lookup != nil {
		v = n.lookup(rel)
	}
	if n.attrs == nil {
		n.attrs = make(map[string]bool)
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

// gitPathNormalizesEOL reports whether .gitattributes sets an EOL-normalizing
// rule for rel: a text attribute (set, auto, or a value) or an explicit eol
// attribute. `-text` opts a path out of normalization, so it never counts — a
// binary-typed path is compared byte for byte.
func gitPathNormalizesEOL(ctx context.Context, repoPath, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "check-attr", "-z", "text", "eol", "--", filepath.ToSlash(rel))
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// -z emits "<path>\0<attribute>\0<value>\0" per requested attribute.
	fields := strings.Split(string(out), "\x00")
	textRule, eolRule, textUnset := false, false, false
	for i := 0; i+2 < len(fields); i += 3 {
		value := fields[i+2]
		switch fields[i+1] {
		case "text":
			switch value {
			case "unspecified":
				// No rule: the path keeps its bytes.
			case "unset":
				textUnset = true
			default: // "set", "auto", or an explicit value
				textRule = true
			}
		case "eol":
			switch value {
			case "unspecified", "unset":
				// No eol rule.
			default: // "lf", "crlf", or a bare `eol` attribute
				eolRule = true
			}
		}
	}
	if textUnset {
		return false
	}
	return textRule || eolRule
}
