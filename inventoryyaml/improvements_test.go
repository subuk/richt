package inventoryyaml

import (
	"github.com/subuk/richt/inventory"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDeclarationsRemainInspectionOnly(t *testing.T) {
	source := `tags:
  common:
    services: {app: {}}
services:
  app:
    order: 10
    configs:
      main:
        path: /etc/app.conf
        template: {file: missing-template}
        validate:
          args: [/does/not/exist, '{candidate}']
          timeout: 3s
    processes:
      app:
        type: systemd
        unit: app.service
        state: stopped
        enabled: false
        masked: true
hosts:
  - name: unreachable
    tags: [common]
    transport: {type: ssh, host: unreachable.invalid}
`
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, path, source)
	in, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := in.Resolve(inventory.Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Hosts[0].Services[0].Definition
	if s.Processes["app"].Masked == nil || !*s.Processes["app"].Masked || s.Configs["main"].Validate.Timeout != "3s" {
		t.Fatalf("lost declarations: %#v", s)
	}
	for _, replacement := range []string{"unexpected: true", "args: [relative, '{candidate}']"} {
		modified := strings.Replace(source, "args: [/does/not/exist, '{candidate}']", replacement, 1)
		write(t, path, modified)
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", replacement)
		}
	}
}
