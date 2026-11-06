// Package inventoryyaml loads local and HTTPS YAML into the inventory domain model.
package inventoryyaml

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/source"
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
	URL                         string `yaml:"url"`
	SHA256                      string `yaml:"sha256"`
	inventory.ServiceDefinition `yaml:",inline"`
}

// LoadOptions supplies source access and client-owned local secret bindings.
// Secret source values and templates are never opened during inventory loading.
type LoadOptions struct {
	Sources       *source.Client
	SHA256        string
	SecretSources map[string]inventory.SecretSource
}

// Load reads inventory and explicitly referenced service definitions. Paths are
// based on the declaring document, never the current working directory.
func Load(filename string) (*inventory.Inventory, error) {
	return LoadWithOptions(context.Background(), filename, LoadOptions{})
}

// LoadWithOptions loads a local inventory or an explicit absolute HTTPS URL.
// Remote documents may only refer to remote service and template sources.
func LoadWithOptions(ctx context.Context, location string, options LoadOptions) (*inventory.Inventory, error) {
	remote := isURLLocation(location)
	var err error
	if remote {
		if err := source.ValidateURL(location); err != nil {
			return nil, err
		}
		if options.SHA256 != "" {
			if err := inventory.ValidateSourceSHA256(options.SHA256); err != nil {
				return nil, err
			}
		}
	} else {
		if options.SHA256 != "" {
			return nil, fmt.Errorf("inventory sha256 requires an HTTPS inventory URL")
		}
		location, err = filepath.Abs(location)
		if err != nil {
			return nil, fmt.Errorf("inventory path: %w", err)
		}
	}
	client := options.Sources
	if client == nil {
		client, err = source.New(source.Options{})
		if err != nil {
			return nil, err
		}
	}
	var doc document
	declaring, err := readDocument(ctx, client, location, options.SHA256, remote, &doc)
	if err != nil {
		return nil, err
	}
	in := &inventory.Inventory{SourceIdentity: location, Tags: doc.Tags, Hosts: doc.Hosts, SecretSources: doc.SecretSources, Services: map[string]inventory.ServiceDefinition{}}
	if in.SecretSources == nil {
		in.SecretSources = map[string]inventory.SecretSource{}
	}
	for _, name := range keys(in.SecretSources) {
		binding := in.SecretSources[name]
		if binding.File != "" {
			binding.File, err = localPath(declaring, binding.File)
			if err != nil {
				return nil, documentError(declaring, remote, "invalid secret source declaration", fmt.Errorf("secret source %q file: %w", name, err))
			}
		}
		in.SecretSources[name] = binding
	}
	for _, name := range keys(options.SecretSources) {
		if _, exists := in.SecretSources[name]; exists {
			return nil, fmt.Errorf("client secret source %q conflicts with an inventory declaration", name)
		}
		binding := options.SecretSources[name]
		if !filepath.IsAbs(binding.File) {
			return nil, fmt.Errorf("client secret source %q file: expected an absolute local file path", name)
		}
		binding.File, err = localPath("", binding.File)
		if err != nil {
			return nil, fmt.Errorf("client secret source %q file: %w", name, err)
		}
		in.SecretSources[name] = binding
	}
	// Resolve and validate every declaration before following any service source.
	// Referenced definitions use a harmless valid placeholder for this first pass;
	// their real content is checked immediately after loading below.
	references := map[string]string{}
	for _, name := range keys(doc.Services) {
		raw := doc.Services[name]
		if raw.File != "" {
			if remote {
				return nil, documentError(declaring, true, "local file references are forbidden in remote documents", nil)
			}
			references[name], err = localPath(declaring, raw.File)
		} else if raw.URL != "" {
			references[name], err = resolveURL(declaring, raw.URL, remote)
			if err == nil {
				err = inventory.ValidateSourceSHA256(raw.SHA256)
			}
		} else {
			definition := raw.ServiceDefinition
			err = resolvePaths(&definition.Service, declaring, remote)
			if err == nil {
				if remote {
					definition.SourceURL = declaring
				} else {
					definition.SourceFile = declaring
				}
				in.Services[name] = definition
			}
		}
		if err != nil {
			return nil, documentError(declaring, remote, "invalid service source declaration", fmt.Errorf("service %q: %w", name, err))
		}
		if raw.File != "" || raw.URL != "" {
			order := 0
			in.Services[name] = inventory.ServiceDefinition{Order: &order, Service: inventory.Service{Binaries: []inventory.Binary{{Type: "rpm", Package: "richt-source-placeholder"}}}}
		}
	}
	if err := in.Validate(); err != nil {
		return nil, documentError(declaring, remote, "invalid inventory definition", err)
	}
	for _, name := range keys(references) {
		raw := doc.Services[name]
		serviceRemote := raw.URL != ""
		var definition inventory.ServiceDefinition
		serviceSource, err := readDocument(ctx, client, references[name], strings.ToLower(raw.SHA256), serviceRemote, &definition)
		if err != nil {
			// readDocument has already redacted remote decoder diagnostics;
			// its HTTPS failures are safe categories supplied by source.Client.
			// Preserve those causes without quoting names from a remote root.
			if remote {
				return nil, fmt.Errorf("%s: referenced service: %w", declaring, err)
			}
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		if err := resolvePaths(&definition.Service, serviceSource, serviceRemote); err != nil {
			return nil, documentError(serviceSource, serviceRemote, "invalid service source declaration", fmt.Errorf("service %q: %w", name, err))
		}
		if serviceRemote {
			definition.SourceURL = serviceSource
		} else {
			definition.SourceFile = serviceSource
		}
		// Validate remote definitions here so decoder/domain errors cannot expose a
		// remote scalar through a later local-inventory diagnostic.
		if serviceRemote {
			check := &inventory.Inventory{Hosts: []inventory.Host{{Name: "source", Transport: inventory.Transport{Type: "local"}}}, Services: map[string]inventory.ServiceDefinition{"source": definition}}
			if err := check.Validate(); err != nil {
				return nil, documentError(serviceSource, true, "invalid service definition", err)
			}
		}
		in.Services[name] = definition
	}
	if err := in.Validate(); err != nil {
		return nil, documentError(declaring, remote, "invalid inventory definition", err)
	}
	return in, nil
}

func isURLLocation(location string) bool {
	return source.IsURL(location)
}

func resolveURL(declaring, reference string, remote bool) (string, error) {
	if remote {
		return source.Resolve(declaring, reference)
	}
	// Using the explicit reference as its own base validates that it is
	// absolute and applies the client's canonical HTTPS spelling.
	return source.Resolve(reference, "")
}

func readDocument(ctx context.Context, client *source.Client, location, checksum string, remote bool, value any) (string, error) {
	var data []byte
	if remote {
		doc, err := client.Read(ctx, location, checksum)
		if err != nil {
			return "", err
		}
		data, location = doc.Data, doc.URL
	} else {
		var err error
		data, err = os.ReadFile(location)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", location, err)
		}
	}
	if err := decodeDocument(data, location, remote, value); err != nil {
		return "", err
	}
	return location, nil
}

var yamlLine = regexp.MustCompile(`line [0-9]+`)

func documentError(location string, remote bool, category string, err error) error {
	if remote {
		line := ""
		if err != nil {
			if match := yamlLine.FindString(err.Error()); match != "" {
				line = " (" + match + ")"
			}
		}
		return fmt.Errorf("%s: %s%s", location, category, line)
	}
	return fmt.Errorf("%s: %w", location, err)
}

func decodeDocument(data []byte, location string, remote bool, value any) error {
	failure := func(category string, err error) error { return documentError(location, remote, category, err) }
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return failure("invalid YAML syntax", err)
	}
	if err := stringKeys(&root, make(map[*yaml.Node]bool)); err != nil {
		return failure("invalid YAML mapping keys", err)
	}
	var schemaErr error
	switch value.(type) {
	case *document:
		schemaErr = inventorySchema(&root)
		if schemaErr == nil && remote {
			schemaErr = mappingFields(documentRoot(&root), func(key string, value *yaml.Node) error {
				if key == "secret_sources" {
					return fmt.Errorf("line %d: secret_sources is forbidden in remote documents; use client bindings", value.Line)
				}
				return nil
			})
		}
	case *inventory.ServiceDefinition:
		schemaErr = serviceDefinitionSchema(&root, false)
	}
	if schemaErr != nil {
		return failure("invalid YAML schema or forbidden source declaration", schemaErr)
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(value); err != nil {
		return failure("invalid YAML fields or values", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return failure("invalid YAML document", err)
		}
		return failure("expected exactly one YAML document", fmt.Errorf("expected exactly one YAML document"))
	}
	return nil
}

func documentRoot(root *yaml.Node) *yaml.Node {
	if root.Kind == yaml.DocumentNode && len(root.Content) != 0 {
		return root.Content[0]
	}
	return root
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

func resolvePaths(s *inventory.Service, declaring string, remote bool) error {
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
			if remote {
				return c, fmt.Errorf("local template file references are forbidden in remote documents")
			}
			c.Template.File, err = localPath(declaring, c.Template.File)
			if err != nil {
				return c, err
			}
		}
		if c.Template.URL != "" {
			var err error
			c.Template.URL, err = resolveURL(declaring, c.Template.URL, remote)
			if err != nil {
				return c, err
			}
		}
		c.Template.SHA256 = strings.ToLower(c.Template.SHA256)
		return c, nil
	}
	for i, config := range s.Configs {
		name := config.Name
		if name == "" {
			name = fmt.Sprintf("[%d]", i+1)
		}
		c, err := resolve(config)
		if err != nil {
			return fmt.Errorf("config.%s template: %w", name, err)
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
				return fmt.Errorf("process.%s unit_file template: %w", name, err)
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
				return fmt.Errorf("process.%s overrides %q template: %w", name, key, err)
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
