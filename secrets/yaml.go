package secrets

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

type yamlSource struct {
	file   string
	once   sync.Once
	values map[string]string
	err    error
}

func (s *yamlSource) Lookup(key string) (string, error) {
	s.once.Do(s.load)
	if s.err != nil {
		return "", s.err
	}
	value, ok := s.values[key]
	if !ok {
		return "", errUnknownKey
	}
	return value, nil
}

func (s *yamlSource) load() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		s.err = errReadSource
		return
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil || len(root.Content) != 1 {
		s.err = errInvalidSource
		return
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		s.err = errInvalidSource
		return
	}
	values := make(map[string]string)
	if !collectValues(root.Content[0], "", values) {
		s.err = errInvalidSource
		return
	}
	s.values = values
}

func collectValues(node *yaml.Node, prefix string, values map[string]string) bool {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return false
	}
	seen := make(map[string]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || strings.Contains(key.Value, ".") || seen[key.Value] {
			return false
		}
		seen[key.Value] = true
		name := prefix + key.Value
		switch {
		case value.Kind == yaml.MappingNode:
			if !collectValues(value, name+".", values) {
				return false
			}
		case value.Kind == yaml.ScalarNode && value.Tag == "!!str":
			values[name] = value.Value
		default:
			return false
		}
	}
	return true
}
