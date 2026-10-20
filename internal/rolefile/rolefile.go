// Package rolefile is the single parser/marshaler for sub-agent role
// files (markdown with YAML frontmatter). Both the role registry
// (internal/agent/subagent) and the role tool (internal/tools) consume
// it, so the read and write paths cannot drift apart — a drift that
// once silently dropped the response contract on edit (review B3).
// It is a leaf package: no yaah imports.
package rolefile

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// ContractField names a field in a sub-agent's response contract and
// optionally classifies it as evidence (raw tool output, verifiable) or
// interpretation (model synthesis, may need verification).
type ContractField struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"` // "evidence" or "interpretation"; empty = interpretation
}

// UnmarshalYAML accepts both string and map forms so existing role YAMLs
// remain valid:
//   - "fieldname"            → ContractField{Name: "fieldname"}
//   - {name: field, kind: e} → ContractField{Name: "field", Kind: "e"}
func (f *ContractField) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		f.Name = value.Value
		return nil
	case yaml.MappingNode:
		type raw ContractField
		return value.Decode((*raw)(f))
	default:
		return fmt.Errorf("contract field must be a string or map, got %v", value.Tag)
	}
}

// ContractDef describes the structured output block a sub-agent must
// append to its response so the main agent can extract data reliably.
type ContractDef struct {
	Heading string          `yaml:"heading"`
	Fields  []ContractField `yaml:"fields"`
}

// Frontmatter is the full YAML frontmatter of a role file. It must stay
// field-for-field complete: any role field missing here is silently
// dropped when a role file is rewritten.
type Frontmatter struct {
	Name          string      `yaml:"name"`
	Description   string      `yaml:"description"`
	Specialty     string      `yaml:"specialty"`
	Contract      ContractDef `yaml:"contract,omitempty"`
	Tools         []string    `yaml:"tools"`
	MaxLoopCycles int         `yaml:"max_iterations"`
	MinLoopCycles int         `yaml:"min_iterations"` // budget floor; 0 = none
	MaxToolTurns  int         `yaml:"max_turns"`
	MinToolTurns  int         `yaml:"min_turns"` // budget floor; 0 = none
	JSONMode      bool        `yaml:"json_mode"`
	Timeout       int         `yaml:"timeout"` // seconds; 0 = no timeout

	// extra holds frontmatter keys this struct does not model, in source
	// order. They are preserved through Parse→Marshal so editing a role does
	// not silently drop provider/project extensions.
	extra []extraField
	// order records every top-level key in source order, so rewrites do not
	// move extensions relative to modeled fields.
	order []string
}

type extraField struct {
	key   yaml.Node
	value yaml.Node
}

var knownFrontmatterKeys = func() map[string]bool {
	keys := make(map[string]bool)
	t := reflect.TypeOf(Frontmatter{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" {
			continue
		}
		keys[strings.Split(tag, ",")[0]] = true
	}
	return keys
}()

// cloneNode returns a deep copy with aliases inlined and anchors cleared.
// Cycles (a self-referential anchor) collapse to null. A shallow alias copy
// would share the target's Content slice and could create a cyclic node graph
// the YAML emitter never finishes.
func cloneNode(src *yaml.Node, stack map[*yaml.Node]bool) yaml.Node {
	if src == nil {
		return yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	if src.Kind == yaml.AliasNode && src.Alias != nil {
		if stack[src.Alias] {
			return yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		stack[src.Alias] = true
		expanded := cloneNode(src.Alias, stack)
		stack[src.Alias] = false
		expanded.Anchor = ""
		return expanded
	}
	out := *src
	out.Anchor = ""
	out.Alias = nil
	out.Content = make([]*yaml.Node, len(src.Content))
	stack[src] = true
	for i, child := range src.Content {
		copied := cloneNode(child, stack)
		out.Content[i] = &copied
	}
	stack[src] = false
	return out
}

// UnmarshalYAML decodes the modeled fields, records the source key order, and
// stashes every other key with its original key/value nodes. It deliberately
// decodes through an alias type so this method is not re-entered for the
// known fields.
func (f *Frontmatter) UnmarshalYAML(value *yaml.Node) error {
	type plain Frontmatter
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*f = Frontmatter(p)
	if value.Kind != yaml.MappingNode {
		return nil
	}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		if keyNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("role frontmatter keys must be scalar strings")
		}
		name := keyNode.Value
		if seen[name] {
			// Duplicate keys are invalid YAML; keep the first so a rewrite
			// does not emit an invalid mapping.
			continue
		}
		seen[name] = true
		f.order = append(f.order, name)
		if knownFrontmatterKeys[name] {
			continue
		}
		val := cloneNode(value.Content[i+1], make(map[*yaml.Node]bool))
		f.extra = append(f.extra, extraField{key: *keyNode, value: val})
	}
	return nil
}

// Parse splits role file content into its YAML frontmatter and markdown
// body. The content must start with a "---" fence; the frontmatter is
// unmarshaled into a Frontmatter and the trimmed remainder is returned
// as the body.
func Parse(content string) (Frontmatter, string, error) {
	var fm Frontmatter
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return fm, "", fmt.Errorf("role file must start with YAML frontmatter (---)")
	}
	rest := content[3:]
	closingLen := 4
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		idx = strings.Index(rest, "\r\n---")
		closingLen = 5
	}
	if idx < 0 {
		return fm, "", fmt.Errorf("role file has unterminated YAML frontmatter (missing closing ---)")
	}
	yamlBlock := rest[:idx]
	body := strings.TrimSpace(rest[idx+closingLen:])

	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return fm, "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	return fm, body, nil
}

// Marshal serializes frontmatter plus body back into the role file
// format (--- fences around YAML, blank line before the body). An empty
// body produces a frontmatter-only file.
// Marshal serializes frontmatter plus body back into the role file
// format (--- fences around YAML, blank line before the body). An empty
// body produces a frontmatter-only file.
func Marshal(fm Frontmatter, body string) (string, error) {
	type plain Frontmatter
	knownBytes, err := yaml.Marshal((*plain)(&fm))
	if err != nil {
		return "", err
	}
	var knownDoc yaml.Node
	if err := yaml.Unmarshal(knownBytes, &knownDoc); err != nil {
		return "", err
	}
	if len(knownDoc.Content) == 0 || knownDoc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("marshal role frontmatter: unexpected YAML shape")
	}
	knownMap := knownDoc.Content[0]
	knownValues := make(map[string]*yaml.Node, len(knownMap.Content)/2)
	for i := 0; i+1 < len(knownMap.Content); i += 2 {
		knownValues[knownMap.Content[i].Value] = knownMap.Content[i+1]
	}
	extraByKey := make(map[string]extraField, len(fm.extra))
	for _, extra := range fm.extra {
		extraByKey[extra.key.Value] = extra
	}

	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	used := make(map[string]bool)
	appendKey := func(name string) {
		if used[name] {
			return
		}
		if value, ok := knownValues[name]; ok {
			out.Content = append(out.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
				value)
			used[name] = true
			return
		}
		if extra, ok := extraByKey[name]; ok {
			key := extra.key
			value := extra.value
			out.Content = append(out.Content, &key, &value)
			used[name] = true
		}
	}
	for _, name := range fm.order {
		appendKey(name)
	}
	for i := 0; i+1 < len(knownMap.Content); i += 2 {
		appendKey(knownMap.Content[i].Value)
	}
	for _, extra := range fm.extra {
		appendKey(extra.key.Value)
	}

	fmBytes, err := yaml.Marshal(out)
	if err != nil {
		return "", err
	}
	fmText := strings.TrimRight(string(fmBytes), "\n")
	if strings.TrimSpace(body) == "" {
		return fmt.Sprintf("---\n%s\n---\n", fmText), nil
	}
	body = strings.TrimSpace(body)
	return fmt.Sprintf("---\n%s\n---\n\n%s\n", fmText, body), nil
}
