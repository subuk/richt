package inventoryyaml

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/subuk/richt/inventory"
)

func TestLoadEnvironmentSecretDeclarations(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, baseYAML+`secret_sources:
  default: {type: env, env: {database.password: RICHT_UNSET_DECLARATION}}
  empty: {type: env, env: {}}
`)
	in, err := Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	if in.SecretSources["default"].Env["database.password"] != "RICHT_UNSET_DECLARATION" || in.SecretSources["empty"].Env == nil {
		t.Fatalf("declarations changed: %#v", in.SecretSources)
	}
}

func TestStrictSecretSourceDeclarations(t *testing.T) {
	for _, declaration := range []string{
		"{type: env}",
		"{type: env, env: null}",
		"{type: env, env: []}",
		"{type: env, env: {password: {nested: TOKEN}}}",
		"{type: env, env: {password: [TOKEN]}}",
		"{type: env, env: {password: 123}}",
		"{type: env, env: {password: true}}",
		"{type: env, env: {password: 2026-11-07}}",
		"{type: env, env: {password: null}}",
		"{type: env, env: {123: TOKEN}}",
		"{type: env, env: {password: TOKEN, password: SECOND}}",
		"{type: env, env: {'a..b': TOKEN}}",
		"{type: env, env: {password: INVALID-NAME}}",
		"{type: env, env: {}, file: ''}",
		"{type: env, env: {}, file: null}",
		"{type: env, env: {}, file: secrets.yaml}",
		"{type: yaml, file: secrets.yaml, env: {}}",
		"{type: yaml, file: secrets.yaml, env: null}",
		"{type: yaml, file: 123}",
		"{type: yaml, file: null}",
		"{type: env, env: {}, unknown: ignored}",
		"{type: true, env: {}}",
		"null",
	} {
		t.Run(declaration, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "inventory.yaml")
			write(t, filename, baseYAML+"secret_sources: {default: "+declaration+"}\n")
			if _, err := Load(filename); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestEnvironmentClientBindings(t *testing.T) {
	body := "services: {}\nhosts: [{name: example, transport: {type: local}}]\n"
	server, client, _ := inventorySourceServer(t, map[string]string{"/inventory.yaml": body}, nil)
	filename := filepath.Join(t.TempDir(), "inventory.yaml")
	write(t, filename, body)
	for _, location := range []string{filename, server.URL + "/inventory.yaml"} {
		bindings := map[string]inventory.SecretSource{"default": {Type: "env", Env: map[string]string{"database.password": "RICHT_UNSET_CLIENT_BINDING"}}}
		in, err := LoadWithOptions(context.Background(), location, LoadOptions{Sources: client, SecretSources: bindings})
		if err != nil {
			t.Fatal(err)
		}
		if in.SecretSources["default"].Env["database.password"] != "RICHT_UNSET_CLIENT_BINDING" {
			t.Fatal("environment binding changed")
		}
	}
	write(t, filename, body+"secret_sources: {default: {type: env, env: {}}}\n")
	if _, err := LoadWithOptions(context.Background(), filename, LoadOptions{SecretSources: map[string]inventory.SecretSource{"default": {Type: "env", Env: map[string]string{}}}}); err == nil {
		t.Fatal("duplicate source name accepted")
	}
	remoteBody := body + "secret_sources: {default: {type: env, env: {}}}\n"
	remote, remoteClient, _ := inventorySourceServer(t, map[string]string{"/inventory.yaml": remoteBody}, nil)
	if _, err := LoadWithOptions(context.Background(), remote.URL+"/inventory.yaml", LoadOptions{Sources: remoteClient}); err == nil {
		t.Fatal("remote environment declaration accepted")
	}
}
