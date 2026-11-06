package inventoryyaml

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func write(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

const baseYAML = `tags:
  common:
    services:
      app: {}
services:
  app:
    order: 10
    configs:
      - name: main
        path: /etc/app.conf
        template:
          file: templates/not-read.gotmpl
hosts:
  - name: local
    tags: [common]
    transport:
      type: local
`

func TestLoadResolvesDeclaringFilesWithoutReadingTemplatesOrSecrets(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "richt.yaml")
			write(t, filename, `secret_sources:
  default:
    type: yaml
    file: secrets/not-readable.yaml
  production:
    type: yaml
    file: secrets/not-existing.yaml
tags:
  common:
    services: {app: {}}
services:
  app:
    file: services/app.yaml
hosts:
  - name: local
    tags: [common]
    transport: {type: local}
`)
			write(t, filepath.Join(dir, "services/app.yaml"), strings.ReplaceAll(`order: 10
configs:
  - name: main
    path: /etc/app/../app.conf
    owner: root
    mode: "0640"
    template:
      file: templates/app.gotmpl
processes:
  - name: web
    type: systemd
    unit: app.service
    unit_file:
      template: {file: templates/app.service.gotmpl}
    overrides:
      10-runtime.conf:
        template: {inline: "[Service]\nRestart=always\n"}
`, "app.service", "app."+suffix))
			secret := filepath.Join(dir, "secrets/not-readable.yaml")
			// Invalid source contents must not affect inventory-only inspection.
			write(t, secret, "credential: [NEVER_PRINT_SECRET\n")
			if err := os.Chmod(secret, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(secret, 0600) })
			in, err := Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			s := in.Services["app"]
			if s.SourceFile != filepath.Join(dir, "services/app.yaml") || s.Configs[0].Template.File != filepath.Join(dir, "services/templates/app.gotmpl") {
				t.Fatalf("source-relative paths: %#v", s)
			}
			if s.Configs[0].Path != "/etc/app.conf" {
				t.Fatal("target not normalized")
			}
			p := s.Processes[0]
			if p.UnitFile.Path != "/etc/systemd/system/app."+suffix || p.UnitFile.Template.File != filepath.Join(dir, "services/templates/app."+suffix+".gotmpl") {
				t.Fatalf("unit_file: %#v", p.UnitFile)
			}
			if p.Overrides["10-runtime.conf"].Path != "/etc/systemd/system/app."+suffix+".d/10-runtime.conf" {
				t.Fatalf("override: %#v", p.Overrides)
			}
			out, err := in.Resolve(inventory.Selectors{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "NEVER_PRINT_SECRET") {
				t.Fatal("secret source values leaked")
			}
			if in.SecretSources["production"].File != filepath.Join(dir, "secrets/not-existing.yaml") {
				t.Fatal("secret path not source-relative")
			}
		})
	}
}

func TestStrictYAML(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"unknown-root", baseYAML + "typo: true\n", "field typo"},
		{"unknown-assignment", strings.Replace(baseYAML, "app: {}", "app: {configs: {}}", 1), "field configs"},
		{"unknown-component", strings.Replace(baseYAML, "path: /etc/app.conf", "path: /etc/app.conf\n        typo: value", 1), "field typo"},
		{"duplicate-root", baseYAML + "services: {}\n", "already defined"},
		{"duplicate-config", strings.Replace(baseYAML, "path: /etc/app.conf", "path: /etc/app.conf\n        path: /etc/other", 1), "already defined"},
		{"duplicate-variable", strings.Replace(baseYAML, "    services:\n", "    vars:\n      app:\n        port: 8080\n        port: 9090\n    services:\n", 1), "already defined"},
		{"multiple-documents", baseYAML + "---\n{}\n", "exactly one"},
		{"non-string-variable-key", strings.Replace(baseYAML, "    services:\n", "    vars:\n      nested:\n        123: value\n    services:\n", 1), "unsupported"},
		{"file-inline", strings.Replace(baseYAML, "    order: 10\n", "    file: app.yaml\n", 1), "cannot be combined"},
		{"remote-service", strings.Replace(strings.Replace(baseYAML, "    order: 10\n", "", 1), "    configs:\n      - name: main\n        path: /etc/app.conf\n        template:\n          file: templates/not-read.gotmpl", "    file: https://example.com/service.yaml", 1), "local file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "richt.yaml")
			write(t, filename, tc.text)
			_, err := Load(filename)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestReferencedServiceIsStrictAndLocal(t *testing.T) {
	for _, tc := range []struct{ name, service, want string }{
		{"unknown", "configs: []\ntyop: true\n", "field tyop"},
		{"nested-reference", "file: another.yaml\n", "field file"},
		{"merged-reference", "<<: {file: another.yaml}\n", "field file"},
		{"wrapper", "services: {app: {order: 1, configs: []}}\n", "field services"},
		{"nested-definition", "service: {configs: []}\n", "field service"},
		{"empty", "", "at least one"},
		{"bad-unit-file", "processes:\n  - name: web\n    type: systemd\n    unit: app.service\n    unit_file:\n      path: /tmp/not-a-unit\n      template: {inline: hi}\n", "unit_file path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "richt.yaml"), "services:\n  app: {file: services/app.yaml}\nhosts:\n  - name: local\n    transport: {type: local}\n")
			write(t, filepath.Join(dir, "services/app.yaml"), "order: 1\n"+tc.service)
			_, err := Load(filepath.Join(dir, "richt.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestServiceOrderPresenceAndTypes(t *testing.T) {
	for _, tc := range []struct{ name, replacement, want string }{
		{"missing", "", "order: required integer"},
		{"null", "    order: null\n", "must be an integer"},
		{"string", "    order: '10'\n", "must be an integer"},
		{"float", "    order: 10.0\n", "must be an integer"},
		{"boolean", "    order: true\n", "must be an integer"},
		{"sequence", "    order: [10]\n", "must be an integer"},
		{"mapping", "    order: {value: 10}\n", "must be an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "richt.yaml")
			write(t, filename, strings.Replace(baseYAML, "    order: 10\n", tc.replacement, 1))
			_, err := Load(filename)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, file := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-zero-file-%t", file), func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "richt.yaml")
			input := strings.Replace(baseYAML, "    order: 10", "    order: 0", 1)
			if file {
				input = strings.Replace(strings.Replace(input, "    order: 0\n", "", 1), "    configs:\n      - name: main\n        path: /etc/app.conf\n        template:\n          file: templates/not-read.gotmpl", "    file: services/app.yaml", 1)
				write(t, filepath.Join(dir, "services/app.yaml"), "order: 0\nconfigs:\n  - name: main\n    path: /etc/app.conf\n    template: {inline: content}\n")
			}
			write(t, filename, input)
			in, err := Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			if order := in.Services["app"].Order; order == nil || *order != 0 {
				t.Fatalf("explicit zero not retained: %v", order)
			}
			out, err := in.Resolve(inventory.Selectors{})
			if err != nil || len(out.Hosts[0].Services) != 1 || out.Hosts[0].Services[0].Order != 0 {
				t.Fatalf("resolved zero order: %#v, %v", out, err)
			}
		})
	}
}

func TestTagAssignmentSchema(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"host-services", "    tags: [common]", "    tags: [common]\n    services: [{name: app, order: 10}]", "field services"},
		{"weight", "  common:\n", "  common:\n    weight: 10\n", "field weight"},
		{"list", "    services:\n      app: {}", "    services: [{name: app, order: 10}]", "mapping keyed by service name"},
		{"name", "app: {}", "app: {name: app}", "field name"},
		{"order", "app: {}", "app: {order: 10}", "field order"},
		{"overrides", "app: {}", "app: {overrides: {}}", "field overrides"},
		{"binaries", "app: {}", "app: {binaries: {}}", "field binaries"},
		{"configs", "app: {}", "app: {configs: {}}", "field configs"},
		{"processes", "app: {}", "app: {processes: {}}", "field processes"},
		{"null-assignment", "app: {}", "app: null", "assignment must be a mapping"},
		{"scalar-assignment", "app: {}", "app: present", "assignment must be a mapping"},
		{"list-assignment", "app: {}", "app: []", "assignment must be a mapping"},
		{"null-services", "    services:\n      app: {}", "    services: null", "mapping keyed by service name"},
		{"null-state", "app: {}", "app: {state: null}", "state must be present or absent"},
		{"empty-state", "app: {}", "app: {state: ''}", "state must be present or absent"},
		{"boolean-state", "app: {}", "app: {state: true}", "state must be present or absent"},
		{"numeric-state", "app: {}", "app: {state: 10}", "state must be present or absent"},
		{"map-state", "app: {}", "app: {state: {value: present}}", "state must be present or absent"},
		{"list-state", "app: {}", "app: {state: [present]}", "state must be present or absent"},
		{"unknown-state", "app: {}", "app: {state: paused}", "state"},
		{"null-tag-order", "  common:\n", "  common:\n    order: null\n", "order must be an integer"},
		{"float-tag-order", "  common:\n", "  common:\n    order: 1.0\n", "order must be an integer"},
		{"string-tag-order", "  common:\n", "  common:\n    order: '1'\n", "order must be an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The invalid tag is deliberately unused: strict inventory validation
			// cannot depend on selected hosts or service filters.
			input := strings.Replace(baseYAML, "    tags: [common]\n", "", 1)
			if tc.name == "host-services" {
				input = baseYAML
			}
			filename := filepath.Join(t.TempDir(), "richt.yaml")
			write(t, filename, strings.Replace(input, tc.from, tc.to, 1))
			_, err := Load(filename)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTagAssignmentsAcceptEmptyAndExplicitState(t *testing.T) {
	for _, state := range []string{"{}", "{state: present}", "{state: absent}"} {
		t.Run(state, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "richt.yaml")
			write(t, filename, strings.Replace(baseYAML, "app: {}", "app: "+state, 1))
			in, err := Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			out, err := in.Resolve(inventory.Selectors{})
			if err != nil {
				t.Fatal(err)
			}
			want := "present"
			if state == "{state: absent}" {
				want = "absent"
			}
			if got := out.Hosts[0].Services[0].State; got != want {
				t.Fatalf("state %q, want %q", got, want)
			}
		})
	}
}

func TestOrderAndAssignmentsThroughYAMLMerges(t *testing.T) {
	input := strings.Replace(baseYAML, "    order: 10", "    <<: &ordering {order: 0}", 1)
	input = strings.Replace(input, "app: {}", "app: {<<: &assignment {state: absent}}", 1)
	filename := filepath.Join(t.TempDir(), "richt.yaml")
	write(t, filename, input)
	in, err := Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	if in.Tags["common"].Order != 0 || in.Services["app"].Order == nil || *in.Services["app"].Order != 0 {
		t.Fatal("default tag order or merged service order lost")
	}
	out, err := in.Resolve(inventory.Selectors{})
	if err != nil || out.Hosts[0].Services[0].State != "absent" {
		t.Fatalf("merged state: %#v, %v", out, err)
	}
	write(t, filename, strings.Replace(input, "{state: absent}", "{order: 10}", 1))
	if _, err := Load(filename); err == nil || !strings.Contains(err.Error(), "field order") {
		t.Fatalf("merged unsupported assignment field: %v", err)
	}
}

func TestYAMLMergeOverridesValidateEffectiveFields(t *testing.T) {
	for _, tc := range []struct{ name, order, assignment string }{
		{"explicit", "    <<: {order: invalid}\n    order: 0", "{<<: {state: null}, state: present}"},
		{"sequence", "    <<: [{order: 0}, {order: invalid}]", "{<<: [{state: present}, {state: null}]}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Replace(baseYAML, "    order: 10", tc.order, 1)
			input = strings.Replace(input, "app: {}", "app: "+tc.assignment, 1)
			filename := filepath.Join(t.TempDir(), "richt.yaml")
			write(t, filename, input)
			in, err := Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			out, err := in.Resolve(inventory.Selectors{})
			if err != nil {
				t.Fatal(err)
			}
			if got := out.Hosts[0].Services[0]; got.Order != 0 || got.State != "present" {
				t.Fatalf("effective merged assignment: %+v", got)
			}
		})
	}
}

func TestExampleInventory(t *testing.T) {
	in, err := Load("../examples/inventory/richt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := in.Resolve(inventory.Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 2 {
		t.Fatalf("hosts: %d", len(out.Hosts))
	}
	for i, want := range []string{"Home", "Net"} {
		if got := out.Hosts[i].Vars["vmango"].(map[string]any)["cloud_name"]; got != want {
			t.Fatalf("cloud_name %v, want %s", got, want)
		}
		if out.Hosts[i].Services[0].Name != "vmango-repository" || out.Hosts[i].Services[1].Name != "vmango" {
			t.Fatal("service order")
		}
	}
}
