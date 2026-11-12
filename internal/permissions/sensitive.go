package permissions

import (
	"encoding/json"
	"strings"
)

// FileTargetTools are the tools whose permission resource is a file path —
// the set a saved-allow must be able to inspect for secret shapes. The write
// family comes from MutatingFileTools, so a tool that joins the inherited
// ceiling joins the carve-out the same way. The read family is enumerated
// because content readers share no other table: `grep` returns matching
// lines, `json_query` returns a field, `diff` returns both sides and
// `go_outline` extracts source, so each can hand back a secrets file's
// contents without mutating anything.
var FileTargetTools = func() map[string]bool {
	m := make(map[string]bool, len(MutatingFileTools)+6)
	for name := range MutatingFileTools {
		m[name] = true
	}
	for _, name := range []string{"read", "tail", "grep", "json_query", "diff", "go_outline"} {
		m[name] = true
	}
	return m
}()

// SensitiveResource reports whether resource is a credentials-shaped target
// that a persisted "always" approval may never lift (yaah-x6vi.7, the
// kilocode permission/read.ts carve-out lesson): an ask the user answers
// "always" for a secrets file must not stop asking.
//
// It is deliberately narrow in both directions:
//
//   - Narrow in WHAT it flags. Only dotenv shapes, direnv's `.envrc`, and
//     OpenSSH private keys — the conventions that carry live credentials in
//     repos. `.env.example` is exempt — an example file is a published
//     template (kilocode allows it the same way), not a secret.
//   - Narrow in WHAT IT DECIDES. This never denies a call; it keeps a
//     question alive. A config rule (`permissions: deny`) and the mode
//     floor keep their usual authority over the same target.
//
// The tool must be a file-target tool: a shell command or a URL is never a
// file, and flagging one on a substring match ("read .env in a grep
// command") would be a false positive on the wrong axis.
//
// This function judges a ruleset resource — one derived string. The seams
// that hold a call's arguments use SensitiveCall instead, which sees every
// target-bearing argument rather than the single one ResourceFor derives.
func SensitiveResource(tool, resource string) bool {
	if !FileTargetTools[tool] {
		return false
	}
	base := pathBase(resource)
	if base == "" {
		return false
	}
	return SensitiveBasename(base)
}

// SensitiveBasename reports whether a file's base name marks it as a
// credential: an exact dotenv family member, anything derived from one
// (`.env.local`, `prod.env`), direnv's `.envrc`, or a private OpenSSH key
// (the convention names without extension — the `.pub` twins never match).
func SensitiveBasename(name string) bool {
	if name == ".env.example" {
		return false
	}
	if name == ".env" ||
		name == ".envrc" ||
		strings.HasPrefix(name, ".env.") ||
		strings.HasSuffix(name, ".env") {
		return true
	}
	switch name {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk":
		return true
	}
	return false
}

// callTargetKeys are the argument names a file-target tool can carry its
// real target under. Wider than resourceKeys, which derives the ONE string
// rule matching compares: the carve-out has to see every path-shaped
// argument, because several tools hide the target they act on in a second
// key — replace and bulk_files select the files they touch with `pattern`
// against a directory in `path`, and json_query names its input `file`.
var callTargetKeys = []string{"filePath", "path", "to", "url", "file", "a", "b", "include"}

// callTargetListKeys are the same idea for tools that take a list of paths.
var callTargetListKeys = []string{"paths", "files"}

// patternTargetTools are the tools whose `pattern` selects the files they
// act on rather than text to match. A grep pattern that happens to read
// ".env" is not a file target, so the key is scoped rather than global.
var patternTargetTools = map[string]bool{"replace": true, "bulk_files": true}

// SensitiveCall reports whether a tool call touches a credential-shaped
// target, judging the call's own arguments rather than a ruleset resource.
//
// This is the enforcement point for the carve-out. SensitiveResource answers
// "may this saved row lift this ask", which is a question about one derived
// string — but the row may be tool-wide (an empty Resource matches every
// invocation of the tool) or a glob, and ResourceFor sees a single argument.
// The call is therefore the only reliable vantage point, and the seams that
// hold the arguments use this: the evaluator's Ask branch, the ACP session
// memory, and the ACP save.
func SensitiveCall(tool, argsJSON string) bool {
	if !FileTargetTools[tool] {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
		return false
	}
	for _, key := range callTargetKeys {
		if v, ok := payload[key].(string); ok && v != "" && SensitivePath(v) {
			return true
		}
	}
	for _, key := range callTargetListKeys {
		list, ok := payload[key].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			if v, ok := item.(string); ok && v != "" && SensitivePath(v) {
				return true
			}
		}
	}
	if patternTargetTools[tool] {
		if p, ok := payload["pattern"].(string); ok && p != "" && SensitiveBasename(globBasename(p)) {
			return true
		}
	}
	if tool == "patch" {
		// A patch may carry no filePath at all: the target is named inside
		// the diff's own headers, and a body that rewrites a secrets file is
		// the same target by another route.
		if body, ok := payload["patch"].(string); ok && patchTouchesSensitive(body) {
			return true
		}
	}
	return false
}

// SensitivePath reports whether a path-shaped argument names a credential.
func SensitivePath(p string) bool {
	return SensitiveBasename(pathBase(p))
}

// pathBase is the last path segment, for either separator, so `./.env`,
// `sub\.env`, `docs/../.env` and `C:\repo\.env` all reduce to `.env`.
func pathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// globBasename reduces a path glob to the base name it would match, so a
// pattern that can only select env files (`.env`, `*.env`, `**/.env`)
// counts while a broader one (`src/*.go`, `*`) does not.
func globBasename(pattern string) string {
	return strings.Map(func(r rune) rune {
		if r == '*' || r == '?' || r == '[' || r == ']' || r == '{' || r == '}' || r == '\\' {
			return -1
		}
		return r
	}, pathBase(pattern))
}

// patchTouchesSensitive scans the file headers of a unified diff for a
// credential target. Only the ---/+++ lines are read: a body line that
// merely mentions `.env` is not a target.
func patchTouchesSensitive(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		var rest string
		switch {
		case strings.HasPrefix(line, "+++ "):
			rest = line[len("+++ "):]
		case strings.HasPrefix(line, "--- "):
			rest = line[len("--- "):]
		default:
			continue
		}
		rest = strings.TrimSpace(rest)
		// `--- a/x\t2024-01-01` — drop a trailing timestamp column.
		if i := strings.IndexByte(rest, '\t'); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		rest = strings.TrimPrefix(rest, "a/")
		rest = strings.TrimPrefix(rest, "b/")
		if rest == "" || rest == "/dev/null" {
			continue
		}
		if SensitiveBasename(pathBase(rest)) {
			return true
		}
	}
	return false
}
