package inventoryyaml

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestArtifactYAMLInlineAndServiceFiles(t *testing.T) {
	checksum := strings.Repeat("AB", 32)
	for _, external := range []bool{false, true} {
		for _, typ := range []string{"download", "archive"} {
			t.Run(fmt.Sprintf("external-%t/%s", external, typ), func(t *testing.T) {
				dir := t.TempDir()
				fields := "name: tool, type: " + typ + ", url: https://example.invalid/tool, sha256: '" + checksum + "', path: /opt/./tool, owner: root, group: '0', timeout: 2m"
				if typ == "archive" {
					fields += ", format: tar.gz, strip_components: 1"
				} else {
					fields += ", mode: '0750'"
				}
				filename := filepath.Join(dir, "inventory.yaml")
				component := "binaries: [{" + fields + "}]\n"
				input := "services:\n  app:\n    order: 1\n    " + component
				if external {
					input = "services:\n  app: {file: app.yaml}\n"
					write(t, filepath.Join(dir, "app.yaml"), "order: 1\n"+component)
				}
				// The source does not exist. Inspection validates syntax only.
				input += "hosts: [{name: local, transport: {type: local}}]\n"
				write(t, filename, input)
				in, err := Load(filename)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := in.Resolve(inventory.Selectors{}); err != nil {
					t.Fatal(err)
				}
				b := in.Services["app"].Binaries[0]
				if b.Path != "/opt/tool" || b.SHA256 != strings.ToLower(checksum) || b.Timeout != "2m" || b.Owner != "root" || b.Group != "0" {
					t.Fatalf("lost or unnormalized artifact inputs: %#v", b)
				}
				// Resolution must not inject RPM defaults into artifact inputs.
				in.Tags = map[string]inventory.Tag{"all": {Services: map[string]inventory.ServiceAssignment{"app": {}}}}
				in.Hosts[0].Tags = []string{"all"}
				resolved, err := in.Resolve(inventory.Selectors{})
				if err != nil {
					t.Fatal(err)
				}
				b = resolved.Hosts[0].Services[0].Definition.Binaries[0]
				if err := inventory.ValidateArtifactBinary(b); err != nil {
					t.Fatalf("resolved artifact is invalid: %v", err)
				}
			})
		}
	}
}

func TestArtifactStrictYAMLSchema(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	for _, external := range []bool{false, true} {
		for _, tc := range []struct {
			name, typ, fields, omit string
		}{
			{"download-package", "download", "package: ''", ""},
			{"download-manager", "download", "manager: null", ""},
			{"download-version", "download", "version: null", ""},
			{"download-excludes", "download", "disable_excludes: all", ""},
			{"download-format", "download", "format: null", ""},
			{"download-strip", "download", "strip_components: 0", ""},
			{"archive-mode", "archive", "mode: ''", ""},
			{"archive-package", "archive", "package: null", ""},
			{"archive-no-format", "archive", "", "format"},
			{"archive-format-null", "archive", "format: null", "format"},
			{"archive-strip-string", "archive", "strip_components: '1'", ""},
			{"archive-strip-float", "archive", "strip_components: 1.5", ""},
			{"archive-strip-null", "archive", "strip_components: null", ""},
			{"archive-strip-negative", "archive", "strip_components: -1", ""},
			{"archive-strip-large", "archive", "strip_components: 33", ""},
			{"rpm-url", "rpm", "url: ''", ""},
			{"rpm-path", "rpm", "path: null", ""},
			{"rpm-sha", "rpm", "sha256: null", ""},
			{"no-checksum", "download", "", "sha256"},
			{"checksum-number", "download", "sha256: 123", "sha256"},
			{"checksum-null", "download", "sha256: null", "sha256"},
			{"url-null", "download", "url: null", "url"},
			{"url-number", "download", "url: 42", "url"},
			{"mode-number", "download", "mode: 0755", ""},
			{"owner-number", "download", "owner: 0", ""},
			{"group-list", "download", "group: [root]", ""},
			{"timeout-number", "download", "timeout: 5", ""},
			{"timeout-empty", "download", "timeout: ''", ""},
			{"path-null", "download", "path: null", "path"},
			{"path-control-removed-by-clean", "download", `path: "/opt/\t/../tool"`, "path"},
			{"merged-forbidden", "archive", "<<: {mode: null}", ""},
		} {
			t.Run(fmt.Sprintf("external-%t/%s", external, tc.name), func(t *testing.T) {
				dir := t.TempDir()
				filename := filepath.Join(dir, "inventory.yaml")
				fields := "type: " + tc.typ
				if tc.typ == "rpm" {
					fields += ", package: app"
				} else {
					for _, f := range []struct{ key, value string }{{"url", "https://example.invalid/tool"}, {"sha256", "'" + checksum + "'"}, {"path", "/opt/tool"}} {
						if f.key != tc.omit {
							fields += ", " + f.key + ": " + f.value
						}
					}
					if tc.typ == "archive" && tc.omit != "format" {
						fields += ", format: tar.gz"
					}
				}
				if tc.fields != "" {
					fields += ", " + tc.fields
				}
				component := "binaries: [{" + fields + "}]\n"
				input := "services:\n  app:\n    order: 1\n    " + component
				if external {
					input = "services:\n  app: {file: app.yaml}\n"
					write(t, filepath.Join(dir, "app.yaml"), "order: 1\n"+component)
				}
				// Invalid unused definitions are still errors.
				input += "hosts: [{name: local, transport: {type: local}}]\n"
				write(t, filename, input)
				if _, err := Load(filename); err == nil {
					t.Fatal("invalid artifact accepted")
				}
			})
		}
	}
}

func TestAbsentArtifactsStillRequireIntegrityInputs(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, `tags: {all: {services: {app: {state: absent}}}}
services:
  app:
    order: 1
    binaries: [{type: download, url: https://example.invalid/tool, path: /opt/tool}]
hosts: [{name: local, tags: [all], transport: {type: local}}]
`)
	if _, err := Load(filename); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("missing integrity inputs: %v", err)
	}
}
