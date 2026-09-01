// Package rolefile is the single parser/marshaler for sub-agent role
// files (markdown with YAML frontmatter). Both the role registry
// (internal/agent/subagent) and the role tool (internal/tools) consume
// it, so the read and write paths cannot drift apart — a drift that
// once silently dropped the response contract on edit (review B3).
// It is a leaf package: no yaah imports.
package rolefile

import (
	"fmt"
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
func Marshal(fm Frontmatter, body string) (string, error) {
	fmBytes, err := yaml.Marshal(&fm)
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
