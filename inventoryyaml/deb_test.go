package inventoryyaml

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestDEBYAML(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tc := range []struct {
			name, fields string
			invalid      bool
		}{
			{name: "default"},
			{name: "exact", fields: "manager: apt, version: '2:1.0~rc1-1ubuntu1'"},
			{name: "merge", fields: "<<: {manager: apt, version: '1.0'}"},
			{name: "manager", fields: "manager: dnf", invalid: true},
			{name: "null-manager", fields: "manager: null", invalid: true},
			{name: "empty-version", fields: "version: ''", invalid: true},
			{name: "null-version", fields: "version: null", invalid: true},
			{name: "number-version", fields: "version: 1", invalid: true},
			{name: "invalid-version", fields: "version: '1.0*'", invalid: true},
			{name: "deb-rpm-excludes", fields: "disable_excludes: all", invalid: true},
			{name: "empty-rpm-excludes", fields: "disable_excludes: ''", invalid: true},
			{name: "null-artifact", fields: "url: null", invalid: true},
			{name: "empty-artifact", fields: "path: ''", invalid: true},
			{name: "artifact-timeout", fields: "timeout: 5m", invalid: true},
			{name: "artifact-strip", fields: "strip_components: 0", invalid: true},
			{name: "merged-artifact", fields: "<<: {sha256: ''}", invalid: true},
		} {
			t.Run(fmt.Sprintf("external-%t/%s", external, tc.name), func(t *testing.T) {
				dir := t.TempDir()
				filename := filepath.Join(dir, "inventory.yaml")
				component := "binaries: [{type: deb, package: app"
				if tc.fields != "" {
					component += ", " + tc.fields
				}
				component += "}]\n"
				input := "services:\n  app:\n    order: 1\n    " + component
				if external {
					input = "services:\n  app: {file: app.yaml}\n"
					write(t, filepath.Join(dir, "app.yaml"), "order: 1\n"+component)
				}
				input += "hosts: [{name: local, transport: {type: local}}]\n"
				write(t, filename, input)
				in, err := Load(filename)
				if tc.invalid {
					if err == nil {
						t.Fatal("accepted invalid DEB definition outside assigned scope")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := in.Resolve(inventory.Selectors{}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDEBYAMLRejectsPackageSelectors(t *testing.T) {
	for _, value := range []string{"app:amd64", "app*", "App", "app=1.0", "app/stable", "a"} {
		filename := filepath.Join(t.TempDir(), "inventory.yaml")
		write(t, filename, fmt.Sprintf("services:\n  app:\n    order: 1\n    binaries: [{type: deb, package: %q}]\nhosts: [{name: local, transport: {type: local}}]\n", value))
		if _, err := Load(filename); err == nil {
			t.Errorf("accepted package selector %q", value)
		}
	}
}
