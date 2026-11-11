package substrate

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// HarnessPaths is the write-time guard for the harness's own configuration.
//
// Role files, skills, plans, and yaah's configuration/state decide what the
// agent may do: a role file names the toolset, provider, budgets and permission
// rules a dispatch runs under. Treating those files as ordinary workspace
// content lets a full-access session (or a write-capable sub-agent in a shared
// lane) rewrite its own or its parent's role, add tools, repoint the model, or
// widen its permissions — an escalation that needs no bug in the model, only a
// write call.
//
// The guard is deliberately outside the permission ruleset. Rules are ordered
// and last-match-wins, so a user rule, a persona rule, or a future saved
// "always allow" could shadow them; this policy is consulted by the path
// resolvers themselves and has no rule surface to shadow. It also does not
// consult the session's approval mode: "allow" suppresses prompts for
// dangerous tool calls, it does not make harness configuration writable
// without a human decision.
//
// Reads are never gated. The agent may inspect its own role and config; only
// mutations are classified.
type HarnessPaths struct {
	// Home is the user's home directory. Rules for ~/.agents live here.
	home string
	// ConfigDir is yaah's configuration/state directory (~/.yaah by
	// default, or $YAAH_HOME). It is never agent-writable.
	configDir string
	// roots are the workspace roots whose relative .agents/.yaah trees are
	// protected. A session with --workspace has two (cwd and the workspace
	// root); an isolated sub-agent has its own worktree.
	roots []string
	// rules are evaluated in order; the first matching rule wins. Global
	// (home/config) rules are built before workspace rules so a session
	// started at $HOME cannot downgrade a global deny to a workspace ask.
	rules []harnessRule

	// Ask is the approval surface for HarnessAsk paths. Nil means no
	// surface is attached (headless, a sub-agent without a bridge) and the
	// write is refused. It is set once during session wiring, before any
	// turn can dispatch.
	Ask func(path, reason string) bool

	mu       sync.Mutex
	approved map[string]bool
}

type harnessRule struct {
	root    string
	prefix  string // slash-separated path below root
	verdict HarnessVerdict
	reason  string
}

// HarnessVerdict is what the policy says about one path.
type HarnessVerdict int

const (
	// HarnessAllow means the path is not harness configuration.
	HarnessAllow HarnessVerdict = iota
	// HarnessAsk means the path is harness configuration inside the
	// workspace: reachable with a human decision, never silently.
	HarnessAsk
	// HarnessDeny means the path is global harness configuration or state:
	// no agent write is legitimate, so no prompt is offered.
	HarnessDeny
)

// HarnessRefusalError reports a protected path that the guard refused. It
// distinguishes a hard denial from a declined ask so the model sees which
// boundary it hit.
type HarnessRefusalError struct {
	Path   string
	Reason string
	Deny   bool
}

func (e *HarnessRefusalError) Error() string {
	if e.Deny {
		return fmt.Sprintf("harness path %q is protected (%s); the agent cannot modify harness configuration", e.Path, e.Reason)
	}
	return fmt.Sprintf("harness path %q requires approval (%s); the write was refused", e.Path, e.Reason)
}

// NewHarnessPaths builds the policy for one session. home and configDir may be
// empty (the corresponding rules are then omitted); roots are the workspace
// roots whose .agents/.yaah trees are protected.
func NewHarnessPaths(home, configDir string, roots ...string) *HarnessPaths {
	h := &HarnessPaths{
		home:      canonicalDir(home),
		configDir: canonicalDir(configDir),
	}
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		root = canonicalDir(root)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		h.roots = append(h.roots, root)
	}
	h.rules = h.buildRules()
	return h
}

// WithWorkspaceRoot derives the policy for an isolated checkout (a sub-agent
// worktree or a container root). Home rules and the approval surface carry
// over; workspace rules are rebuilt around the new root, and the grant cache is
// fresh so an approval in one scope cannot widen another.
func (h *HarnessPaths) WithWorkspaceRoot(root string) *HarnessPaths {
	if h == nil {
		return nil
	}
	derived := &HarnessPaths{
		home:      h.home,
		configDir: h.configDir,
		Ask:       h.Ask,
	}
	if root = canonicalDir(root); root != "" {
		derived.roots = []string{root}
	}
	derived.rules = derived.buildRules()
	return derived
}

// CheckWrite refuses or asks for a write to abs (already resolved by the
// caller). It is a no-op for paths outside the protected set.
func (h *HarnessPaths) CheckWrite(abs string) error {
	if h == nil {
		return nil
	}
	path := filepath.Clean(abs)
	verdict, reason := h.Classify(path)
	switch verdict {
	case HarnessDeny:
		return &HarnessRefusalError{Path: path, Reason: reason, Deny: true}
	case HarnessAsk:
		if h.ask(path, reason) {
			return nil
		}
		return &HarnessRefusalError{Path: path, Reason: reason}
	default:
		return nil
	}
}

// CheckWrites classifies a batch of host-relative paths under root and returns
// the protected ones. apply_out uses it to refuse a delta that would land
// harness configuration, rather than asking once for a whole batch whose
// contents the approval prompt cannot show.
func (h *HarnessPaths) CheckWrites(root string, rels []string) []string {
	if h == nil || root == "" {
		return nil
	}
	var blocked []string
	for _, rel := range rels {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if verdict, _ := h.Classify(path); verdict != HarnessAllow {
			blocked = append(blocked, rel)
		}
	}
	return blocked
}

// CheckSandboxWrites classifies a sandbox delta. Backends report changed paths
// inconsistently — the in-memory fake returns absolute paths under the sandbox
// root (/workspace/.yaah), the git lane reports workspace-relative ones — so
// each entry is mapped onto the host tree before classification. An absolute
// path that cannot be mapped is classified as-is rather than skipped: a backend
// that reports host paths already gives the policy something to match.
func (h *HarnessPaths) CheckSandboxWrites(hostDir, sandboxRoot string, paths []string) []string {
	if h == nil || hostDir == "" {
		return nil
	}
	root := strings.TrimSuffix(filepath.ToSlash(sandboxRoot), "/")
	var blocked []string
	for _, p := range paths {
		slashed := filepath.ToSlash(p)
		var hostPath string
		switch {
		case root != "" && strings.HasPrefix(slashed, root+"/"):
			hostPath = filepath.Join(hostDir, filepath.FromSlash(strings.TrimPrefix(slashed, root+"/")))
		case filepath.IsAbs(p):
			hostPath = p
		default:
			hostPath = filepath.Join(hostDir, filepath.FromSlash(slashed))
		}
		if verdict, _ := h.Classify(hostPath); verdict != HarnessAllow {
			blocked = append(blocked, p)
		}
	}
	return blocked
}

// Classify reports what the policy says about abs. Both the input and the rule
// roots are canonicalised first (best effort): callers pass resolved paths from
// the file tools, but direct writers and tests may pass the symlinked form —
// macOS /var → /private/var is the everyday case — and a comparison that mixed
// the two would silently classify every protected path as ordinary.
func (h *HarnessPaths) Classify(abs string) (HarnessVerdict, string) {
	if h == nil {
		return HarnessAllow, ""
	}
	clean := canonicalPath(abs)
	for _, rule := range h.rules {
		if withinRoot(rule.root, clean) && withinPrefix(rule.root, clean, rule.prefix) {
			return rule.verdict, rule.reason
		}
	}
	return HarnessAllow, ""
}

// buildRules orders global rules before workspace rules so the stricter verdict
// wins when a workspace root is or contains the home directory.
func (h *HarnessPaths) buildRules() []harnessRule {
	var rules []harnessRule
	if h.configDir != "" {
		rules = append(rules, harnessRule{
			root:    h.configDir,
			verdict: HarnessDeny,
			reason:  "yaah configuration and state",
		})
	}
	if h.home != "" {
		rules = append(rules,
			harnessRule{root: h.home, prefix: ".agents/roles", verdict: HarnessDeny, reason: "user-level role definitions"},
			harnessRule{root: h.home, prefix: ".agents/skills", verdict: HarnessDeny, reason: "user-level skills"},
			harnessRule{root: h.home, prefix: ".agents/plans", verdict: HarnessAsk, reason: "user-level plans"},
			// Whole-tree fallback: removing or replacing ~/.agents would
			// delete the protected children without naming any of them.
			harnessRule{root: h.home, prefix: ".agents", verdict: HarnessDeny, reason: "user-level agent configuration"},
		)
	}
	for _, root := range h.roots {
		rules = append(rules,
			harnessRule{root: root, prefix: ".agents/roles", verdict: HarnessAsk, reason: "project role definitions"},
			harnessRule{root: root, prefix: ".agents/skills", verdict: HarnessAsk, reason: "project skills"},
			harnessRule{root: root, prefix: ".agents/plans", verdict: HarnessAsk, reason: "project plans"},
			harnessRule{root: root, prefix: ".yaah", verdict: HarnessAsk, reason: "session state"},
			// Whole-tree fallback for RemoveAll/Move of .agents itself.
			harnessRule{root: root, prefix: ".agents", verdict: HarnessAsk, reason: "project agent configuration"},
		)
	}
	return rules
}

// ask consults the approval surface, caching grants per path+reason so a
// multi-step write to the same file (create then edit, or copy then chmod)
// does not re-prompt within a session.
func (h *HarnessPaths) ask(path, reason string) bool {
	if h.Ask == nil {
		return false
	}
	key := path + "\x00" + reason
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.approved[key] {
		return true
	}
	if !h.Ask(path, reason) {
		return false
	}
	if h.approved == nil {
		h.approved = make(map[string]bool)
	}
	h.approved[key] = true
	return true
}

// canonicalDir returns an absolute, symlink-resolved directory (best effort).
// The harness compares already-resolved tool paths against these roots, so an
// unresolved symlinked home would miss every rule.
func canonicalDir(dir string) string {
	if dir == "" {
		return ""
	}
	return canonicalPath(dir)
}

// canonicalPath resolves p to its real location, following the deepest
// existing ancestor for a path whose leaf does not exist yet. It is best
// effort: a path that cannot be resolved is returned clean and absolute.
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	if real, err := resolveExistingAncestor(abs); err == nil {
		return real
	}
	return abs
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func withinPrefix(root, path, prefix string) bool {
	if prefix == "" {
		return withinRoot(root, path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	rel = filepath.ToSlash(rel)
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
		prefix = strings.ToLower(prefix)
	}
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}
