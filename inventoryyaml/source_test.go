package inventoryyaml

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/source"
)

func sourceDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func inventorySourceServer(t *testing.T, documents map[string]string, redirects map[string]string) (*httptest.Server, *source.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if location := redirects[r.URL.Path]; location != "" {
			http.Redirect(w, r, location, http.StatusFound)
			return
		}
		if body, ok := documents[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "source-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := source.New(source.Options{CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	return server, client, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), requests...) }
}

func TestLocalInventoryMixesLocalAndHTTPSServiceSources(t *testing.T) {
	hash := strings.Repeat("A", 64)
	remote := "order: 20\nconfigs: [{path: /etc/remote, template: {url: ../templates/remote.gotmpl, sha256: '" + hash + "'}}]\n"
	server, client, requests := inventorySourceServer(t, map[string]string{"/final/services/remote.yaml": remote}, map[string]string{"/service.yaml": "/final/services/remote.yaml"})
	dir := t.TempDir()
	filename := filepath.Join(dir, "inventory.yaml")
	write(t, filepath.Join(dir, "services/local.yaml"), "order: 10\nconfigs: [{path: /etc/local, template: {file: missing.gotmpl}}]\n")
	write(t, filename, fmt.Sprintf(`tags: {common: {services: {local: {}, remote: {state: absent}}}}
services:
  local: {file: services/local.yaml}
  remote: {url: %s/service.yaml, sha256: '%s'}
hosts: [{name: example, tags: [common], transport: {type: local}}]
secret_sources: {default: {type: yaml, file: missing-secrets.yaml}}
`, server.URL, strings.ToUpper(sourceDigest(remote))))
	in, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client})
	if err != nil {
		t.Fatal(err)
	}
	local, remoteService := in.Services["local"], in.Services["remote"]
	if local.SourceFile != filepath.Join(dir, "services/local.yaml") || local.Configs[0].Template.File != filepath.Join(dir, "services/missing.gotmpl") {
		t.Fatalf("local source lost: %#v", local)
	}
	if remoteService.SourceFile != "" || remoteService.SourceURL != server.URL+"/final/services/remote.yaml" || remoteService.Configs[0].Template.URL != server.URL+"/final/templates/remote.gotmpl" || remoteService.Configs[0].Template.SHA256 != strings.ToLower(hash) {
		t.Fatalf("remote source lost: %#v", remoteService)
	}
	resolved, err := in.Resolve(inventory.Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	if in.SourceIdentity != filename || resolved.SourceIdentity != filename {
		t.Fatal("configured local inventory identity was not retained")
	}
	if resolved.Hosts[0].Services[1].State != "absent" {
		t.Fatal("absence lost")
	}
	if got := requests(); len(got) != 2 {
		t.Fatalf("templates must stay unopened: %v", got)
	}
}

func TestRemoteInventoryUsesFinalDocumentBasesAndClientSecrets(t *testing.T) {
	hash := strings.Repeat("B", 64)
	remote := "order: 20\nprocesses:\n  - type: systemd\n    unit: app.service\n    unit_file: {template: {url: ../templates/unit, sha256: '" + hash + "'}}\n    overrides: {10-extra.conf: {template: {url: ../templates/dropin, sha256: '" + hash + "'}}}\n"
	body := fmt.Sprintf(`tags: {common: {services: {remote: {state: absent}, inline: {}}}}
services:
  remote: {url: ../remote.yaml, sha256: '%s'}
  inline:
    order: 10
    configs: [{path: /etc/inline, template: {url: ../templates/inline, sha256: '%s'}}]
hosts: [{name: example, tags: [common], transport: {type: local}}]
`, sourceDigest(remote), hash)
	server, client, requests := inventorySourceServer(t, map[string]string{"/tenant/root/inventory.yaml": body, "/stable/services/app.yaml": remote}, map[string]string{"/inventory.yaml": "/tenant/root/inventory.yaml", "/tenant/remote.yaml": "/stable/services/app.yaml"})
	secrets := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	in, err := LoadWithOptions(context.Background(), server.URL+"/inventory.yaml", LoadOptions{Sources: client, SHA256: strings.ToUpper(sourceDigest(body)), SecretSources: map[string]inventory.SecretSource{"production": {Type: "yaml", File: secrets}}})
	if err != nil {
		t.Fatal(err)
	}
	if in.SecretSources["production"].File != secrets {
		t.Fatal("client secret binding lost")
	}
	resolved, err := in.Resolve(inventory.Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	if in.SourceIdentity != server.URL+"/inventory.yaml" || resolved.SourceIdentity != in.SourceIdentity {
		t.Fatal("redirect replaced configured root inventory identity")
	}
	inline, s := in.Services["inline"], in.Services["remote"]
	if inline.SourceURL != server.URL+"/tenant/root/inventory.yaml" || inline.Configs[0].Template.URL != server.URL+"/tenant/templates/inline" {
		t.Fatalf("inventory redirect base lost: %#v", inline)
	}
	if s.SourceURL != server.URL+"/stable/services/app.yaml" || s.Processes[0].UnitFile.Template.URL != server.URL+"/stable/templates/unit" || s.Processes[0].Overrides["10-extra.conf"].Template.URL != server.URL+"/stable/templates/dropin" {
		t.Fatalf("service redirect base lost: %#v", s)
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"source_url"`) || !strings.Contains(string(encoded), `"sha256":"`+strings.ToLower(hash)+`"`) {
		t.Fatalf("resolved source metadata missing: %s", encoded)
	}
	if strings.Contains(string(encoded), "SourceIdentity") || strings.Contains(string(encoded), "source_identity") {
		t.Fatal("controller cache identity leaked into inventory output")
	}
	if got := requests(); len(got) != 4 {
		t.Fatalf("only inventory and service documents may be read: %v", got)
	}
}

func TestRemoteInventoryRejectsLocalAuthority(t *testing.T) {
	host := "\nhosts: [{name: example, transport: {type: local}}]\n"
	for _, tc := range []struct{ name, body string }{
		{"service-file", "services: {app: {file: /private/missing-service.yaml}}" + host},
		{"template-file", "services: {app: {order: 0, configs: [{path: /etc/app, template: {file: /private/missing-template}}]}}" + host},
		{"unit-file-template", "services: {app: {order: 0, processes: [{type: systemd, unit: app.service, unit_file: {template: {file: /private/unit}}}]}}" + host},
		{"dropin-template", "services: {app: {order: 0, processes: [{type: systemd, unit: app.service, overrides: {10-extra.conf: {template: {file: /private/dropin}}}}]}}" + host},
		{"empty-secrets", "services: {}\nsecret_sources: {}" + host},
		{"null-secrets", "services: {}\nsecret_sources: null" + host},
		{"secrets", "services: {}\nsecret_sources: {default: {type: yaml, file: /private/secrets}}" + host},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client, requests := inventorySourceServer(t, map[string]string{"/inventory.yaml": tc.body}, nil)
			_, err := LoadWithOptions(context.Background(), server.URL+"/inventory.yaml", LoadOptions{Sources: client})
			if err == nil {
				t.Fatal("remote local reference accepted")
			}
			if strings.Contains(err.Error(), "/private/") {
				t.Fatalf("remote scalar exposed: %v", err)
			}
			if got := requests(); len(got) != 1 {
				t.Fatalf("unexpected source access: %v", got)
			}
		})
	}
}

func TestSourceDeclarationsValidatedBeforeFetch(t *testing.T) {
	for _, fields := range []string{
		"url: SOURCE/service.yaml", // Missing pin.
		"url: SOURCE/service.yaml, sha256: invalid",
		"url: SOURCE/service.yaml, sha256: 123",
		"url: SOURCE/service.yaml, sha256: HASH, order: 0",
		"url: SOURCE/service.yaml, sha256: HASH, configs: []",
		"url: SOURCE/service.yaml, sha256: HASH, file: missing.yaml",
		"url: relative.yaml, sha256: HASH",
		"url: http://example.invalid/service.yaml, sha256: HASH",
		"url: https://user:NEVER_PRINT_CREDENTIAL@example.invalid/service.yaml, sha256: HASH",
		"url: SOURCE/service.yaml, sha256: HASH, typo: true",
	} {
		t.Run(fields, func(t *testing.T) {
			server, client, requests := inventorySourceServer(t, nil, nil)
			fields = strings.ReplaceAll(strings.ReplaceAll(fields, "SOURCE", server.URL), "HASH", "'"+strings.Repeat("a", 64)+"'")
			filename := filepath.Join(t.TempDir(), "inventory.yaml")
			write(t, filename, "services: {app: {"+fields+"}}\nhosts: [{name: example, transport: {type: local}}]\n")
			if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client}); err == nil {
				t.Fatal("invalid source declaration accepted")
			}
			if got := requests(); len(got) != 0 {
				t.Fatalf("invalid declaration caused network access: %v", got)
			}
		})
	}
	// Invalid inline definitions outside selection must also fail before fetching.
	server, client, requests := inventorySourceServer(t, nil, nil)
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, fmt.Sprintf("services:\n  remote: {url: %s/service.yaml, sha256: '%s'}\n  unused: {order: 1, configs: [{path: /etc/app, template: {url: relative.yaml, sha256: '%s'}}]}\nhosts: [{name: example, transport: {type: local}}]\n", server.URL, strings.Repeat("a", 64), strings.Repeat("a", 64)))
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client}); err == nil {
		t.Fatal("invalid unused definition accepted")
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("invalid inline definition caused network access: %v", got)
	}
}

func TestRemoteDiagnosticsDoNotExposeScalars(t *testing.T) {
	marker := "NEVER_PRINT_REMOTE_CREDENTIAL"
	for _, body := range []string{
		"services: [" + marker,
		"services: {}\n" + marker + ": true\nhosts: []\n",
		"services: {}\nhosts: [{name: " + marker + ", transport: {type: invalid}}]\n",
		"services: {app: {order: 0, configs: [{type: " + marker + "}]}}\nhosts: []\n",
		"services: {}\nhosts: []\nhosts: [" + marker + "]\n",
		"services: {}\nhosts: []\n---\n" + marker + ": [\n",
	} {
		t.Run(fmt.Sprintf("body-%s", sourceDigest(body)[:8]), func(t *testing.T) {
			server, client, _ := inventorySourceServer(t, map[string]string{"/inventory.yaml": body}, nil)
			_, err := LoadWithOptions(context.Background(), server.URL+"/inventory.yaml", LoadOptions{Sources: client})
			if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), server.URL+"/inventory.yaml") {
				t.Fatalf("unsafe remote diagnostic: %v", err)
			}
		})
	}
	// A remote service referenced by local inventory retains the same protection.
	body := "order: 0\nconfigs: [{type: " + marker + "}]\n"
	server, client, _ := inventorySourceServer(t, map[string]string{"/service.yaml": body}, nil)
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, fmt.Sprintf("services: {unused: {url: %s/service.yaml, sha256: '%s'}}\nhosts: [{name: example, transport: {type: local}}]\n", server.URL, sourceDigest(body)))
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client}); err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("unsafe service diagnostic: %v", err)
	}
}

func TestInventorySourceChecksumsAndClientBindings(t *testing.T) {
	body := "services: {}\nhosts: [{name: example, transport: {type: local}}]\n"
	server, client, requests := inventorySourceServer(t, map[string]string{"/inventory.yaml": body}, nil)
	if _, err := LoadWithOptions(context.Background(), server.URL+"/inventory.yaml", LoadOptions{Sources: client, SHA256: strings.Repeat("0", 64)}); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("root checksum mismatch accepted: %v", err)
	}
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, body)
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client, SHA256: sourceDigest(body)}); err == nil {
		t.Fatal("local checksum accepted")
	}
	for _, binding := range []inventory.SecretSource{{Type: "yaml", File: "relative.yaml"}, {Type: "unsupported", File: "/not-read.yaml"}, {Type: "yaml", File: "https://example.invalid/secrets"}} {
		if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client, SecretSources: map[string]inventory.SecretSource{"default": binding}}); err == nil {
			t.Fatalf("invalid client binding accepted: %#v", binding)
		}
	}
	write(t, filename, body+"secret_sources: {default: {type: yaml, file: not-read.yaml}}\n")
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client, SecretSources: map[string]inventory.SecretSource{"default": {Type: "yaml", File: "/not-read.yaml"}}}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("silent client override: %v", err)
	}
	if got := requests(); len(got) != 1 {
		t.Fatalf("unexpected fetch: %v", got)
	}
}

func TestReferencedRemoteServicesCannotRecurseOrReadLocalFiles(t *testing.T) {
	for _, body := range []string{
		"file: /not-read.yaml\n",
		"url: https://example.invalid/recursive.yaml\nsha256: '" + strings.Repeat("a", 64) + "'\n",
		"order: 0\nconfigs: [{path: /etc/app, template: {file: /not-read.gotmpl}}]\n",
	} {
		t.Run(sourceDigest(body)[:8], func(t *testing.T) {
			server, client, requests := inventorySourceServer(t, map[string]string{"/service.yaml": body}, nil)
			filename := filepath.Join(t.TempDir(), "inventory.yaml")
			write(t, filename, fmt.Sprintf("services: {unused: {url: %s/service.yaml, sha256: '%s'}}\nhosts: [{name: example, transport: {type: local}}]\n", server.URL, sourceDigest(body)))
			if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client}); err == nil {
				t.Fatal("unsafe remote service accepted")
			}
			if got := requests(); len(got) != 1 {
				t.Fatalf("unexpected recursive access: %v", got)
			}
		})
	}
	body := "order: 0\nconfigs: [{path: /etc/app, template: {inline: value}}]\n"
	server, client, _ := inventorySourceServer(t, map[string]string{"/service.yaml": body}, nil)
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, fmt.Sprintf("services: {unused: {url: %s/service.yaml, sha256: '%s'}}\nhosts: [{name: example, transport: {type: local}}]\n", server.URL, strings.Repeat("0", 64)))
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{Sources: client}); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("service checksum mismatch accepted: %v", err)
	}
}

func TestRemoteInventoryPreservesSafeReferencedFetchFailures(t *testing.T) {
	service := "order: 0\nconfigs: [{path: /etc/app, template: {inline: value}}]\n"
	for _, tc := range []struct{ name, path, pin, want string }{
		{"http-status", "/missing.yaml", sourceDigest(service), "HTTP status 404"},
		{"checksum", "/service.yaml", strings.Repeat("0", 64), "SHA-256 mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fmt.Sprintf("services: {app: {url: %s, sha256: '%s'}}\nhosts: [{name: example, transport: {type: local}}]\n", tc.path, tc.pin)
			server, client, _ := inventorySourceServer(t, map[string]string{"/inventory.yaml": root, "/service.yaml": service}, nil)
			_, err := LoadWithOptions(context.Background(), server.URL+"/inventory.yaml", LoadOptions{Sources: client})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("safe fetch failure category lost: %v", err)
			}
		})
	}
}

func TestMalformedInventoryURLsNeverBecomeLocalPaths(t *testing.T) {
	for _, raw := range []string{"https:%zzNEVER_PRINT_URL", "https:%%NEVER_PRINT_URL", "https:NEVER_PRINT_URL@example.invalid/inventory.yaml", "https:/NEVER_PRINT_URL@example.invalid/inventory.yaml"} {
		_, err := LoadWithOptions(context.Background(), raw, LoadOptions{})
		if err == nil || strings.Contains(err.Error(), "NEVER_PRINT_URL") {
			t.Fatalf("malformed URL interpreted as local input: %v", err)
		}
	}
}
