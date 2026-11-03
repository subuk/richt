package inventoryyaml

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestRPMDisableExcludesYAML(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tc := range []struct {
			name, fields, want string
			invalid            bool
		}{
			{name: "omitted"},
			{name: "all", fields: "disable_excludes: all", want: "all"},
			{name: "main", fields: "disable_excludes: main", want: "main"},
			{name: "repo", fields: "disable_excludes: Vendor_Repo-9.2:arch", want: "Vendor_Repo-9.2:arch"},
			{name: "quoted-number", fields: "disable_excludes: '123'", want: "123"},
			{name: "merge", fields: "<<: {disable_excludes: epel}", want: "epel"},
			{name: "override", fields: "<<: {disable_excludes: null}, disable_excludes: all", want: "all"},
			{name: "merge-sequence", fields: "<<: [{disable_excludes: main}, {disable_excludes: null}]", want: "main"},
			{name: "null", fields: "disable_excludes: null", invalid: true},
			{name: "empty", fields: "disable_excludes: ''", invalid: true},
			{name: "boolean", fields: "disable_excludes: true", invalid: true},
			{name: "number", fields: "disable_excludes: 123", invalid: true},
			{name: "list", fields: "disable_excludes: [all]", invalid: true},
			{name: "map", fields: "disable_excludes: {repo: all}", invalid: true},
			{name: "whitespace", fields: "disable_excludes: ' all'", invalid: true},
			{name: "glob", fields: "disable_excludes: 'repo*'", invalid: true},
			{name: "multiple", fields: "disable_excludes: 'one,two'", invalid: true},
			{name: "merged-null", fields: "<<: {disable_excludes: null}", invalid: true},
			{name: "merged-number", fields: "<<: {disable_excludes: 123}", invalid: true},
		} {
			t.Run(fmt.Sprintf("external-%t/%s", external, tc.name), func(t *testing.T) {
				dir := t.TempDir()
				filename := filepath.Join(dir, "inventory.yaml")
				component := "binaries: [{type: rpm, package: app"
				if tc.fields != "" {
					component += ", " + tc.fields
				}
				component += "}]\n"
				input := "services:\n  app:\n    order: 1\n    " + component
				if external {
					input = "services:\n  app: {order: 1, file: app.yaml}\n"
					write(t, filepath.Join(dir, "app.yaml"), component)
				}
				// Unassigned definitions must receive the same validation.
				input += "hosts: [{name: local, transport: {type: local}}]\n"
				write(t, filename, input)
				in, err := Load(filename)
				if tc.invalid {
					if err == nil || !strings.Contains(err.Error(), "disable_excludes") {
						t.Fatalf("want disable_excludes error, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b := in.Services["app"].Binaries[0]
				if b.DisableExcludes != tc.want {
					t.Fatalf("got %q, want %q", b.DisableExcludes, tc.want)
				}
				data, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "disable_excludes") != (tc.want != "") {
					t.Fatalf("incorrect JSON omission: %s", data)
				}
			})
		}
	}
}

func TestRPMDisableExcludesRetainedForAbsentAssignment(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, `tags:
  common: {services: {app: {state: absent}}}
services:
  app:
    order: 1
    binaries: [{type: rpm, package: app, disable_excludes: epel}]
hosts:
  - name: local
    tags: [common]
    transport: {type: local}
`)
	in, err := Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := in.Resolve(inventory.Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	s := resolved.Hosts[0].Services[0]
	if s.State != "absent" || s.Definition.Binaries[0].DisableExcludes != "epel" {
		t.Fatalf("lost removal input: %+v", s)
	}
}
