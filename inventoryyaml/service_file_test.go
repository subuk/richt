package inventoryyaml

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestReferencedServiceOrderPresenceAndTypes(t *testing.T) {
	for _, tc := range []struct{ name, order, want string }{
		{"missing", "", "order: required integer"},
		{"null", "order: null\n", "must be an integer"},
		{"string", "order: '10'\n", "must be an integer"},
		{"float", "order: 10.0\n", "must be an integer"},
		{"boolean", "order: true\n", "must be an integer"},
		{"sequence", "order: [10]\n", "must be an integer"},
		{"mapping", "order: {value: 10}\n", "must be an integer"},
		{"merged-null", "<<: {order: null}\n", "must be an integer"},
		{"merged-string", "<<: [{order: invalid}, {order: 10}]\n", "must be an integer"},
		{"duplicate", "order: 1\norder: 2\n", "already defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "inventory.yaml")
			serviceFile := filepath.Join(dir, "services/app.yaml")
			// This unused service still requires a complete valid definition.
			write(t, filename, "services: {app: {file: services/app.yaml}}\nhosts: [{name: untouched, transport: {type: ssh, host: unreachable.invalid}}]\n")
			write(t, serviceFile, tc.order+"configs: [{path: /etc/app.conf, template: {file: not-read}}]\n")
			_, err := Load(filename)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), serviceFile) {
				t.Fatalf("want %q and source filename, got %v", tc.want, err)
			}
		})
	}
}

func TestServiceFileReferenceRejectsOuterFieldsBeforeReading(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"order", "order: 20", "file cannot be combined with order"},
		{"zero", "order: 0", "file cannot be combined with order"},
		{"null", "order: null", "file cannot be combined with order"},
		{"merged-order", "<<: {order: 0}", "file cannot be combined with order"},
		{"merged-null", "<<: {order: null}", "file cannot be combined with order"},
		{"merged-file-and-order", "<<: {file: missing.yaml, order: 1}", "file cannot be combined with order"},
		{"merged-sequence", "<<: [{order: null}, {order: 1}]", "file cannot be combined with order"},
		{"merged-overridden", "<<: {order: 1}, order: 0", "file cannot be combined with order"},
		{"binaries-empty", "binaries: []", "file cannot be combined with inline components"},
		{"configs-null", "configs: null", "file cannot be combined with inline components"},
		{"processes-merged", "<<: {processes: []}", "file cannot be combined with inline components"},
		{"unknown", "typo: true", "field typo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "inventory.yaml")
			// The file deliberately does not exist: shape validation must happen
			// before any referenced file is opened.
			write(t, filename, "services: {app: {file: missing.yaml, "+tc.fields+"}}\nhosts: [{name: local, transport: {type: local}}]\n")
			if _, err := Load(filename); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, value := range []string{"null", "''", "' '", "10", "true", "[]", "{}"} {
		t.Run("file-"+value, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "inventory.yaml")
			write(t, filename, "services: {app: {file: "+value+"}}\nhosts: [{name: local, transport: {type: local}}]\n")
			if _, err := Load(filename); err == nil || !strings.Contains(err.Error(), "service file must be a nonempty string") {
				t.Fatalf("invalid file path accepted: %v", err)
			}
		})
	}
}

func TestReferencedServiceOrderControlsResolutionAndHostCollisions(t *testing.T) {
	for _, ordering := range []string{
		"order: 0\n",
		"<<: {order: 0}\n",
		"<<: {order: invalid}\norder: 0\n",
		"<<: [{order: 0}, {order: invalid}]\n",
	} {
		t.Run(fmt.Sprintf("order-%s", strings.TrimSpace(ordering)), func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "inventory.yaml")
			write(t, filename, `tags:
  common: {services: {app: {}, repository: {}}}
services:
  app: {file: services/app.yaml}
  repository: {file: services/repository.yaml}
hosts:
  - {name: untouched, tags: [common], transport: {type: ssh, host: unreachable.invalid}}
`)
			write(t, filepath.Join(dir, "services/app.yaml"), ordering+"configs: [{path: /etc/app.conf, template: {file: missing}}]\n")
			repository := filepath.Join(dir, "services/repository.yaml")
			write(t, repository, "order: -10\nconfigs: [{path: /etc/repository.conf, template: {file: missing}}]\n")
			in, err := Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			out, err := in.Resolve(inventory.Selectors{})
			if err != nil {
				t.Fatal(err)
			}
			services := out.Hosts[0].Services
			if len(services) != 2 || services[0].Name != "repository" || services[0].Order != -10 || services[1].Name != "app" || services[1].Order != 0 {
				t.Fatalf("file-defined service order lost: %#v", services)
			}
			write(t, repository, "order: 0\nconfigs: [{path: /etc/repository.conf, template: {file: missing}}]\n")
			in, err = Load(filename)
			if err != nil {
				t.Fatal(err)
			}
			// Service filtering must not hide a collision on the selected host.
			_, err = in.Resolve(inventory.Selectors{Services: []string{"app"}})
			if err == nil || !strings.Contains(err.Error(), "order 0") || !strings.Contains(err.Error(), `"app"`) || !strings.Contains(err.Error(), `"repository"`) {
				t.Fatalf("expected host order collision, got %v", err)
			}
		})
	}
}
