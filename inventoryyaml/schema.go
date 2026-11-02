package inventoryyaml

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// inventorySchema checks syntax that Go's zero values or YAML scalar coercions
// would otherwise hide.
// KnownFields still checks the complete schema when decoding the document.
func inventorySchema(root *yaml.Node) error {
	if root.Kind == yaml.DocumentNode && len(root.Content) != 0 {
		root = root.Content[0]
	}
	return mappingFields(root, func(key string, value *yaml.Node) error {
		switch key {
		case "services":
			return mappingFields(value, func(name string, definition *yaml.Node) error {
				if err := serviceSchema(definition); err != nil {
					return fmt.Errorf("service %q: %w", name, err)
				}
				return mappingFields(definition, func(field string, value *yaml.Node) error {
					if field == "order" {
						return integer(value, fmt.Sprintf("service %q order", name))
					}
					return nil
				})
			})
		case "tags":
			return mappingFields(value, func(name string, tag *yaml.Node) error {
				return mappingFields(tag, func(field string, value *yaml.Node) error {
					context := fmt.Sprintf("tag %q", name)
					switch field {
					case "order":
						return integer(value, context+" order")
					case "services":
						return tagAssignments(value, context)
					}
					return nil
				})
			})
		}
		return nil
	})
}

func tagAssignments(node *yaml.Node, context string) error {
	node = dereference(node)
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s services must be a mapping keyed by service name", node.Line, context)
	}
	return mappingFields(node, func(name string, assignment *yaml.Node) error {
		assignment = dereference(assignment)
		if assignment.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: %s service %q assignment must be a mapping containing only optional state; use {} for the default present state", assignment.Line, context, name)
		}
		return mappingFields(assignment, func(field string, value *yaml.Node) error {
			if field == "state" {
				value = dereference(value)
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || (value.Value != "present" && value.Value != "absent") {
					return fmt.Errorf("line %d: %s service %q state must be present or absent", value.Line, context, name)
				}
			}
			return nil
		})
	})
}

func integer(node *yaml.Node, context string) error {
	node = dereference(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return fmt.Errorf("line %d: %s must be an integer", node.Line, context)
	}
	return nil
}

func dereference(node *yaml.Node) *yaml.Node {
	visited := make(map[*yaml.Node]bool)
	for node.Kind == yaml.AliasNode && node.Alias != nil && !visited[node] {
		visited[node] = true
		node = node.Alias
	}
	return node
}

// mappingFields visits effective fields: explicit keys override merged keys,
// and the first mapping in a merge sequence takes precedence over later ones.
// The decoder still rejects duplicate direct keys and malformed merges.
func mappingFields(node *yaml.Node, visit func(string, *yaml.Node) error) error {
	visited := make(map[*yaml.Node]bool)
	fields := make(map[string]bool)
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		node = dereference(node)
		if visited[node] {
			return nil
		}
		visited[node] = true
		if node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		if node.Kind == yaml.MappingNode {
			// Register direct fields before following merges, regardless of where
			// the merge key appears in the source mapping.
			for i := 0; i < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Tag == "!!merge" || fields[key.Value] {
					continue
				}
				fields[key.Value] = true
				if err := visit(key.Value, value); err != nil {
					return err
				}
			}
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Tag == "!!merge" {
					if err := walk(node.Content[i+1]); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk(node)
}

// serviceSchema requires flat, ordered component mappings, including in bare
// service files. A supplied name must be a nonempty string; omission is valid.
func serviceSchema(root *yaml.Node) error {
	if root.Kind == yaml.DocumentNode && len(root.Content) != 0 {
		root = root.Content[0]
	}
	return mappingFields(root, func(field string, node *yaml.Node) error {
		switch field {
		case "binaries", "configs", "processes":
			node = dereference(node)
			if node.Kind != yaml.SequenceNode {
				return fmt.Errorf("line %d: %s must be a list of component mappings", node.Line, field)
			}
			for i, item := range node.Content {
				item = dereference(item)
				if item.Kind != yaml.MappingNode {
					return fmt.Errorf("line %d: %s item %d must be a component mapping", item.Line, field, i+1)
				}
				if field == "configs" {
					if err := configSchema(item); err != nil {
						return err
					}
				}
				if field == "processes" {
					if err := processSchema(item); err != nil {
						return err
					}
				}
				if err := mappingFields(item, func(key string, value *yaml.Node) error {
					if field == "processes" {
						switch key {
						case "unit_file":
							if err := configSchema(value); err != nil {
								return err
							}
						case "overrides":
							if err := mappingFields(value, func(_ string, config *yaml.Node) error { return configSchema(config) }); err != nil {
								return err
							}
						}
					}
					if key == "name" {
						value = dereference(value)
						if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
							return fmt.Errorf("line %d: %s item %d name must be a nonempty string when supplied", value.Line, field, i+1)
						}
					}
					return nil
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Process backends have disjoint fields, even when supplied values are empty.
func processSchema(node *yaml.Node) error {
	fields := map[string]*yaml.Node{}
	if err := mappingFields(node, func(key string, value *yaml.Node) error {
		fields[key] = value
		return nil
	}); err != nil {
		return err
	}
	typ := ""
	if value := fields["type"]; value != nil {
		typ = dereference(value).Value
	}
	var forbidden []string
	switch typ {
	case "oneshot":
		forbidden = []string{"unit", "state", "enabled", "masked", "restart_on", "reload_on", "unit_file", "overrides"}
	case "systemd":
		forbidden = []string{"command", "run_on"}
	}
	for _, key := range forbidden {
		if value := fields[key]; value != nil {
			return fmt.Errorf("line %d: %s process cannot set %s", value.Line, typ, key)
		}
	}
	for _, key := range []string{"command", "run_on"} {
		if value := fields[key]; value != nil {
			if err := stringList(value, "process "+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func stringList(node *yaml.Node, context string) error {
	node = dereference(node)
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: %s must be a list of strings", node.Line, context)
	}
	for _, item := range node.Content {
		item = dereference(item)
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return fmt.Errorf("line %d: %s must be a list of strings", item.Line, context)
		}
	}
	return nil
}

// Omitted type selects the template backend. Explicit values must name it.
func configSchema(node *yaml.Node) error {
	return mappingFields(node, func(key string, value *yaml.Node) error {
		if key == "type" {
			value = dereference(value)
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value != "template" {
				return fmt.Errorf("line %d: config type: expected template when supplied", value.Line)
			}
		}
		return nil
	})
}
