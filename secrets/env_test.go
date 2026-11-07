package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subuk/richt/inventory"
)

func envDeclarations(values map[string]string) map[string]inventory.SecretSource {
	return map[string]inventory.SecretSource{"default": {Type: "env", Env: values}}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal("unset fictional environment fixture failed")
	}
}

func TestEnvValues(t *testing.T) {
	values := map[string]string{
		"database.password": "fictional-password",
		"empty":             "",
		"multiline":         "  fictional first line \nfictional second line\n\n",
		"text":              "fictional-λ-🙂\t\r\x01",
	}
	bindings := make(map[string]string, len(values))
	for key, value := range values {
		name := "RICHT_TEST_ENV_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		t.Setenv(name, value)
		bindings[key] = name
	}
	t.Setenv("RICHT_TEST_ENV_UNDECLARED", "fictional-undeclared")
	r := New(envDeclarations(bindings))
	for key, want := range values {
		requireValue(t, r, key, "default", want)
	}
	for _, key := range []string{"database", "RICHT_TEST_ENV_UNDECLARED"} {
		if value, err := r.Lookup(key, "default"); value != "" || err != errUnknownKey {
			t.Fatal("undeclared key resolved from the environment")
		}
	}
}

func TestEnvVariableAliases(t *testing.T) {
	for _, value := range []string{"fictional-shared", ""} {
		const variable = "RICHT_TEST_ENV_ALIASES"
		t.Setenv(variable, value)
		r := New(envDeclarations(map[string]string{"first": variable, "nested.second": variable}))
		requireValue(t, r, "first", "default", value)
		requireValue(t, r, "nested.second", "default", value)
	}
}

func TestEnvInvalidUTF8FailsWholeSourceAndCachesFailure(t *testing.T) {
	const marker = "FICTIONAL_ENV_INVALID_UTF8_MARKER"
	t.Setenv(marker+"_VALID", "fictional-present")
	t.Setenv(marker+"_INVALID", marker+"\xff")
	sources := envDeclarations(map[string]string{"valid": marker + "_VALID", marker: marker + "_INVALID"})
	r := New(sources)
	if value, err := r.Lookup("valid", "default"); value != "" || err != errInvalidEnvValue || strings.Contains(err.Error(), marker) {
		t.Fatal("invalid UTF-8 did not safely fail the whole environment source")
	}
	t.Setenv(marker+"_INVALID", "fictional-repaired")
	if value, err := r.Lookup(marker, "default"); value != "" || err != errInvalidEnvValue {
		t.Fatal("invalid UTF-8 source failure was not cached")
	}
	requireValue(t, New(sources), marker, "default", "fictional-repaired")
}

func TestEnvSourcesLoadLazilyAndOncePerResolver(t *testing.T) {
	const first = "RICHT_TEST_ENV_LAZY_FIRST"
	const second = "RICHT_TEST_ENV_LAZY_SECOND"
	const unused = "RICHT_TEST_ENV_LAZY_UNUSED"
	unsetEnv(t, first)
	unsetEnv(t, second)
	unsetEnv(t, unused)
	sources := envDeclarations(map[string]string{"first": first, "second": second})
	sources["unused"] = inventory.SecretSource{Type: "env", Env: map[string]string{"value": unused}}
	r := New(sources)
	t.Setenv(first, "fictional-first-original")
	t.Setenv(second, "fictional-second-original")
	requireValue(t, r, "first", "default", "fictional-first-original")
	t.Setenv(first, "fictional-first-changed")
	t.Setenv(second, "fictional-second-changed")
	requireValue(t, r, "first", "default", "fictional-first-original")
	requireValue(t, r, "second", "default", "fictional-second-original")
	fresh := New(sources)
	requireValue(t, fresh, "first", "default", "fictional-first-changed")
	requireValue(t, fresh, "second", "default", "fictional-second-changed")
	// A separate source does not take its snapshot until it is selected.
	t.Setenv(unused, "fictional-unused-now-selected")
	requireValue(t, r, "value", "unused", "fictional-unused-now-selected")
	if err := os.Unsetenv(first); err != nil {
		t.Fatal("unset fictional environment fixture failed")
	}
	requireValue(t, r, "first", "default", "fictional-first-original")
}

func TestEnvMissingVariableFailsWholeSourceAndCachesFailure(t *testing.T) {
	const present = "RICHT_TEST_ENV_FAILURE_PRESENT"
	const missing = "RICHT_TEST_ENV_FAILURE_MISSING"
	t.Setenv(present, "fictional-present")
	unsetEnv(t, missing)
	sources := envDeclarations(map[string]string{"present": present, "missing": missing})
	r := New(sources)
	if value, err := r.Lookup("present", "default"); value != "" || err != errReadSource {
		t.Fatal("missing declared variable did not fail the whole source")
	}
	t.Setenv(missing, "fictional-repaired")
	for _, key := range []string{"present", "missing", "unknown"} {
		if value, err := r.Lookup(key, "default"); value != "" || err != errReadSource {
			t.Fatal("failed environment source was loaded again")
		}
	}
	requireValue(t, New(sources), "missing", "default", "fictional-repaired")
}

func TestEnvDeclarationsAreDeepCopied(t *testing.T) {
	const original = "RICHT_TEST_ENV_COPY_ORIGINAL"
	const changed = "RICHT_TEST_ENV_COPY_CHANGED"
	t.Setenv(original, "fictional-original")
	t.Setenv(changed, "fictional-changed")
	bindings := map[string]string{"value": original}
	sources := envDeclarations(bindings)
	r := New(sources)
	bindings["value"] = changed
	bindings["added"] = changed
	delete(sources, "default")
	requireValue(t, r, "value", "default", "fictional-original")
	if _, err := r.Lookup("added", "default"); err != errUnknownKey {
		t.Fatal("resolver retained the caller's declaration map")
	}
}

func TestEnvAndYAMLSourcesAreIndependentWithoutFallback(t *testing.T) {
	const variable = "RICHT_TEST_ENV_INDEPENDENT"
	t.Setenv(variable, "fictional-environment")
	filename := filepath.Join(t.TempDir(), "values.yaml")
	writeSource(t, filename, "shared: fictional-yaml\nonly-yaml: fictional-only-yaml\n")
	sources := declarations(filename)
	sources["environment"] = inventory.SecretSource{Type: "env", Env: map[string]string{"shared": variable}}
	r := New(sources)
	requireValue(t, r, "shared", "default", "fictional-yaml")
	requireValue(t, r, "shared", "environment", "fictional-environment")
	if _, err := r.Lookup("only-yaml", "environment"); err != errUnknownKey {
		t.Fatal("environment lookup fell back to the YAML source")
	}
	explicitOnly := New(map[string]inventory.SecretSource{"environment": sources["environment"]})
	if _, err := explicitOnly.Lookup("shared", "default"); err != errUnknownSource {
		t.Fatal("missing source fell back to the environment source")
	}
	requireValue(t, explicitOnly, "shared", "environment", "fictional-environment")
}

func TestEnvSafeErrors(t *testing.T) {
	const marker = "FICTIONAL_ENV_SENSITIVE_MARKER"
	t.Setenv(marker+"_PRESENT", marker+"_VALUE")
	unsetEnv(t, marker+"_MISSING")
	r := New(map[string]inventory.SecretSource{
		marker: {Type: "env", Env: map[string]string{marker: marker + "_PRESENT", marker + "_OTHER": marker + "_MISSING"}},
	})
	value, err := r.Lookup(marker, marker)
	if value != "" || err != errReadSource || strings.Contains(err.Error(), marker) {
		t.Fatal("environment source error exposed secret lookup data")
	}
	r = New(envDeclarations(map[string]string{"value": marker + "_PRESENT"}))
	if value, err := r.Lookup(marker, "default"); value != "" || err != errUnknownKey || strings.Contains(err.Error(), marker) {
		t.Fatal("missing environment key error exposed lookup data")
	}
}

func TestInvalidLookupKeysDoNotReadEnvSource(t *testing.T) {
	const variable = "RICHT_TEST_ENV_INVALID_KEY"
	unsetEnv(t, variable)
	r := New(envDeclarations(map[string]string{"value": variable}))
	for _, key := range []string{"", ".", ".value", "value.", "nested..value"} {
		if _, err := r.Lookup(key, "default"); err != errInvalidKey {
			t.Fatal("invalid key was accepted")
		}
	}
	t.Setenv(variable, "fictional-value")
	requireValue(t, r, "value", "default", "fictional-value")
}

func TestConcurrentEnvLookups(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "success"
		if missing {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			const variable = "RICHT_TEST_ENV_CONCURRENT"
			t.Setenv(variable, "fictional-value")
			var wantErr error
			want := "fictional-value"
			if missing {
				unsetEnv(t, variable)
				want, wantErr = "", errReadSource
			}
			r := New(envDeclarations(map[string]string{"value": variable}))
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					value, err := r.Lookup("value", "default")
					if err != wantErr || value != want {
						t.Error("concurrent environment lookup failed")
					}
				})
			}
			wg.Wait()
		})
	}
}
