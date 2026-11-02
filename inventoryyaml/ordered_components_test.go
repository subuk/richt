package inventoryyaml

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func loadServiceFixture(t *testing.T, service string, external bool) (*inventory.Inventory, error) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "inventory.yaml")
	definition := "    file: service.yaml\n"
	if external {
		write(t, filepath.Join(dir, "service.yaml"), service)
	} else {
		definition = ""
		for _, line := range strings.Split(strings.TrimSuffix(service, "\n"), "\n") {
			definition += "    " + line + "\n"
		}
	}
	write(t, file, "services:\n  app:\n    order: 10\n"+definition+"hosts:\n  - name: untouched\n    transport: {type: ssh, host: unreachable.invalid}\n")
	return Load(file)
}

func TestComponentListSchemaInlineAndServiceFiles(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, collection := range []string{"binaries", "configs", "processes"} {
			for _, tc := range []struct{ name, value, want string }{
				{"mapping", "{main: {}}", "must be a list"}, {"null", "null", "must be a list"}, {"scalar", "text", "must be a list"},
				{"null item", "[null]", "must be a component mapping"}, {"scalar item", "[text]", "must be a component mapping"}, {"list item", "[[]]", "must be a component mapping"},
				{"keyed list item", "[{main: {}}]", "field main"}, {"empty name", "[{name: ''}]", "name must be a nonempty string"}, {"null name", "[{name: null}]", "name must be a nonempty string"}, {"numeric name", "[{name: 42}]", "name must be a nonempty string"}, {"boolean name", "[{name: true}]", "name must be a nonempty string"}, {"list name", "[{name: []}]", "name must be a nonempty string"},
			} {
				t.Run(fmt.Sprintf("file-%t/%s/%s", external, collection, tc.name), func(t *testing.T) {
					_, err := loadServiceFixture(t, collection+": "+tc.value+"\n", external)
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("want %q, got %v", tc.want, err)
					}
				})
			}
		}
	}
}

func TestOrderedNamedAndUnnamedListsFromBothSources(t *testing.T) {
	service := `binaries:
  - {name: z-last, type: rpm, package: first}
  - {type: rpm, package: second}
  - {type: rpm, package: third}
  - {name: a-first, type: rpm, package: fourth}
configs:
  - {name: z-last, type: template, path: /etc/first, template: {file: missing-first}}
  - {path: /etc/second, template: {file: missing-second}}
  - {path: /etc/third, template: {file: missing-third}}
  - {name: a-first, path: /etc/fourth, template: {file: missing-fourth}}
processes:
  - {name: z-last, type: systemd, unit: first.service, restart_on: [binary.z-last, config.a-first]}
  - {type: systemd, unit: second.service}
  - {type: systemd, unit: third.service}
  - {name: a-first, type: systemd, unit: fourth.service}
`
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprintf("file-%t", external), func(t *testing.T) {
			in, err := loadServiceFixture(t, service, external)
			if err != nil {
				t.Fatal(err)
			}
			s := in.Services["app"]
			for i, want := range []string{"z-last", "", "", "a-first"} {
				if s.Binaries[i].Name != want || s.Configs[i].Name != want || s.Processes[i].Name != want {
					t.Fatalf("order/names changed: %#v", s)
				}
			}
		})
	}
	for _, external := range []bool{false, true} {
		for _, tc := range []struct{ from, to, want string }{
			{"name: a-first", "name: z-last", "duplicate name"},
			{"binary.z-last", "binary.second", "unknown component"},
			{"config.a-first", "config.1", "unknown component"},
			{"name: z-last", "name: bad.name", "name must contain"},
			{"unit: second.service", "unit: second.service, after: [z-last]", "field after"},
			{"type: template", "type: file", "type: expected template"},
		} {
			_, err := loadServiceFixture(t, strings.Replace(service, tc.from, tc.to, 1), external)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("file %t %s: want %q got %v", external, tc.to, tc.want, err)
			}
		}
	}
}

func TestConfigTypePresenceInlineAndServiceFiles(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, location := range []string{"config", "unit_file", "override"} {
			for _, typ := range []string{"null", "''", "true", "42", "[]", "{}", "file"} {
				t.Run(fmt.Sprintf("file-%t/%s/%s", external, location, typ), func(t *testing.T) {
					config := "{type: " + typ + ", path: /etc/systemd/system/app.service, template: {inline: text}}"
					service := "configs: [" + config + "]\n"
					if location == "unit_file" {
						service = "processes: [{type: systemd, unit: app.service, unit_file: " + config + "}]\n"
					}
					if location == "override" {
						service = "processes: [{type: systemd, unit: app.service, overrides: {main.conf: " + config + "}}]\n"
					}
					_, err := loadServiceFixture(t, service, external)
					if err == nil || !strings.Contains(err.Error(), "config type: expected template") {
						t.Fatalf("invalid supplied type accepted: %v", err)
					}
				})
			}
		}
	}
}
