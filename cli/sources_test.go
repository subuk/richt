package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeSourceFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCLIHTTPSFrozenPlanAndContinuation(t *testing.T) {
	for _, scenario := range []string{"standalone", "remote-catalog", "local-catalog"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			first, blocked, last := filepath.Join(dir, "first"), filepath.Join(dir, "blocked"), filepath.Join(dir, "last")
			template := `value={{ secret "password" }}; host={{ .label }}`
			var mu sync.Mutex
			files := map[string]string{}
			requests := map[string]int{}
			const token = "private-http-token-krg80"
			const secret = "private-template-secret-krg81"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests[r.URL.Path]++
				if r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, token, http.StatusUnauthorized)
					return
				}
				value, ok := files[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				fmt.Fprint(w, value)
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			writeSourceFixture(t, filepath.Join(dir, "ca.pem"), string(ca))
			writeSourceFixture(t, filepath.Join(dir, "token"), token+"\n")
			clientFile := filepath.Join(dir, "client.yaml")
			writeSourceFixture(t, clientFile, fmt.Sprintf(`ca_file: ca.pem
auth:
  - origin: %s
    path_prefix: /catalog
    bearer: {file: token}
secret_sources:
  default: {type: yaml, file: secrets.yaml}
`, server.URL))
			tpl := map[string]any{"inline": template}
			if scenario != "standalone" {
				tpl = map[string]any{"url": "templates/app.gotmpl", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(template)))}
			}
			service := map[string]any{"order": 10, "configs": []any{
				map[string]any{"path": first, "mode": "0600", "template": tpl},
				map[string]any{"path": filepath.Join(blocked, "second"), "template": map[string]any{"inline": "second"}},
				map[string]any{"path": last, "template": map[string]any{"inline": "last"}},
			}}
			serviceBytes, _ := json.Marshal(service)
			var serviceRef any = service
			if scenario != "standalone" {
				url := "app.yaml"
				if scenario == "local-catalog" {
					url = server.URL + "/catalog/app.yaml"
				}
				serviceRef = map[string]any{"url": url, "sha256": fmt.Sprintf("%x", sha256.Sum256(serviceBytes))}
			}
			inv := map[string]any{
				"tags":     map[string]any{"common": map[string]any{"services": map[string]any{"app": map[string]any{}}, "vars": map[string]any{"label": "one"}}},
				"services": map[string]any{"app": serviceRef},
				"hosts":    []any{map[string]any{"name": "local", "tags": []string{"common"}, "transport": map[string]any{"type": "local"}}},
			}
			invBytes, _ := json.Marshal(inv)
			files["/catalog/inventory.yaml"] = string(invBytes)
			files["/catalog/app.yaml"] = string(serviceBytes)
			files["/catalog/templates/app.gotmpl"] = template
			input := server.URL + "/catalog/inventory.yaml"
			flags := []string{"--source-config", clientFile}
			if scenario == "local-catalog" {
				input = filepath.Join(dir, "inventory.yaml")
				writeSourceFixture(t, input, string(invBytes))
			} else {
				flags = append(flags, "--inventory-sha256", fmt.Sprintf("%x", sha256.Sum256(invBytes)))
			}
			check := func(args ...string) string {
				t.Helper()
				code, out, stderr := run(t, args...)
				if code != 0 {
					t.Fatalf("command failed: %s", stderr)
				}
				if strings.Contains(out+stderr, token) || strings.Contains(out+stderr, secret) {
					t.Fatal("private value escaped CLI display")
				}
				return out
			}
			check(append([]string{"inventory", "show", "-i", input}, flags...)...)
			mu.Lock()
			gotTemplates := requests["/catalog/templates/app.gotmpl"]
			mu.Unlock()
			if gotTemplates != 0 {
				t.Fatal("inventory inspection fetched a template")
			}
			// The absent local secret file did not prevent inspection.
			writeSourceFixture(t, filepath.Join(dir, "secrets.yaml"), "password: "+secret+"\n")
			saved := filepath.Join(dir, "plan.json")
			check(append([]string{"diff", "-i", input, "-o", saved}, flags...)...)
			planBytes, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(planBytes, []byte(token)) || bytes.Contains(planBytes, []byte(server.URL)) || !bytes.Contains(planBytes, []byte(secret)) {
				t.Fatal("saved plan must retain rendered secrets, without source credentials or URL")
			}
			server.Close()
			for _, name := range []string{"client.yaml", "secrets.yaml", "token", "ca.pem"} {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			writeSourceFixture(t, blocked, "block parent creation")
			code, out, stderr := run(t, "apply", "-y", saved)
			if code == 0 || strings.Contains(out+stderr, secret) {
				t.Fatalf("expected private partial failure: %d", code)
			}
			content, _ := os.ReadFile(first)
			if string(content) != "value="+secret+"; host=one" {
				t.Fatal("first task did not use frozen template")
			}
			if _, err := os.Stat(last); !os.IsNotExist(err) {
				t.Fatal("failure did not block later tasks")
			}
			writeSourceFixture(t, first, "completed task stays skipped")
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			check("apply", "-y", saved)
			content, _ = os.ReadFile(first)
			if string(content) != "completed task stays skipped" {
				t.Fatal("continuation reran successful task")
			}
			content, _ = os.ReadFile(last)
			if string(content) != "last" {
				t.Fatal("continuation did not finish saved tasks")
			}
		})
	}
}

func TestClientSettingsValidationAndSavedPlanIsolation(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "client.yaml")
	t.Setenv("RICHT_SOURCE_TEST_TOKEN", "env-token-value")
	writeSourceFixture(t, filename, "auth: [{origin: 'https://example.org', bearer: {env: RICHT_SOURCE_TEST_TOKEN}}]\n")
	if _, _, err := loadSourceConfig(filename); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"auth: [{origin: 'https://example.org', bearer: {env: MISSING_RICHT_TOKEN}}]",
		"auth: [{origin: 'https://example.org', bearer: {env: RICHT_SOURCE_TEST_TOKEN, file: token}}]",
		"auth: [{origin: 'https://example.org', bearer: {inline: private-marker}}]",
		"auth: [{origin: 'https://example.org', bearer: {env: RICHT_SOURCE_TEST_TOKEN}, basic: {username: user}}]",
		"auth: [{origin: 'https://example.org', basic: {username: 'invalid:user', password: {env: RICHT_SOURCE_TEST_TOKEN}}}]",
		"auth: []\nauth: []",
		"secret_sources: {default: {type: yaml, file: https://example.org/private-marker}}",
		"ca_file: https://example.org/private-marker",
		"unknown: private-marker",
		"[]",
		"{}\n---\n{}",
	} {
		writeSourceFixture(t, filename, input)
		if _, _, err := loadSourceConfig(filename); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatalf("invalid client config must fail without values: %v", err)
		}
	}
	for _, args := range [][]string{
		{"--source-config", "does-not-exist"},
		{"--inventory-sha256", strings.Repeat("0", 64)},
	} {
		code, _, stderr := run(t, append([]string{"apply", "-y", "does-not-exist.json"}, args...)...)
		if code == 0 || !strings.Contains(stderr, "cannot be used with a saved plan") {
			t.Fatalf("source flags must be rejected before opening saved plan/settings: %s", stderr)
		}
	}
}

func TestCLIHTTPSCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, stderr bytes.Buffer
	if code := RunContext(ctx, []string{"inventory", "validate", "-i", "https://example.invalid/inventory.yaml"}, &out, &stderr); code == 0 {
		t.Fatal("canceled source load succeeded")
	}
}

func TestClientEnvironmentSecretBindings(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "client.yaml")
	writeSourceFixture(t, filename, `secret_sources:
  default: {type: env, env: {database.password: RICHT_UNSET_CLIENT_SECRET}}
  empty: {type: env, env: {}}
  local: {type: yaml, file: secrets.yaml}
`)
	_, bindings, err := loadSourceConfig(filename)
	if err != nil {
		t.Fatal(err)
	}
	if bindings["default"].Env["database.password"] != "RICHT_UNSET_CLIENT_SECRET" || bindings["empty"].Env == nil {
		t.Fatal("environment declarations changed")
	}
	if bindings["local"].File != filepath.Join(filepath.Dir(filename), "secrets.yaml") {
		t.Fatal("YAML path was not resolved against client settings")
	}
	for _, declaration := range []string{
		"{type: env}",
		"{type: env, env: null}",
		"{type: env, env: []}",
		"{type: env, env: {password: 123}}",
		"{type: env, env: {password: true}}",
		"{type: env, env: {password: 2026-11-07}}",
		"{type: env, env: {password: null}}",
		"{type: env, env: {123: TOKEN}}",
		"{type: env, env: {password: TOKEN, password: SECOND}}",
		"{type: env, env: {password: {nested: TOKEN}}}",
		"{type: env, env: {password: [TOKEN]}}",
		"{type: env, env: {'a..b': TOKEN}}",
		"{type: env, env: {password: INVALID-NAME}}",
		"{type: env, env: {}, file: ''}",
		"{type: env, env: {}, file: null}",
		"{type: yaml, file: secrets.yaml, env: null}",
		"{type: yaml, file: secrets.yaml, env: {}}",
		"{type: yaml, file: 123}",
		"{type: env, env: {}, unknown: private-marker}",
	} {
		t.Run(declaration, func(t *testing.T) {
			writeSourceFixture(t, filename, "secret_sources: {default: "+declaration+"}\n")
			if _, _, err := loadSourceConfig(filename); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("invalid client declaration must fail safely: %v", err)
			}
		})
	}
	for _, text := range []string{
		"secret_sources: {default: {type: env, env: {}}, default: {type: env, env: {}}}",
		"secret_sources: {123: {type: env, env: {}}}",
	} {
		writeSourceFixture(t, filename, text)
		if _, _, err := loadSourceConfig(filename); err == nil {
			t.Fatal("duplicate or non-string source name accepted")
		}
	}
}

func TestCLIRejectsMalformedSourceURLsWithoutPathExpansion(t *testing.T) {
	for _, input := range []string{
		"https:private-source-marker@example.invalid/inventory.yaml",
		"https:/private-source-marker@example.invalid/inventory.yaml",
		"https:%zzprivate-source-marker",
		"https:%%private-source-marker",
		"file:private-source-marker",
		"https://user:private-source-marker@example.invalid/inventory.yaml",
	} {
		code, out, stderr := run(t, "inventory", "validate", "-i", input)
		if code == 0 || strings.Contains(out+stderr, "private-source-marker") || !strings.Contains(stderr, "HTTPS") {
			t.Fatalf("malformed source must be rejected without echo: %d %s %s", code, out, stderr)
		}
	}
}
