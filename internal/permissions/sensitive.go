package permissions

import (
	"strings"
)

// FileTargetTools are the tools whose permission resource is a file path —
// the set a saved-allow must be able to inspect for secret shapes. It is
// MutatingFileTools (the write family) plus the read family, so the two
// tables cannot drift apart: whatever tool learns to mutate or read files
// tomorrow joins SensitiveResource's reach through MutatingFileTools the
// moment it joins the inherited ceiling.
var FileTargetTools = func() map[string]bool {
	m := make(map[string]bool, len(MutatingFileTools)+2)
	for name := range MutatingFileTools {
		m[name] = true
	}
	m["read"] = true
	m["tail"] = true
	return m
}()

// SensitiveResource reports whether resource is a credentials-shaped target
// that a persisted "always" approval may never lift (yaah-x6vi.7, the
// kilocode permission/read.ts carve-out lesson): an ask the user answers
// "always" for a secrets file must not stop asking.
//
// It is deliberately narrow in both directions:
//
//   - Narrow in WHAT it flags. Only dotenv shapes and OpenSSH private
//     keys, the two conventions that carry live credentials in repos.
//     `.env.example` is exempt — an example file is a published template
//     (kilocode allows it the same way), not a secret.
//   - Narrow in WHAT IT DECIDES. Only the saved-allow consult consults
//     this: a resource may stay "ask"-lifted forever, but a config rule
//     (permissions: deny) and the mode floor keep their usual authority.
//     This never denies a call; it keeps a question alive.
//
// The tool must be a file-target tool: a shell command or a URL is never a
// file, and flagging one on a substring match ("read .env in a grep
// command") would be a false positive on the wrong axis. Bash-policy
// lessons (#11523) aside, saved bash allows stay the parent's own gate.
func SensitiveResource(tool, resource string) bool {
	if !FileTargetTools[tool] {
		return false
	}
	base := resource
	if i := strings.LastIndexAny(resource, `/\`); i >= 0 {
		base = resource[i+1:]
	}
	if base == "" {
		return false
	}
	return SensitiveBasename(base)
}

// SensitiveBasename reports whether a file's base name marks it as a
// credential: an exact dotenv family member, anything derived from one
// (`.env.local`, `prod.env`), or a private OpenSSH key (the convention
// names without extension — the `.pub` twins never match).
func SensitiveBasename(name string) bool {
	if name == ".env.example" {
		return false
	}
	if name == ".env" ||
		strings.HasPrefix(name, ".env.") ||
		strings.HasSuffix(name, ".env") {
		return true
	}
	switch name {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ed25519_sk":
		return true
	}
	return false
}
