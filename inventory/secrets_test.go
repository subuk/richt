package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretSourceValidation(t *testing.T) {
	for _, source := range []SecretSource{
		{Type: "yaml", File: "/does-not-exist/secrets.yaml"},
		{Type: "env", Env: map[string]string{}},
		{Type: "env", Env: map[string]string{"database.password": "RICHT_UNSET_ENV", "token": "_TOKEN_2"}},
	} {
		in := fixture()
		in.SecretSources = map[string]SecretSource{"default": source}
		if _, err := in.Resolve(Selectors{}); err != nil {
			t.Fatalf("valid declaration failed: %v", err)
		}
	}
	for _, source := range []SecretSource{
		{},
		{Type: "unsupported"},
		{Type: "yaml"},
		{Type: "yaml", File: "secrets.yaml", Env: map[string]string{}},
		{Type: "env"},
		{Type: "env", File: "secrets.yaml", Env: map[string]string{}},
	} {
		if err := ValidateSecretSource("default", source); err == nil {
			t.Fatalf("invalid declaration accepted: %#v", source)
		}
	}
	for _, key := range []string{"", ".password", "database.", "a..b"} {
		if err := ValidateSecretSource("default", SecretSource{Type: "env", Env: map[string]string{key: "TOKEN"}}); err == nil {
			t.Errorf("invalid key accepted: %q", key)
		}
	}
	for _, variable := range []string{"", "1TOKEN", "TOKEN-NAME", "TOKEN.NAME", "TOKEN NAME", "TOKEN\n", "TÖKEN", "${TOKEN}"} {
		if err := ValidateSecretSource("default", SecretSource{Type: "env", Env: map[string]string{"token": variable}}); err == nil {
			t.Errorf("invalid environment variable name accepted: %q", variable)
		}
	}
	if err := ValidateSecretSource("bad.name", SecretSource{Type: "env", Env: map[string]string{}}); err == nil {
		t.Fatal("invalid source name accepted")
	}
}

func TestEnvironmentSecretSourceJSONContainsDeclarations(t *testing.T) {
	data, err := json.Marshal(SecretSource{Type: "env", Env: map[string]string{"database.password": "RICHT_PASSWORD"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"file"`) || !strings.Contains(string(data), `"database.password":"RICHT_PASSWORD"`) {
		t.Fatalf("unexpected declaration JSON: %s", data)
	}
}
