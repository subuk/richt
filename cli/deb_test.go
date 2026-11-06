package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestCLIDEBInventoryInspectionNeedsNoTargetCapabilities(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	text := `tags:
  tools:
    services: {tools: {}}
services:
  tools:
    order: 10
    binaries:
      - {name: package, type: deb, package: tree, version: "2.1.1-2"}
hosts:
  - {name: debian, tags: [tools], transport: {type: ssh, host: unreachable.invalid, sudo: true}}
`
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	// Neither SSH, APT, dpkg nor Python is available through this PATH.
	t.Setenv("PATH", t.TempDir())
	code, stdout, stderr := run(t, "inventory", "validate", "-i", filename)
	if code != 0 {
		t.Fatalf("validate required target capabilities: %s %s", stdout, stderr)
	}
	code, stdout, stderr = run(t, "inventory", "show", "-i", filename)
	if code != 0 {
		t.Fatalf("show required target capabilities: %s %s", stdout, stderr)
	}
	var resolved inventory.Resolved
	if err := json.Unmarshal([]byte(stdout), &resolved); err != nil {
		t.Fatal(err)
	}
	binary := resolved.Hosts[0].Services[0].Definition.Binaries[0]
	if binary.Type != "deb" || binary.Package != "tree" || binary.Version != "2.1.1-2" {
		t.Fatal(binary)
	}
	code, stdout, stderr = run(t, "--help")
	if code != 0 || !strings.Contains(stdout, "DEB") {
		t.Fatalf("help omitted DEB support: %s %s", stdout, stderr)
	}
}
