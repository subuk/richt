package inventory

import (
	"fmt"
	"regexp"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateSecretSource checks a named declaration without reading its values.
func ValidateSecretSource(name string, source SecretSource) error {
	if err := validName("secret source", name); err != nil {
		return err
	}
	switch source.Type {
	case "yaml":
		if source.Env != nil {
			return fmt.Errorf("secret source %q env: forbidden for type yaml", name)
		}
		if strings.TrimSpace(source.File) == "" {
			return fmt.Errorf("secret source %q file: required", name)
		}
	case "env":
		if source.File != "" {
			return fmt.Errorf("secret source %q file: forbidden for type env", name)
		}
		if source.Env == nil {
			return fmt.Errorf("secret source %q env: required mapping", name)
		}
		for _, key := range sortedKeys(source.Env) {
			for part := range strings.SplitSeq(key, ".") {
				if part == "" {
					return fmt.Errorf("secret source %q env: keys must have nonempty dot-separated segments", name)
				}
			}
			if !environmentNamePattern.MatchString(source.Env[key]) {
				return fmt.Errorf("secret source %q env: expected portable environment variable names", name)
			}
		}
	default:
		return fmt.Errorf("secret source %q type: expected yaml or env", name)
	}
	return nil
}
