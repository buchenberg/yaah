package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxDescriptionLength caps a skill description. Skills frontmatter carries a
// one-line description for load-time matching, not a body; an overlong one
// inflates every prompt that lists skills and is a strong smell that the
// author confused description with body (docs/compaction-as-sleep.md §8 P2).
const MaxDescriptionLength = 200

// Validate enforces the skill frontmatter schema. It is the always-on
// correctness half of the P2 guardrails: a malformed skill must never be
// written regardless of who authors it.
func Validate(name, description, body string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return fmt.Errorf("skill name %q must be a single path element", name)
	}
	if description == "" {
		return fmt.Errorf("skill description is required")
	}
	if len(description) > MaxDescriptionLength {
		return fmt.Errorf("skill description is %d bytes; max is %d", len(description), MaxDescriptionLength)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("skill body is required")
	}
	return nil
}

// dangerousPatterns are the clearly-dangerous constructs that a skill body
// must never contain: remote-content piped into a shell, recursive filesystem
// deletion, fork bombs, and filesystem/disk destroyers. The scan is
// intentionally narrow and specific — it exists to stop autonomous skill
// writes from smuggling executable attack chains into a skill that will later
// be loaded and followed, not to police every shell command a skill might
// legitimately document.
var dangerousPatterns = []*regexp.Regexp{
	// Fetch-and-execute: curl/wget/nc output piped into a shell.
	regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat)\b[^\n|]*\|\s*(ba|z|k)?sh\b`),
	// A shell -c that itself fetches remote content.
	regexp.MustCompile(`(?i)\b(ba|z|k)?sh\s+-c\s+["'][^"']*(curl|wget)\b`),
	// Recursive delete of a filesystem root or home (bare or contents).
	// Relative targets like `rm -rf ./build` or a specific path like
	// `rm -rf /home/x` are intentionally NOT matched: those are ordinary
	// project cleanup or scoped deletes, not an attack chain. The target
	// must be followed by a non-path character (whitespace, a quote, a
	// backtick, or end of line) so `/` only matches the root itself.
	regexp.MustCompile("\\brm\\s+-[a-z]*r[a-z]*f[a-z]*\\s+(/\\*?|~/\\*?)(?:\\s|$|\\x60|'|\")"),
	// Classic fork bomb.
	regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	// Filesystem / disk destroyers.
	regexp.MustCompile(`(?i)\b(mkfs\b|\bdd\s+if=/dev/zero)\b`),
}

// ScanSecurity rejects a skill body that contains a dangerous construct. It is
// the safety half of the P2 guardrails: enabled for autonomous (unattended)
// skill writes via SkillTool.Strict, where the author is the agent rather than
// a human. A nil return means the body passed.
func ScanSecurity(body string) error {
	for _, re := range dangerousPatterns {
		if re.MatchString(body) {
			return fmt.Errorf("skill body matches a dangerous pattern %q", re.String())
		}
	}
	return nil
}
