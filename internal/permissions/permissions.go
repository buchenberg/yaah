// Package permissions is the ruleset engine behind role-file and global
// `permissions:` configuration (parity W5): ordered per-tool rules with an
// allow/deny/ask effect and a glob resource, evaluated last-match-wins.
//
// It is a leaf package: no yaah imports, so the rolefile parser, the
// config loader, and the agent loop can all share one rule vocabulary
// without dragging the dependency graph along.
//
// The YAML shape (plan W5.1):
//
//	permissions:
//	  edit: { "*.md": allow, "*": deny }
//	  bash: { "git push*": ask, "git status*": allow }
//	  webfetch: deny
//
// A tool mapped to a bare effect matches every invocation of that tool
// (tool-name-only match); a tool mapped to a resource map matches the
// call's resource string (the shell command for shell tools, the file
// path for file-mutating tools) glob-style, "*" matching anything.
// Rules keep their source order, and the LAST matching rule wins — the
// example denies every edit except *.md files.
package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Effect is what a matching rule does to a tool call.
type Effect string

const (
	// Allow lets the call proceed (still subject to the mode-axis floor:
	// a withheld tool is never re-enabled, and a dangerous call's global
	// approval posture still applies).
	Allow Effect = "allow"
	// Deny strips the call before dispatch; the user cannot approve past it.
	Deny Effect = "deny"
	// Ask routes the call through the approval pipeline: the surface's
	// approve bridge prompts, and a refusal becomes a synthesized denial.
	Ask Effect = "ask"
)

// Rule is one permission rule: an effect applied to calls of one tool whose
// resource matches a glob. An empty Resource matches every invocation of
// the tool (tool-name-only).
type Rule struct {
	Tool     string `json:"tool"`
	Resource string `json:"resource,omitempty"`
	Effect   Effect `json:"effect"`
}

// Rules is an ordered rule list, evaluated last-match-wins. The order is
// the YAML source order within one document; composition (global config
// rules then persona rules) appends so the later — persona — rules win on
// overlap, the opencode semantics the plan pins.
type Rules []Rule

// Match returns the effect of the last rule matching this call. The second
// return is false when no rule matches, which is not a verdict: the caller
// falls through to the configured approval posture.
func (r Rules) Match(tool, resource string) (Effect, bool) {
	var hit Effect
	found := false
	for _, rule := range r {
		if rule.Tool != tool {
			continue
		}
		if rule.Resource != "" && !globMatch(rule.Resource, resource) {
			continue
		}
		hit = rule.Effect
		found = true
	}
	return hit, found
}

// AskRules returns the rules whose effect is Ask, in source order. It is
// what a loading surface needs to name the exact boundaries a headless run
// cannot honour: a sub-agent dispatch has no approval bridge, so an `ask`
// rule there is a boundary whose enforcement point does not exist (the
// dispatch denies it — see the role registry's load-time warning).
func AskRules(rules Rules) []Rule {
	var out []Rule
	for _, rule := range rules {
		if rule.Effect == Ask {
			out = append(out, rule)
		}
	}
	return out
}

// globMatch reports whether pattern matches value. Patterns are
// workspace-relative globs using forward slashes; the value is normalized
// the same way so Windows backslash paths match.
//
// Both `*` and `**` cross directory separators and `?` matches any single
// character — broader than a shell glob or gitignore pattern, where `*`
// stays within one path segment. That breadth is what makes the plan's
// example behave as written: `"*.md": allow` followed by `"*": deny`
// allows any markdown file anywhere and denies the rest, and a command
// glob like `git push*` prefixes the whole command. Escaping is not
// supported; an invalid pattern (unbalanced bracket) matches nothing: a
// rule that cannot be interpreted must not widen what its author wrote.
func globMatch(pattern, value string) bool {
	re, err := globRegexp(strings.ReplaceAll(pattern, "\\", "/"))
	if err != nil {
		return false
	}
	return re.MatchString(strings.ReplaceAll(value, "\\", "/"))
}

// globRegexp compiles a resource glob into an anchored regexp. Only the
// metacharacters the glob vocabulary defines are interpreted; everything
// else — including regex metacharacters — is literal.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// UnmarshalYAML decodes the permission map preserving source order.
//
// Each key is a tool name; its value is either a bare effect (every call of
// that tool) or a mapping of resource glob → effect. A plain Go map would
// lose the order the last-match-wins rule depends on, so the node tree is
// walked directly.
func (r *Rules) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("permissions must be a mapping of tool names, got %v", value.Tag)
	}
	var out Rules
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, val := value.Content[i], value.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("permissions keys must be tool names, got %v", key.Tag)
		}
		switch val.Kind {
		case yaml.ScalarNode:
			effect, err := parseEffect(val.Value)
			if err != nil {
				return fmt.Errorf("permissions.%s: %w", key.Value, err)
			}
			out = append(out, Rule{Tool: key.Value, Effect: effect})
		case yaml.MappingNode:
			for j := 0; j+1 < len(val.Content); j += 2 {
				res, effNode := val.Content[j], val.Content[j+1]
				if res.Kind != yaml.ScalarNode || effNode.Kind != yaml.ScalarNode {
					return fmt.Errorf("permissions.%s: entries must be resource: effect pairs", key.Value)
				}
				effect, err := parseEffect(effNode.Value)
				if err != nil {
					return fmt.Errorf("permissions.%s.%s: %w", key.Value, res.Value, err)
				}
				out = append(out, Rule{Tool: key.Value, Resource: res.Value, Effect: effect})
			}
		default:
			return fmt.Errorf("permissions.%s: expected an effect or a resource map, got %v", key.Value, val.Tag)
		}
	}
	*r = out
	return nil
}

// MarshalYAML renders rules back into the map form the parser reads, so a
// role-file rewrite emits the compact shape rather than a rule array.
// Lossless for parsed input: source order is preserved.
func (r Rules) MarshalYAML() (any, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	// Group consecutive same-tool rules to reconstruct the per-tool maps;
	// grouping by first occurrence keeps source order stable.
	order := make([]string, 0, len(r))
	byTool := make(map[string][]Rule, len(r))
	for _, rule := range r {
		if _, seen := byTool[rule.Tool]; !seen {
			order = append(order, rule.Tool)
		}
		byTool[rule.Tool] = append(byTool[rule.Tool], rule)
	}
	for _, tool := range order {
		rules := byTool[tool]
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: tool}
		if len(rules) == 1 && rules[0].Resource == "" {
			out.Content = append(out.Content, key,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(rules[0].Effect)})
			continue
		}
		val := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, rule := range rules {
			val.Content = append(val.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: rule.Resource},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(rule.Effect)})
		}
		out.Content = append(out.Content, key, val)
	}
	return out, nil
}

// parseEffect validates the effect vocabulary. An unknown value is an
// error, not a fallback: defaulting a typo to allow would widen what the
// file meant to narrow, and defaulting it to deny would silently break
// every rule after a rename. The failure is loud at load.
func parseEffect(v string) (Effect, error) {
	switch Effect(v) {
	case Allow, Deny, Ask:
		return Effect(v), nil
	default:
		return "", fmt.Errorf("unknown effect %q (want allow, deny, or ask)", v)
	}
}

// shellTools take a command string as their permission resource.
var shellTools = map[string]bool{
	"bash":       true,
	"powershell": true,
}

// resourceKeys are the argument names a non-shell tool's target can
// arrive under, in probe order: the file-mutating tools carry
// "filePath" (write/edit/append/delete/patch), the move-family tools
// (move/copy/bulk/replace/mkdir/rmdir) carry "path" and a destination
// "to", and webfetch carries "url". A rule matches against the first
// non-empty value, so e.g. `move: { "docs/*": deny }` and
// `webfetch: { "*evil*": deny }` do exactly what they read like.
var resourceKeys = []string{"filePath", "path", "to", "url"}

// ResourceFor extracts the string a permission rule matches against for
// this call: the shell command for shell tools, the first non-empty
// target argument (filePath/path/to/url) for everything else, and ""
// (tool-name-only matching) when none is present. Unparseable JSON
// yields "" — an unparseable call cannot be shown to match a narrowing
// glob, so only name-only rules catch it.
func ResourceFor(tool, argsJSON string) string {
	if shellTools[tool] {
		var shell struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &shell); err != nil {
			return ""
		}
		return shell.Command
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
		return ""
	}
	for _, key := range resourceKeys {
		if v, ok := payload[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// MutatingFileTools lists the tools whose calls mutate the file tree:
// the write/edit family, the move/copy family, and the bulk/patch/replace
// forms. It is the set the delegate-inheritance ceiling (parity W5.7)
// propagates: a deny rule on one of these binds every sub-agent dispatch.
// Shell tools are deliberately absent — inheriting broad shell denies
// breaks sub-agents in confusing ways (the Kilo #11523 lesson): a
// sub-agent that cannot run git cannot do its job, and the failure shows
// up as a baffling tool error one level removed from the rule that caused
// it. Shell restriction stays the parent's job (the mode axis and the
// supervision guard already act there).
var MutatingFileTools = map[string]bool{
	"write":      true,
	"edit":       true,
	"append":     true,
	"delete":     true,
	"patch":      true,
	"move":       true,
	"move_tree":  true,
	"copy":       true,
	"copy_tree":  true,
	"mkdir":      true,
	"rmdir":      true,
	"bulk_files": true,
	"replace":    true,
	"apply_out":  true,
}

// InheritedCeiling filters a ruleset down to what sub-agent dispatches
// inherit (parity W5.7): deny rules on mutating file tools, nothing else.
// ask rules cannot propagate (a sub-agent has no one to ask), allow rules
// must not (a ceiling only narrows), and everything outside the mutating
// set stays the parent's own gate.
func InheritedCeiling(rules Rules) Rules {
	var out Rules
	for _, rule := range rules {
		if rule.Effect == Deny && MutatingFileTools[rule.Tool] {
			out = append(out, rule)
		}
	}
	return out
}

// inheritedKey is the context key the parent loop's dispatch uses to hand
// its inherited ceiling to the sub-agent runner. It lives here — a leaf
// package both sides already import — because the agent and runner
// packages cannot import each other.
type inheritedKey struct{}

// WithInherited returns a context carrying the inherited ceiling for a
// sub-agent dispatch. Nil or empty rules inherit nothing (the zero-value
// read below) — behavior is exactly the pre-W5.7 dispatch.
func WithInherited(ctx context.Context, rules Rules) context.Context {
	return context.WithValue(ctx, inheritedKey{}, rules)
}

// Inherited extracts the ceiling a parent dispatch carried. Nil when the
// context carries none.
func Inherited(ctx context.Context) Rules {
	rules, _ := ctx.Value(inheritedKey{}).(Rules)
	return rules
}
