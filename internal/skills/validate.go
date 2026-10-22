package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// MaxDescriptionLength caps a skill description. Skills frontmatter carries a
// one-line description for load-time matching, not a body; an overlong one
// inflates every prompt that lists skills and is a strong smell that the
// author confused description with body (docs/compaction-as-sleep.md §8 P2).
const MaxDescriptionLength = 200

// Validate enforces the full schema for a NEW skill: shape plus description
// length. Create uses it so a fresh skill is both well-formed and bounded.
func Validate(name, description, body string) error {
	if err := validateShape(name, description, body); err != nil {
		return err
	}
	if len(description) > MaxDescriptionLength {
		return fmt.Errorf("skill description is %d bytes; max is %d", len(description), MaxDescriptionLength)
	}
	return nil
}

// validateShape enforces the frontmatter shape without the description-length
// cap. Edit uses it directly: an edit that only changes the body must not be
// rejected because an existing skill's description exceeds MaxDescriptionLength
// (the repo already ships skills with 300+ byte descriptions).
//
// The name is additionally restricted to a single path element with no control
// characters, because it becomes a directory name. The description is only
// required to be non-empty: yaml.Marshal (renderSkill) quotes or block-scalars
// YAML-special and multi-line values safely, so a newline in a description is
// no longer an injection vector.
func validateShape(name, description, body string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("skill name is required")
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return fmt.Errorf("skill name %q must be a single path element", name)
	}
	if hasControlOrNewline(name) {
		return fmt.Errorf("skill name must not contain control characters or newlines")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("skill description is required")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("skill body is required")
	}
	return nil
}

// hasControlOrNewline reports whether s contains a newline or any C0 control
// character. These are unsafe in a filename (the skill name becomes a
// directory name) regardless of how the YAML is serialized.
func hasControlOrNewline(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r < 0x20 || r == unicode.ReplacementChar {
			return true
		}
	}
	return false
}

// dangerousPatterns are the clearly-dangerous constructs that a skill body
// must never contain. The scan is intentionally narrow and specific — it
// exists to stop autonomous skill writes from smuggling executable attack
// chains into a skill that will later be loaded and followed, not to police
// every shell command a skill might legitimately document. Patterns use (?i)
// so trivial case changes do not defeat them.
var dangerousPatterns = []*regexp.Regexp{
	// Fetch-and-execute: curl/wget/nc output piped into a shell, with an
	// optional sudo/env wrapper between the pipe and the shell.
	regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat)\b[^\n|;]*\|\s*(sudo\s+)?(env\s+)?(ba|z|k)?sh\b`),
	// A shell -c that itself fetches remote content.
	regexp.MustCompile(`(?i)\b(ba|z|k)?sh\s+-c\s+["'][^"']*(curl|wget)\b`),
	// eval of a command substitution that fetches remote content.
	regexp.MustCompile(`(?i)\beval\s+\$\(\s*(curl|wget)\b`),
	// A python one-liner that loads remote content.
	regexp.MustCompile(`(?i)\bpython3?\s+-c\s+["'][^"']*(curl|wget|urllib|requests)\b`),
	// Recursive delete of a filesystem root or home (bare or contents), with
	// the r/f flags in either order. Relative targets like `rm -rf ./build`
	// or a scoped path like `rm -rf /home/x` are intentionally NOT matched:
	// those are ordinary cleanup, not an attack chain. The target must be
	// followed by a non-path character (whitespace, quote, backtick, or EOL)
	// so `/` only matches the root itself.
	regexp.MustCompile("(?i)\\brm\\s+[^\\n]*?(?:-[a-z]*[rf][a-z]*|--(?:recursive|force))[^\\n]*?(/\\*?|~/?\\*?)(?:\\s|$|\\x60|'|\")"),
	// Classic fork bomb.
	regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	// Filesystem / disk destroyers. dd is only flagged when the OUTPUT is a
	// raw device (of=/dev/...) — writing to a regular file, a swap file, or
	// generating a key file is legitimate. shred/wipefs are only flagged
	// against a device target.
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bdd\s+[^\n]*if=/dev/(zero|urandom|random)\s+[^\n]*of=/dev/`),
	regexp.MustCompile(`(?i)\bdd\s+[^\n]*of=/dev/\S+\s+[^\n]*if=/dev/(zero|urandom|random)\b`),
	regexp.MustCompile(`(?i)\b(shred|wipefs)\b[^\n]*/dev/`),
}

// ScanSecurity rejects content that contains a dangerous construct. It is the
// safety half of the P2 guardrails: enabled for autonomous (unattended) skill
// writes via SkillTool.Strict, where the author is the agent rather than a
// human. A nil return means the content passed.
func ScanSecurity(content string) error {
	for _, re := range dangerousPatterns {
		if re.MatchString(content) {
			return fmt.Errorf("skill content matches a dangerous pattern %q", re.String())
		}
	}
	return nil
}
