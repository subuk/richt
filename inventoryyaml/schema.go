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
