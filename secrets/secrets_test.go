package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subuk/richt/inventory"
)

func writeSource(t *testing.T, filename, text string) {
	t.Helper()
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal("write fictional source fixture failed")
	}
}

func declarations(filename string) map[string]inventory.SecretSource {
	return map[string]inventory.SecretSource{"default": {Type: "yaml", File: filename}}
}

func requireValue(t *testing.T, r *Resolver, key, source, want string) {
	t.Helper()
	got, err := r.Lookup(key, source)
	if err != nil || got != want {
		t.Fatal("lookup did not return the expected fixture value")
	}
}

func TestYAMLValues(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "values.yaml")
	writeSource(t, filename, `plain: fictional-example
database:
  password: "fictional-nested"
  empty: ""
  multiline: |
    fictional first line
    fictional second line
  numeric: "123"
  boolean: "true"
  explicit: !!str 123
  empty-map: {}
"spaced key": fictional-spaced
`)
	r := New(declarations(filename))
	for key, want := range map[string]string{
		"plain":              "fictional-example",
		"database.password":  "fictional-nested",
		"database.empty":     "",
		"database.multiline": "fictional first line\nfictional second line\n",
		"database.numeric":   "123",
		"database.boolean":   "true",
		"database.explicit":  "123",
		"spaced key":         "fictional-spaced",
	} {
		requireValue(t, r, key, "default", want)
	}
	if _, err := r.Lookup("database", "default"); err != errUnknownKey {
		t.Fatal("a mapping must not resolve to a secret string")
	}
}

func TestSourcesAreIndependentWithoutFallback(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.yaml"), filepath.Join(dir, "second.yaml")
	writeSource(t, first, "shared: fictional-default\nonly-default: fictional-only-default\n")
	writeSource(t, second, "shared: fictional-production\n")
	sources := declarations(first)
	sources["production"] = inventory.SecretSource{Type: "yaml", File: second}
	r := New(sources)
	requireValue(t, r, "shared", "default", "fictional-default")
	requireValue(t, r, "shared", "production", "fictional-production")
	if _, err := r.Lookup("only-default", "production"); err != errUnknownKey {
		t.Fatal("missing key fell back to another source")
	}
	for _, source := range []string{"", "missing"} {
		if _, err := r.Lookup("shared", source); err != errUnknownSource {
			t.Fatal("missing source fell back to another source")
		}
	}
	explicitOnly := New(map[string]inventory.SecretSource{"production": sources["production"]})
	requireValue(t, explicitOnly, "shared", "production", "fictional-production")
}

func TestSourcesLoadLazilyAndOncePerResolver(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "created-after-new.yaml")
	sources := declarations(filename)
	sources["unused"] = inventory.SecretSource{Type: "yaml", File: filepath.Join(dir, "missing.yaml")}
	r := New(sources)
	// The declarations are copied, and files are first read at lookup time.
	sources["default"] = inventory.SecretSource{Type: "yaml", File: filepath.Join(dir, "wrong.yaml")}
	writeSource(t, filename, "value: fictional-original\n")
	requireValue(t, r, "value", "default", "fictional-original")
	writeSource(t, filename, "value: fictional-changed\nadded: fictional-new\n")
	requireValue(t, r, "value", "default", "fictional-original")
	if _, err := r.Lookup("added", "default"); err != errUnknownKey {
		t.Fatal("cached source was reread for an unknown key")
	}
	requireValue(t, New(declarations(filename)), "value", "default", "fictional-changed")
	if err := os.Remove(filename); err != nil {
		t.Fatal("remove fixture failed")
	}
	requireValue(t, r, "value", "default", "fictional-original")
}

func TestSourceFailuresAreCached(t *testing.T) {
	for _, initial := range []string{"missing", "invalid"} {
		t.Run(initial, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "values.yaml")
			want := errReadSource
			if initial == "invalid" {
				writeSource(t, filename, "value: [fictional-invalid]\n")
				want = errInvalidSource
			}
			r := New(declarations(filename))
			if _, err := r.Lookup("value", "default"); err != want {
				t.Fatal("unexpected initial error category")
			}
			writeSource(t, filename, "value: fictional-repaired\n")
			if _, err := r.Lookup("value", "default"); err != want {
				t.Fatal("failed source was read again")
			}
			requireValue(t, New(declarations(filename)), "value", "default", "fictional-repaired")
		})
	}
}

func TestYAMLRejectsInvalidSources(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"empty", ""},
		{"root-null", "null\n"},
		{"root-string", "fictional-value\n"},
		{"root-list", "[fictional-value]\n"},
		{"nested-list", "nested: [fictional-value]\n"},
		{"integer", "value: 123\n"},
		{"float", "value: 1.5\n"},
		{"boolean", "value: true\n"},
		{"timestamp", "value: 2026-01-01\n"},
		{"null", "value: null\n"},
		{"implicit-null", "value:\n"},
		{"nested-nonstring", "nested: {value: 123}\n"},
		{"empty-key", "'': fictional-value\n"},
		{"dotted-key", "nested.value: fictional-value\n"},
		{"nested-dotted-key", "nested: {'nested.value': fictional-value}\n"},
		{"numeric-key", "123: fictional-value\n"},
		{"boolean-key", "true: fictional-value\n"},
		{"mapping-key", "? {key: fictional-key}\n: fictional-value\n"},
		{"duplicate-key", "value: fictional-first\nvalue: fictional-second\n"},
		{"duplicate-nested-key", "nested: {value: fictional-first, value: fictional-second}\n"},
		{"alias-value", "first: &value fictional-value\nsecond: *value\n"},
		{"alias-key", "first: &key fictional-key\n*key: fictional-value\n"},
		{"merge", "first: &first {value: fictional-value}\nsecond: {<<: *first}\n"},
		{"merge-without-alias", "<<: {value: fictional-value}\n"},
		{"multiple-documents", "value: fictional-first\n---\nvalue: fictional-second\n"},
		{"extra-empty-document", "value: fictional-first\n---\n"},
		{"malformed", "value: [fictional-value\n"},
		{"tagged-value", "value: !custom fictional-value\n"},
		{"tagged-mapping", "!custom {value: fictional-value}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "values.yaml")
			writeSource(t, filename, tc.text)
			if value, err := New(declarations(filename)).Lookup("value", "default"); value != "" || err != errInvalidSource {
				t.Fatal("invalid fixture did not produce the safe schema error")
			}
		})
	}
}

func TestSafeErrors(t *testing.T) {
	const marker = "FICTIONAL_SENSITIVE_MARKER"
	filename := filepath.Join(t.TempDir(), marker+".yaml")
	writeSource(t, filename, marker+": ["+marker+"\n")
	r := New(map[string]inventory.SecretSource{
		marker:    {Type: "yaml", File: filename},
		"missing": {Type: "yaml", File: filepath.Join(filename, marker)},
		"other":   {Type: marker, File: filename},
	})
	for _, tc := range []struct {
		key, source string
		want        error
	}{
		{marker, marker, errInvalidSource},
		{marker, "missing", errReadSource},
		{marker, "other", errUnsupportedSource},
		{marker, marker + "-missing", errUnknownSource},
		{"." + marker, marker, errInvalidKey},
	} {
		value, err := r.Lookup(tc.key, tc.source)
		if value != "" || err != tc.want || strings.Contains(err.Error(), marker) {
			t.Fatal("lookup did not return the safe expected error category")
		}
	}
	writeSource(t, filename, "present: fictional-value\n")
	if _, err := New(declarations(filename)).Lookup(marker, "default"); err != errUnknownKey || strings.Contains(err.Error(), marker) {
		t.Fatal("missing key error exposed lookup data")
	}
}

func TestInvalidLookupKeysDoNotReadSource(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "values.yaml")
	r := New(declarations(filename))
	for _, key := range []string{"", ".", ".value", "value.", "nested..value"} {
		if _, err := r.Lookup(key, "default"); err != errInvalidKey {
			t.Fatal("invalid key was accepted")
		}
	}
	writeSource(t, filename, "value: fictional-value\n")
	requireValue(t, r, "value", "default", "fictional-value")
}

func TestConcurrentLookups(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "values.yaml")
	writeSource(t, filename, "value: fictional-value\n")
	r := New(declarations(filename))
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			value, err := r.Lookup("value", "default")
			if err != nil || value != "fictional-value" {
				t.Error("concurrent lookup failed")
			}
		})
	}
	wg.Wait()
}
