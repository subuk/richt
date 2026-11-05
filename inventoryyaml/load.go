// Package inventoryyaml loads local YAML into the inventory domain model.
package inventoryyaml

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/subuk/richt/inventory"
	"go.yaml.in/yaml/v3"
)

type document struct {
	Tags          map[string]inventory.Tag          `yaml:"tags"`
	Services      map[string]serviceDefinition      `yaml:"services"`
	Hosts         []inventory.Host                  `yaml:"hosts"`
	SecretSources map[string]inventory.SecretSource `yaml:"secret_sources"`
}

type serviceDefinition struct {
	File                        string `yaml:"file"`
	inventory.ServiceDefinition `yaml:",inline"`
}

// Load reads inventory and explicitly referenced service definitions. Templates
// and secret files are not opened. Paths are based on the file declaring them,
// never the current working directory.
func Load(filename string) (*inventory.Inventory, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("inventory path: %w", err)
	}
	var doc document
	if err := read(filename, &doc); err != nil {
		return nil, err
	}
	in := &inventory.Inventory{Tags: doc.Tags, Hosts: doc.Hosts, SecretSources: doc.SecretSources, Services: map[string]inventory.ServiceDefinition{}}
	for _, name := range keys(doc.Services) {
		raw := doc.Services[name]
		definition := raw.ServiceDefinition
		source := filename
		if raw.File != "" {
			source, err = localPath(filename, raw.File)
			if err != nil {
				return nil, fmt.Errorf("service %q file: %w", name, err)
			}
			if err := read(source, &definition); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
		}
		s := &definition.Service
		s.SourceFile = source
		if err := resolvePaths(s, source); err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		in.Services[name] = definition
	}
	for _, name := range keys(in.SecretSources) {
		s := in.SecretSources[name]
		if s.File != "" {
			s.File, err = localPath(filename, s.File)
			if err != nil {
				return nil, fmt.Errorf("secret source %q file: %w", name, err)
			}
		}
		in.SecretSources[name] = s
	}
	if err := in.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return in, nil
}

func read(filename string, value any) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("%s: %w", filename, err)
	}
	if err := stringKeys(&root, make(map[*yaml.Node]bool)); err != nil {
		return fmt.Errorf("%s: %w", filename, err)
	}
	var schemaErr error
	switch value.(type) {
	case *document:
		schemaErr = inventorySchema(&root)
	case *inventory.ServiceDefinition:
		schemaErr = serviceDefinitionSchema(&root, false)
	}
	if schemaErr != nil {
		return fmt.Errorf("%s: %w", filename, schemaErr)
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("%s: %w", filename, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return fmt.Errorf("%s: %w", filename, err)
		}
		return fmt.Errorf("%s: expected exactly one YAML document", filename)
	}
	return nil
}

func stringKeys(node *yaml.Node, visited map[*yaml.Node]bool) error {
	if visited[node] {
		return nil
	}
	visited[node] = true
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || (key.Tag != "!!str" && key.Tag != "!!merge") {
				return fmt.Errorf("line %d: unsupported mapping key; keys must be strings", key.Line)
			}
		}
	}
	for _, child := range node.Content {
		if err := stringKeys(child, visited); err != nil {
			return err
		}
	}
	if node.Alias != nil {
		return stringKeys(node.Alias, visited)
	}
	return nil
}

func localPath(source, value string) (string, error) {
	if strings.Contains(value, "://") || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("expected a local file path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(source), value)
	}
	return filepath.Clean(value), nil
}

func resolvePaths(s *inventory.Service, source string) error {
	for i := range s.Binaries {
		b := &s.Binaries[i]
		if b.Type == "download" || b.Type == "archive" {
			if strings.IndexFunc(b.Path, unicode.IsControl) >= 0 {
				return fmt.Errorf("binary.%d path: control characters are not allowed", i+1)
			}
			if b.Path != "" {
				b.Path = path.Clean(b.Path)
			}
			b.SHA256 = strings.ToLower(b.SHA256)
		}
	}
	resolve := func(c inventory.Config) (inventory.Config, error) {
		if c.Path != "" {
			c.Path = path.Clean(c.Path)
		}
		if c.Array != "" {
			c.Array = path.Clean(c.Array)
		}
		if c.Source != "" {
			c.Source = path.Clean(c.Source)
		}
		if c.Target != "" {
			c.Target = path.Clean(c.Target)
		}
		c.Members = append([]string(nil), c.Members...)
		for i, p := range c.Members {
			c.Members[i] = path.Clean(p)
		}
		c.Devices = append([]string(nil), c.Devices...)
		for i, p := range c.Devices {
			c.Devices[i] = path.Clean(p)
		}
		if c.Template.File != "" {
			var err error
			c.Template.File, err = localPath(source, c.Template.File)
			if err != nil {
				return c, err
			}
		}
		return c, nil
	}
	for i, config := range s.Configs {
		name := config.Name
		if name == "" {
			name = fmt.Sprintf("[%d]", i+1)
		}
		c, err := resolve(config)
		if err != nil {
			return fmt.Errorf("config.%s template.file: %w", name, err)
		}
		s.Configs[i] = c
	}
	for i, p := range s.Processes {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("[%d]", i+1)
		}
		if p.UnitFile != nil {
			c := *p.UnitFile
			if c.Path == "" {
				c.Path = "/etc/systemd/system/" + p.Unit
			}
			c, err := resolve(c)
			if err != nil {
				return fmt.Errorf("process.%s unit_file template.file: %w", name, err)
			}
			p.UnitFile = &c
		}
		for _, key := range keys(p.Overrides) {
			c := p.Overrides[key]
			if c.Path == "" {
				c.Path = "/etc/systemd/system/" + p.Unit + ".d/" + key
			}
			c, err := resolve(c)
			if err != nil {
				return fmt.Errorf("process.%s overrides %q template.file: %w", name, key, err)
			}
			p.Overrides[key] = c
		}
		s.Processes[i] = p
	}
	return nil
}

func keys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Stable order also makes file and validation errors reproducible.
	slices.Sort(keys)
	return keys
}
