package cli

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func TestCLIEnvironmentSecretsFrozenContinuation(t *testing.T) {
	for _, location := range []string{"inventory", "client-local", "client-https"} {
		t.Run(location, func(t *testing.T) {
			dir := t.TempDir()
			const variable = "RICHT_ENV_CLI_TEST_SECRET"
			const value = "fictional-env-first-line\nfictional-env-second-line\n"
			const replacement = "fictional-env-replaced"
			t.Setenv(variable, "")
			if err := os.Unsetenv(variable); err != nil {
				t.Fatal(err)
			}
			target, blocked := filepath.Join(dir, "first"), filepath.Join(dir, "blocked")
			later := filepath.Join(blocked, "second")
			config := func(path string) map[string]any {
				return map[string]any{"path": path, "mode": "0600", "sensitive": false, "template": map[string]any{"inline": `{{secret "database.password"}}`}}
			}
			declarations := map[string]any{"default": map[string]any{"type": "env", "env": map[string]string{"database.password": variable}}}
			inventory := map[string]any{
				"tags":     map[string]any{"app": map[string]any{"services": map[string]any{"app": map[string]any{}}}},
				"services": map[string]any{"app": map[string]any{"order": 10, "configs": []any{config(target), config(later)}}},
				"hosts":    []any{map[string]any{"name": "local", "tags": []string{"app"}, "transport": map[string]string{"type": "local"}}},
			}
			if location == "inventory" {
				inventory["secret_sources"] = declarations
			}
			body, err := json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(dir, "inventory.yaml")
			writeSourceFixture(t, input, string(body))
			var flags []string
			if location != "inventory" {
				settings := map[string]any{"secret_sources": declarations}
				if location == "client-https" {
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
					defer server.Close()
					ca := filepath.Join(dir, "ca.pem")
					writeSourceFixture(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
					settings["ca_file"] = ca
					input = server.URL + "/inventory.yaml"
					flags = append(flags, "--inventory-sha256", fmt.Sprintf("%x", sha256.Sum256(body)))
				}
				clientFile := filepath.Join(dir, "client.yaml")
				clientBody, err := json.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				writeSourceFixture(t, clientFile, string(clientBody))
				flags = append(flags, "--source-config", clientFile)
			}
			check := func(wantSuccess bool, args ...string) string {
				t.Helper()
				code, out, stderr := run(t, args...)
				for _, marker := range []string{"fictional-env-first-line", "fictional-env-second-line", replacement} {
					if strings.Contains(out+stderr, marker) {
						t.Fatal("secret escaped CLI output")
					}
				}
				if (code == 0) != wantSuccess {
					t.Fatalf("unexpected CLI exit %d", code)
				}
				return out + stderr
			}
			for _, cmd := range []string{"validate", "show"} {
				check(true, append([]string{"inventory", cmd, "-i", input}, flags...)...)
			}
			// Missing secrets fail preparation before a plan can be published.
			saved := filepath.Join(dir, "plan.json")
			output := check(false, append([]string{"diff", "--progress=plain", "-i", input, "-o", saved}, flags...)...)
			if strings.Contains(output, variable) || strings.Contains(output, "database.password") {
				t.Fatal("lookup arguments escaped diagnostics")
			}
			if _, err := os.Stat(saved); !os.IsNotExist(err) {
				t.Fatal("failed preparation published a plan")
			}
			t.Setenv(variable, value)
			output = check(true, append([]string{"diff", "--progress=plain", "-i", input, "-o", saved}, flags...)...)
			if !strings.Contains(output, "Content hidden: sensitive") {
				t.Fatal("missing sensitive display")
			}
			raw, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			var prepared plan.Plan
			if err := json.Unmarshal(raw, &prepared); err != nil {
				t.Fatal(err)
			}
			if prepared.Version != "1.8" || strings.Contains(string(raw), variable) || strings.Contains(string(raw), "secret_sources") {
				t.Fatal("plan changed format or persisted source bindings")
			}
			for _, task := range prepared.Hosts[0].Tasks {
				if task.File == nil || !task.File.Sensitive || task.File.Content != value {
					t.Fatal("plan lost prepared secret bytes or sensitivity")
				}
			}
			for _, name := range []string{"inventory.yaml", "client.yaml", "ca.pem"} {
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			t.Setenv(variable, replacement)
			writeSourceFixture(t, blocked, "block second task")
			check(false, "apply", "--progress=plain", "-y", saved)
			content, err := os.ReadFile(target)
			if err != nil || string(content) != value {
				t.Fatal("apply did not use frozen secret")
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("private target mode lost")
			}
			writeSourceFixture(t, target, "completed task remains skipped")
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			if err := os.Unsetenv(variable); err != nil {
				t.Fatal(err)
			}
			check(true, "apply", "--progress=plain", "-y", saved)
			content, err = os.ReadFile(later)
			if err != nil || string(content) != value {
				t.Fatal("continuation did not use frozen secret")
			}
			content, err = os.ReadFile(target)
			if err != nil || string(content) != "completed task remains skipped" {
				t.Fatal("continuation reran successful task")
			}
		})
	}
}
