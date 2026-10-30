package parity

// deferral_comments_test.go keeps a deferral comment falsifiable. The audit
// behind yaah-qlf9 found comments that still described a blocker after it had
// landed; a bead id makes the claim checkable with `bd show` instead of
// trusting stale prose. The patterns are deliberately narrow: "not yet" is
// normal status prose, while these phrases describe a deferred obligation.

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var deferralPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)\buntil\b.{0,80}\b(?:bridge|support|wiring|rehydration|feature|implementation|work)\b.{0,40}\b(?:lands?|ships?)\b`),
	regexp.MustCompile(`(?is)\bnot wired yet\b`),
	regexp.MustCompile(`(?is)\bstill unused\b`),
	regexp.MustCompile(`(?is)\breserved for the P[0-9]\b`),
}

var falsifiableReference = regexp.MustCompile(`\b(?:yaah|croft|shepherd-runtime-go|goatherd-runtime)-[a-z0-9]+\b`)

func TestDeferralCommentsNameFalsifiableReference(t *testing.T) {
	root := filepath.Clean("../..")
	fset := token.NewFileSet()
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// The walk root is "..", which looks hidden but must be traversed.
			// Below it, skip local-only tool directories and dependency/build
			// trees so a developer machine cannot parse third-party Go.
			if path == root {
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			switch entry.Name() {
			case "vendor", "node_modules", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		scanned++
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, group := range file.Comments {
			text := group.Text()
			if !hasDeferralClaim(text) || falsifiableReference.MatchString(text) {
				continue
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			t.Errorf("%s:%d: deferral comment names no falsifiable reference (bead id): %q",
				rel, fset.Position(group.Pos()).Line, strings.TrimSpace(text))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan comments: %v", err)
	}
	if scanned == 0 {
		t.Fatal("deferral scan visited no Go files; the walk root was skipped")
	}
}

func hasDeferralClaim(text string) bool {
	for _, pattern := range deferralPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
