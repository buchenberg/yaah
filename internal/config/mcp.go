package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AddMCPServer adds or replaces an MCP server entry in config.yaml,
// preserving all other sections, comments, and formatting.
func AddMCPServer(path, name string, cfg MCPServerConfig) error {
	doc, err := readConfigNode(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("config root is not a mapping")
	}

	mcpSlot := findValueByKey(mapping, "mcp_servers")
	if mcpSlot == nil {
		mcpSlot = &yaml.Node{Kind: yaml.MappingNode}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "mcp_servers"},
			mcpSlot,
		)
	}

	serverNode, err := serverToNode(name, cfg)
	if err != nil {
		return fmt.Errorf("encode server config: %w", err)
	}

	removeByKey(mcpSlot, name)
	mcpSlot.Content = append(mcpSlot.Content, serverNode.Content...)

	return writeConfigNode(path, doc)
}

// RemoveMCPServer removes an MCP server entry from config.yaml.
// If the mcp_servers mapping becomes empty, the key is removed entirely.
func RemoveMCPServer(path, name string) error {
	doc, err := readConfigNode(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("config root is not a mapping")
	}

	mcpSlot := findValueByKey(mapping, "mcp_servers")
	if mcpSlot == nil {
		return fmt.Errorf("MCP server %q not found (no mcp_servers section)", name)
	}

	found := false
	for i := 0; i < len(mcpSlot.Content); i += 2 {
		if mcpSlot.Content[i].Value == name {
			mcpSlot.Content = append(mcpSlot.Content[:i], mcpSlot.Content[i+2:]...)
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("MCP server %q not found", name)
	}

	if len(mcpSlot.Content) == 0 {
		removeByKey(mapping, "mcp_servers")
	}

	return writeConfigNode(path, doc)
}

// readConfigNode reads a YAML file into a yaml.Node document.
// If the file doesn't exist, returns a new empty document node.
func readConfigNode(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			doc := &yaml.Node{Kind: yaml.DocumentNode}
			doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode})
			return doc, nil
		}
		return nil, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode}
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode})
	}
	return &doc, nil
}

// writeConfigNode ensures the parent directory exists and writes the node as YAML.
func writeConfigNode(path string, doc *yaml.Node) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}

// findValueByKey searches a MappingNode for a key and returns the value node.
// Returns nil if the key is not found.
func findValueByKey(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// removeByKey removes a key-value pair from a MappingNode by key.
func removeByKey(mapping *yaml.Node, key string) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

// serverToNode converts an MCPServerConfig + name to a yaml.MappingNode.
func serverToNode(name string, cfg MCPServerConfig) (*yaml.Node, error) {
	wrapper := map[string]MCPServerConfig{name: cfg}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		return nil, err
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}

	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0], nil
	}
	return nil, fmt.Errorf("unexpected yaml node structure")
}
