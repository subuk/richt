package inventoryyaml

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// ValidateSecretSourceDeclarations checks source declaration syntax before YAML
// decoding can erase field presence or coerce non-string mapping values. The
// node is the secret_sources mapping, not the containing document.
func ValidateSecretSourceDeclarations(node *yaml.Node) error {
	if err := stringKeys(node, make(map[*yaml.Node]bool)); err != nil {
		return err
	}
	return mappingFields(node, func(name string, declaration *yaml.Node) error {
		declaration = dereference(declaration)
		if declaration.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: secret source %q must be a mapping", declaration.Line, name)
		}
		fields := map[string]*yaml.Node{}
		if err := mappingFields(declaration, func(field string, value *yaml.Node) error {
			fields[field] = value
			return nil
		}); err != nil {
			return err
		}
		typeNode := fields["type"]
		if typeNode == nil {
			return fmt.Errorf("secret source %q type: required", name)
		}
		if err := nonemptyString(typeNode, "secret source type"); err != nil {
			return err
		}
		switch dereference(typeNode).Value {
		case "yaml":
			if value := fields["env"]; value != nil {
				return fmt.Errorf("line %d: secret source env is forbidden for type yaml", value.Line)
			}
			if value := fields["file"]; value != nil {
				return nonemptyString(value, "secret source file")
			}
			return fmt.Errorf("secret source %q file: required", name)
		case "env":
			if value := fields["file"]; value != nil {
				return fmt.Errorf("line %d: secret source file is forbidden for type env", value.Line)
			}
			value := fields["env"]
			if value == nil {
				return fmt.Errorf("secret source %q env: required mapping", name)
			}
			value = dereference(value)
			if value.Kind != yaml.MappingNode {
				return fmt.Errorf("line %d: secret source env must be a mapping", value.Line)
			}
			for i := 0; i < len(value.Content); i += 2 {
				key, variable := value.Content[i], dereference(value.Content[i+1])
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || variable.Kind != yaml.ScalarNode || variable.Tag != "!!str" {
					return fmt.Errorf("line %d: secret source env keys and values must be strings", key.Line)
				}
			}
		}
		return nil
	})
}
