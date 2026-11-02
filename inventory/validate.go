package inventory

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_@:.\\-]+\.service$`)

func validName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s %q: name must contain letters, numbers, underscores or hyphens and start with a letter or number", kind, name)
	}
	return nil
}

// Validate checks definitions and references without reading host state or
// resolving tags. Effective assignments, ownership and equal-order conflicts
// are checked only for selected hosts during resolution.
func (in *Inventory) Validate() error {
	if in == nil {
		return fmt.Errorf("inventory is nil")
	}
	if len(in.Hosts) == 0 {
		return fmt.Errorf("hosts: at least one host is required")
	}
	for _, name := range sortedKeys(in.Tags) {
		if err := validName("tag", name); err != nil {
			return err
		}
		// Values must also be representable in the public JSON view and plan.
		if _, err := json.Marshal(in.Tags[name].Vars); err != nil {
			return fmt.Errorf("tag %q vars: unsupported value: %w", name, err)
		}
		for _, service := range sortedKeys(in.Tags[name].Services) {
			if _, ok := in.Services[service]; !ok {
				return fmt.Errorf("tag %q services: unknown service %q", name, service)
			}
			state := in.Tags[name].Services[service].State
			if state != "" && state != "present" && state != "absent" {
				return fmt.Errorf("tag %q service %q state: expected present or absent", name, service)
			}
		}
	}
	for _, name := range sortedKeys(in.SecretSources) {
		if err := validName("secret source", name); err != nil {
			return err
		}
		s := in.SecretSources[name]
		if s.Type != "yaml" {
			return fmt.Errorf("secret source %q type: expected yaml", name)
		}
		if strings.TrimSpace(s.File) == "" {
			return fmt.Errorf("secret source %q file: required", name)
		}
	}
	for _, name := range sortedKeys(in.Services) {
		if err := validName("service", name); err != nil {
			return err
		}
		if in.Services[name].Order == nil {
			return fmt.Errorf("service %q order: required integer; set order on the top-level service definition", name)
		}
		if err := validateService(in.Services[name].Service); err != nil {
			return fmt.Errorf("service %q: %w", name, err)
		}
	}
	hosts := map[string]bool{}
	for _, h := range in.Hosts {
		if err := validName("host", h.Name); err != nil {
			return err
		}
		if hosts[h.Name] {
			return fmt.Errorf("host %q: duplicate name", h.Name)
		}
		hosts[h.Name] = true
		if err := validateTransport(h.Transport); err != nil {
			return fmt.Errorf("host %q transport: %w", h.Name, err)
		}
		tags := map[string]bool{}
		for _, name := range h.Tags {
			if _, ok := in.Tags[name]; !ok {
				return fmt.Errorf("host %q tags: unknown tag %q", h.Name, name)
			}
			if tags[name] {
				return fmt.Errorf("host %q tags: duplicate tag %q", h.Name, name)
			}
			tags[name] = true
		}
	}
	return nil
}

func validateTransport(t Transport) error {
	if t.Type != "local" && t.Type != "ssh" {
		return fmt.Errorf("type: expected local or ssh")
	}
	if t.Port < 0 || t.Port > 65535 {
		return fmt.Errorf("port: expected 1..65535 or omitted")
	}
	if t.Type == "ssh" {
		invalid := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }
		if t.Host == "" || strings.HasPrefix(t.Host, "-") || strings.ContainsAny(t.Host, "/@") || strings.IndexFunc(t.Host, invalid) >= 0 {
			return fmt.Errorf("host: expected a hostname, address, or SSH alias; specify user separately")
		}
		if strings.HasPrefix(t.User, "-") || strings.IndexFunc(t.User, invalid) >= 0 {
			return fmt.Errorf("user: invalid SSH user")
		}
	}
	if t.Type == "local" && (t.Host != "" || t.User != "" || t.Port != 0) {
		return fmt.Errorf("local transport cannot set host, user or port")
	}
	return nil
}

func validateService(s Service) error {
	if len(s.Binaries)+len(s.Configs)+len(s.Processes) == 0 {
		return fmt.Errorf("service must contain at least one binary, config or process")
	}
	for _, name := range sortedKeys(s.Binaries) {
		if err := validName("binary", name); err != nil {
			return err
		}
		b := s.Binaries[name]
		if b.Type != "rpm" {
			return fmt.Errorf("binary.%s type: expected rpm", name)
		}
		if b.Manager != "" && b.Manager != "dnf" && b.Manager != "yum" {
			return fmt.Errorf("binary.%s manager: expected dnf or yum", name)
		}
		if strings.TrimSpace(b.Package) == "" || strings.ContainsAny(b.Package, " \t\r\n/") {
			return fmt.Errorf("binary.%s package: expected a package name", name)
		}
		if err := ValidateRPMVersion(b.Version); err != nil {
			return fmt.Errorf("binary.%s version: %w", name, err)
		}
	}
	for _, name := range sortedKeys(s.Configs) {
		if err := validName("config", name); err != nil {
			return err
		}
		if err := validateConfig(s.Configs[name]); err != nil {
			return fmt.Errorf("config.%s: %w", name, err)
		}
	}
	for _, name := range sortedKeys(s.Processes) {
		if err := validName("process", name); err != nil {
			return err
		}
		p := s.Processes[name]
		if p.Type != "systemd" {
			return fmt.Errorf("process.%s type: expected systemd", name)
		}
		if !unitPattern.MatchString(p.Unit) {
			return fmt.Errorf("process.%s unit: expected a systemd .service unit name", name)
		}
		if p.State != "" && p.State != "running" && p.State != "stopped" {
			return fmt.Errorf("process.%s state: expected running or stopped", name)
		}
		if p.Masked != nil && *p.Masked {
			if p.State != "stopped" || p.Enabled != nil && *p.Enabled {
				return fmt.Errorf("process.%s masked: true requires state stopped and cannot enable the unit", name)
			}
			if p.UnitFile != nil {
				return fmt.Errorf("process.%s masked: true conflicts with an owned full unit file", name)
			}
			for _, c := range s.Configs {
				if path.Clean(c.Path) == "/etc/systemd/system/"+p.Unit {
					return fmt.Errorf("process.%s masked: true conflicts with an owned full unit file", name)
				}
			}
		}
		if p.Timeout != "" {
			d, err := time.ParseDuration(p.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("process.%s timeout: expected a positive duration such as 5s", name)
			}
		}
		for _, field := range []struct {
			name string
			refs []string
		}{{"restart_on", p.RestartOn}, {"reload_on", p.ReloadOn}} {
			seen := map[string]bool{}
			for _, ref := range field.refs {
				kind, key, ok := strings.Cut(ref, ".")
				exists := false
				if ok {
					switch kind {
					case "binary":
						_, exists = s.Binaries[key]
					case "config":
						_, exists = s.Configs[key]
					}
				}
				if !exists {
					return fmt.Errorf("process.%s %s: unknown component %q; expected binary.<name> or config.<name> in this service", name, field.name, ref)
				}
				if seen[ref] {
					return fmt.Errorf("process.%s %s: duplicate component %q", name, field.name, ref)
				}
				seen[ref] = true
			}
		}
		seen := map[string]bool{}
		for _, ref := range p.After {
			if _, ok := s.Processes[ref]; !ok {
				return fmt.Errorf("process.%s after: unknown process %q in this service", name, ref)
			}
			if seen[ref] {
				return fmt.Errorf("process.%s after: duplicate process %q", name, ref)
			}
			seen[ref] = true
		}
		if p.UnitFile != nil {
			if err := validateConfig(*p.UnitFile); err != nil {
				return fmt.Errorf("process.%s unit_file: %w", name, err)
			}
			if path.Clean(p.UnitFile.Path) != "/etc/systemd/system/"+p.Unit {
				return fmt.Errorf("process.%s unit_file path: expected /etc/systemd/system/%s", name, p.Unit)
			}
		}
		for _, key := range sortedKeys(p.Overrides) {
			if path.Base(key) != key || !strings.HasSuffix(key, ".conf") || key == ".conf" {
				return fmt.Errorf("process.%s overrides %q: expected a .conf filename", name, key)
			}
			c := p.Overrides[key]
			if err := validateConfig(c); err != nil {
				return fmt.Errorf("process.%s overrides %q: %w", name, key, err)
			}
			if path.Clean(c.Path) != "/etc/systemd/system/"+p.Unit+".d/"+key {
				return fmt.Errorf("process.%s overrides %q path: expected /etc/systemd/system/%s.d/%s", name, key, p.Unit, key)
			}
		}
	}
	states := map[string]int{}
	var stack []string
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 1 {
			return fmt.Errorf("process.%s after: cycle %s -> %s", name, strings.Join(stack, " -> "), name)
		}
		if states[name] == 2 {
			return nil
		}
		states[name] = 1
		stack = append(stack, name)
		refs := append([]string(nil), s.Processes[name].After...)
		for _, ref := range refs {
			if err := visit(ref); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		states[name] = 2
		return nil
	}
	for _, name := range sortedKeys(s.Processes) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validateConfig(c Config) error {
	if !path.IsAbs(c.Path) || strings.ContainsAny(c.Path, "\x00\r\n") || path.Clean(c.Path) == "/" {
		return fmt.Errorf("path: expected an absolute file path")
	}
	if (c.Template.File != "") == (c.Template.Inline != nil) {
		return fmt.Errorf("template: specify exactly one of file or inline")
	}
	if c.Mode != "" {
		n, err := strconv.ParseUint(c.Mode, 8, 32)
		if err != nil || n > 07777 {
			return fmt.Errorf("mode: expected an octal mode such as \"0644\"")
		}
	}
	if v := c.Validate; v != nil {
		if len(v.Args) == 0 || !path.IsAbs(v.Args[0]) || v.Args[0] == "/" {
			return fmt.Errorf("validate.args: expected an absolute executable path as the first argument")
		}
		candidates := 0
		for i, arg := range v.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("validate.args: arguments cannot contain NUL")
			}
			if strings.Contains(arg, "{candidate}") {
				if i == 0 || arg != "{candidate}" {
					return fmt.Errorf("validate.args: {candidate} must be a separate argument after the executable")
				}
				candidates++
			}
		}
		if candidates != 1 {
			return fmt.Errorf("validate.args: exactly one {candidate} argument is required")
		}
		if v.Timeout != "" {
			d, err := time.ParseDuration(v.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("validate.timeout: expected a positive duration such as 5s")
			}
		}
	}
	return nil
}
