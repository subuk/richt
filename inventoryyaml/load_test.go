package inventoryyaml

import (
	"encoding/json"
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

const baseYAML = `services:
  app:
    configs:
      main:
        path: /etc/app.conf
        template:
          file: templates/not-read.gotmpl
hosts:
  - name: local
    transport:
      type: local
    services:
      - name: app
        order: 10
`

func TestLoadResolvesDeclaringFilesWithoutReadingTemplatesOrSecrets(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "richt.yaml")
	write(t, filename, `secret_sources:
  default:
    type: yaml
    file: secrets/not-readable.yaml
  production:
    type: yaml
    file: secrets/not-existing.yaml
services:
  app:
    file: services/app.yaml
hosts:
  - name: local
    transport: {type: local}
    services:
      - {name: app, order: 10}
`)
	write(t, filepath.Join(dir, "services/app.yaml"), `configs:
  main:
    path: /etc/app/../app.conf
    owner: root
    mode: "0640"
    template:
      file: templates/app.gotmpl
processes:
  web:
    type: systemd
    unit: app.service
    unit_file:
      template: {file: templates/app.service.gotmpl}
    overrides:
      10-runtime.conf:
        template: {inline: "[Service]\nRestart=always\n"}
`)
	secret := filepath.Join(dir, "secrets/not-readable.yaml")
	write(t, secret, "credential: NEVER_PRINT_SECRET\n")
	if err := os.Chmod(secret, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0600) })
	in, err := Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	s := in.Services["app"]
	if s.SourceFile != filepath.Join(dir, "services/app.yaml") || s.Configs["main"].Template.File != filepath.Join(dir, "services/templates/app.gotmpl") {
		t.Fatalf("source-relative paths: %#v", s)
	}
	if s.Configs["main"].Path != "/etc/app.conf" {
		t.Fatal("target not normalized")
	}
	p := s.Processes["web"]
	if p.UnitFile.Path != "/etc/systemd/system/app.service" || p.UnitFile.Template.File != filepath.Join(dir, "services/templates/app.service.gotmpl") {
		t.Fatalf("unit_file: %#v", p.UnitFile)
	}
	if p.Overrides["10-runtime.conf"].Path != "/etc/systemd/system/app.service.d/10-runtime.conf" {
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
}

func TestStrictYAML(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"unknown-root", baseYAML + "typo: true\n", "field typo"},
		{"unknown-assignment", strings.Replace(baseYAML, "order: 10", "order: 10\n        configs: {}", 1), "field configs"},
		{"unknown-component", strings.Replace(baseYAML, "path: /etc/app.conf", "path: /etc/app.conf\n        typo: value", 1), "field typo"},
		{"duplicate-root", baseYAML + "services: {}\n", "already defined"},
		{"duplicate-config", strings.Replace(baseYAML, "path: /etc/app.conf", "path: /etc/app.conf\n        path: /etc/other", 1), "already defined"},
		{"duplicate-variable", baseYAML + "tags:\n  common:\n    vars:\n      app:\n        port: 8080\n        port: 9090\n", "already defined"},
		{"multiple-documents", baseYAML + "---\n{}\n", "exactly one"},
		{"non-string-variable-key", baseYAML + "tags:\n  common:\n    vars:\n      nested:\n        123: value\n", "unsupported"},
		{"file-inline", strings.Replace(baseYAML, "  app:\n", "  app:\n    file: app.yaml\n", 1), "cannot be combined"},
		{"remote-service", strings.Replace(baseYAML, "    configs:\n      main:\n        path: /etc/app.conf\n        template:\n          file: templates/not-read.gotmpl", "    file: https://example.com/service.yaml", 1), "local file"},
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
	for _, tc := range []struct{ name, service, want string }{{"unknown", "configs: {}\ntyop: true\n", "field tyop"}, {"nested-reference", "file: another.yaml\n", "field file"}, {"empty", "{}\n", "at least one"}, {"bad-unit-file", "processes:\n  web:\n    type: systemd\n    unit: app.service\n    unit_file:\n      path: /tmp/not-a-unit\n      template: {inline: hi}\n", "unit_file path"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "richt.yaml"), "services:\n  app: {file: services/app.yaml}\nhosts:\n  - name: local\n    transport: {type: local}\n    services: [{name: app, order: 1}]\n")
			write(t, filepath.Join(dir, "services/app.yaml"), tc.service)
			_, err := Load(filepath.Join(dir, "richt.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
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
