package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIOneshotInspectionNeedsNoExecutablesOrInputs(t *testing.T) {
	dir := t.TempDir()
	filename, marker := filepath.Join(dir, "inventory.yaml"), filepath.Join(dir, "executed")
	source := fmt.Sprintf(`tags:
  example:
    services: {example: {}}
services:
  example:
    order: 10
    configs:
      - name: main
        path: /var/tmp/richt-inspection.conf
        template: {file: missing-template}
    processes:
      - name: z-first
        type: oneshot
        command: [touch, %q]
        run_on: [config.main]
      - type: oneshot
        command: [missing-executable]
        run_on: [config.main]
      - type: systemd
        unit: example.service
hosts:
  - name: remote
    tags: [example]
    transport: {type: ssh, host: nonexistent.example, sudo: true}
secret_sources:
  default: {type: yaml, file: missing-secrets}
`, marker)
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	for _, sub := range []string{"validate", "show"} {
		code, stdout, stderr := run(t, "inventory", sub, "-i", filename)
		if code != 0 {
			t.Fatalf("inspection contacted a target or opened an input: %d %s", code, stderr)
		}
		if sub == "show" {
			var result struct {
				Hosts []struct {
					Services []struct {
						Definition struct {
							Processes []map[string]any `json:"processes"`
						} `json:"definition"`
					} `json:"services"`
				} `json:"hosts"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatal(err)
			}
			processes := result.Hosts[0].Services[0].Definition.Processes
			if len(processes) != 3 || processes[0]["name"] != "z-first" || processes[1]["type"] != "oneshot" || processes[2]["state"] != "running" {
				t.Fatalf("inspection lost mixed process order: %s", stdout)
			}
			for _, process := range processes[:2] {
				if process["timeout"] != "5s" || process["state"] != nil || process["unit"] != nil {
					t.Fatalf("oneshot inherited systemd defaults: %+v", process)
				}
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inventory inspection executed a command")
	}
}
