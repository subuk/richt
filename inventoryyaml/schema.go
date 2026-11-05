package inventoryyaml

import (
	"fmt"
	"strings"

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
				if err := serviceDefinitionSchema(definition, true); err != nil {
					return fmt.Errorf("service %q: %w", name, err)
				}
				return nil
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

// A definition contains its order and components, or refers to a file containing
// that complete definition. Field presence matters even for zero or null values.
func serviceDefinitionSchema(root *yaml.Node, allowFile bool) error {
	if root.Kind == yaml.DocumentNode && len(root.Content) != 0 {
		root = root.Content[0]
	}
	fields := map[string]*yaml.Node{}
	if err := mappingFields(root, func(key string, value *yaml.Node) error {
		fields[key] = value
		return nil
	}); err != nil {
		return err
	}
	if file := fields["file"]; allowFile && file != nil {
		if order := fields["order"]; order != nil {
			return fmt.Errorf("line %d: file cannot be combined with order; set order in the referenced service file", order.Line)
		}
		for _, key := range []string{"binaries", "configs", "processes"} {
			if value := fields[key]; value != nil {
				return fmt.Errorf("line %d: file cannot be combined with inline components", value.Line)
			}
		}
		file = dereference(file)
		if file.Kind != yaml.ScalarNode || file.Tag != "!!str" || strings.TrimSpace(file.Value) == "" {
			return fmt.Errorf("line %d: service file must be a nonempty string", file.Line)
		}
		return nil
	}
	if !allowFile {
		if file := fields["file"]; file != nil {
			return fmt.Errorf("line %d: field file is not allowed in a referenced service definition", file.Line)
		}
	}
	if order := fields["order"]; order != nil {
		if err := integer(order, "order"); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("order: required integer")
	}
	return serviceSchema(root)
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
					if field == "binaries" && key == "disable_excludes" {
						value = dereference(value)
						if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
							return fmt.Errorf("line %d: binaries item %d disable_excludes must be a nonempty string when supplied", value.Line, i+1)
						}
					}
					if field == "processes" {
						switch key {
						case "unit_file":
							if err := ownedConfigSchema(value); err != nil {
								return err
							}
						case "overrides":
							if err := mappingFields(value, func(_ string, config *yaml.Node) error { return ownedConfigSchema(config) }); err != nil {
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
				if field == "binaries" {
					if err := binarySchema(item); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// Binary backends have disjoint fields even when supplied values are empty or
// null. Artifact inputs are typed explicitly rather than using YAML coercions.
func binarySchema(node *yaml.Node) error {
	fields := map[string]*yaml.Node{}
	if err := mappingFields(node, func(key string, value *yaml.Node) error { fields[key] = value; return nil }); err != nil {
		return err
	}
	typ := ""
	if fields["type"] == nil {
		// Preserve decoder diagnostics for malformed component mappings.
		return nil
	}
	if n := fields["type"]; n != nil {
		n = dereference(n)
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return fmt.Errorf("line %d: binary type must be a string", n.Line)
		}
		typ = n.Value
	}
	allowed := map[string]string{
		"rpm":      "manager package version disable_excludes",
		"download": "url sha256 path owner group mode timeout",
		"archive":  "url sha256 path owner group timeout format strip_components",
	}
	list, ok := allowed[typ]
	if !ok {
		return fmt.Errorf("binary type: expected rpm, download or archive")
	}
	valid := map[string]bool{"name": true, "type": true}
	for _, key := range strings.Fields(list) {
		valid[key] = true
	}
	return mappingFields(node, func(key string, value *yaml.Node) error {
		if !valid[key] {
			return fmt.Errorf("line %d: field %s is not allowed: %s binary cannot set %s", value.Line, key, typ, key)
		}
		if key == "strip_components" {
			return integer(value, "binary strip_components")
		}
		if typ != "rpm" && key != "name" && key != "type" {
			value = dereference(value)
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return fmt.Errorf("line %d: binary %s must be a nonempty string when supplied", value.Line, key)
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

// Config backends have disjoint fields, including explicitly empty values.
func configSchema(node *yaml.Node) error {
	fields := map[string]*yaml.Node{}
	if err := mappingFields(node, func(key string, value *yaml.Node) error { fields[key] = value; return nil }); err != nil {
		return err
	}
	typ := "template"
	if n := fields["type"]; n != nil {
		n = dereference(n)
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Value == "" {
			return fmt.Errorf("line %d: config type: expected a supported type", n.Line)
		}
		typ = n.Value
	}
	allowed := map[string]string{
		"template":  "path owner group mode template sensitive selinux validate",
		"file":      "path owner group mode",
		"directory": "path owner group mode",
		"mdadm":     "array level metadata members filesystem",
		"lvm-vg":    "vg devices",
		"lvm-lv":    "vg lv size filesystem",
		"mount":     "source target filesystem options",
		"user":      "user uid group system home shell comment supplementary_groups",
		"group":     "group gid system",
	}
	list, ok := allowed[typ]
	if !ok {
		return fmt.Errorf("config type: unsupported type %q", typ)
	}
	valid := map[string]bool{"name": true, "type": true}
	for _, key := range strings.Fields(list) {
		valid[key] = true
	}
	return mappingFields(node, func(key string, value *yaml.Node) error {
		if !valid[key] {
			return fmt.Errorf("line %d: field %s is not allowed: %s config cannot set %s", value.Line, key, typ, key)
		}
		if typ == "user" || typ == "group" {
			value = dereference(value)
			switch key {
			case "uid", "gid":
				return integer(value, "config "+key)
			case "system":
				if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
					return fmt.Errorf("line %d: config system must be a boolean", value.Line)
				}
			case "supplementary_groups":
				return stringList(value, "config supplementary_groups")
			case "comment":
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
					return fmt.Errorf("line %d: config comment must be a string", value.Line)
				}
			default:
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
					return fmt.Errorf("line %d: config %s must be a nonempty string", value.Line, key)
				}
			}
			return nil
		}
		switch key {
		case "members", "devices", "options":
			return stringList(value, "config "+key)
		case "array", "level", "metadata", "vg", "lv", "size", "filesystem", "source", "target":
			value = dereference(value)
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("line %d: config %s must be a string", value.Line, key)
			}
		}
		return nil
	})
}

func ownedConfigSchema(node *yaml.Node) error {
	if err := configSchema(node); err != nil {
		return err
	}
	return mappingFields(node, func(key string, value *yaml.Node) error {
		if key == "type" && dereference(value).Value != "template" {
			return fmt.Errorf("line %d: systemd-owned config type: expected template", value.Line)
		}
		return nil
	})
}
