package config_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

type lookupFunc func(string, string) (string, error)

func (f lookupFunc) Lookup(key, source string) (string, error) { return f(key, source) }

func TestSecretTemplatesArgumentsTransformsAndSensitivity(t *testing.T) {
	lookup := lookupFunc(func(key, source string) (string, error) {
		if key != "nested.token" || source != "default" && source != "other" {
			t.Fatal("unexpected source selection")
		}
		return "example<token>\nsecond", nil
	})
	for _, tc := range []struct{ source, want string }{
		{`{{secret "nested.token"}}`, "example<token>\nsecond"},
		{`{{secret "nested.token" "other" | printf "%q"}}`, `"example<token>\nsecond"`},
		{`{{html (secret "nested.token")}}`, "example&lt;token&gt;\nsecond"},
		{`{{define "nested"}}{{secret .}}{{end}}{{template "nested" "nested.token"}}`, "example<token>\nsecond"},
	} {
		f, err := config.Prepare(inventory.Config{Path: "/example", Template: inventory.Template{Inline: ptr(tc.source)}}, "present", nil, lookup)
		if err != nil || !f.Sensitive || f.Content != tc.want {
			t.Fatal("secret rendering or sensitivity failed", err)
		}
	}
	for _, sensitive := range []bool{false, true} {
		f, err := config.Prepare(inventory.Config{Path: "/example", Sensitive: sensitive, Template: inventory.Template{Inline: ptr(`{{if false}}{{secret "unused"}}{{end}}plain`)}}, "present", nil, lookup)
		if err != nil || f.Sensitive != sensitive {
			t.Fatal("unused call changed sensitivity", err)
		}
	}
}

func TestSecretTemplateFailuresHaveFixedDiagnostics(t *testing.T) {
	lookup := lookupFunc(func(key, source string) (string, error) {
		if key == "fail" {
			return "", errors.New("example-private-error")
		}
		return "example-private-value\nsecond", nil
	})
	for _, source := range []string{
		`{{secret}}`, `{{secret 1}}`, `{{secret "token" 1}}`, `{{secret "token" "default" "extra"}}`,
		`{{secret "fail"}}`, `{{secret "token" | index 100}}`, `{{index (secret "token") 999}}`,
		`{{$s := secret "token"}}{{printf "%q" $s}}{{.missing}}`,
		`{{secret (secret "token") (secret "token")}}{{eq (secret "token") 1}}`,
		`{{define "example-private-template"}}{{secret "token"}}{{index "x" 99}}{{end}}{{template "example-private-template"}}`,
		`{{secret "example-private-source"`, `{{example-private-unknown}}`,
	} {
		_, err := config.Prepare(inventory.Config{Path: "/example", Template: inventory.Template{Inline: ptr(source)}}, "present", nil, lookup)
		if err == nil || strings.Contains(err.Error(), "example-private") || strings.Contains(err.Error(), "second") {
			t.Fatal("unsafe or missing template error")
		}
	}
}

func TestAbsentDoesNotReadTemplatesOrSecrets(t *testing.T) {
	f, err := config.Prepare(inventory.Config{Path: "/example", Sensitive: true, Template: inventory.Template{File: "/missing/example-template"}}, "absent", nil, lookupFunc(func(string, string) (string, error) {
		t.Fatal("absent fetched a secret")
		return "", nil
	}))
	if err != nil || !f.Sensitive || f.Content != "" {
		t.Fatal("absent preparation failed", err)
	}
}

func TestDirectSensitiveFileErrorsDoNotExposeDetails(t *testing.T) {
	f := &plan.FileChange{Path: filepath.Join(directory(t), "EXAMPLE_PRIVATE_PATH", "target"), State: "present", Sensitive: true, Owner: "EXAMPLE_PRIVATE_OWNER"}
	if _, err := config.Observe(f); err == nil || strings.Contains(err.Error(), "EXAMPLE_PRIVATE") {
		t.Fatal("unsafe observation diagnostic")
	}
	if err := (config.Local{}).Execute(context.Background(), plan.Action{Kind: "set-file-attributes", File: f}); err == nil || strings.Contains(err.Error(), "EXAMPLE_PRIVATE") {
		t.Fatal("unsafe file execution diagnostic")
	}
}

func TestSecretDiffCachesSourceAndSavedPlanFreezesBytes(t *testing.T) {
	dir := directory(t)
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("token: example-first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := resolved(filepath.Join(dir, "target"), "", false)
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: source}}
	r.Hosts[0].Vars["rotate"] = func() string {
		if err := os.WriteFile(source, []byte("token: example-second\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return ""
	}
	r.Hosts[0].Services[0].Definition.Configs = []inventory.Config{
		{Name: "a", Path: filepath.Join(dir, "a"), Template: inventory.Template{Inline: ptr(`{{secret "token"}}{{call .rotate}}`)}},
		{Name: "b", Path: filepath.Join(dir, "b"), Template: inventory.Template{Inline: ptr(`{{secret "token"}}`)}},
	}
	p := diff(t, r)
	for _, task := range p.Hosts[0].Tasks {
		if !task.File.Sensitive || task.File.Content != "example-first" {
			t.Fatal("source was reloaded within one diff")
		}
	}
	var display bytes.Buffer
	if err := plan.Display(&display, p, config.Local{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(display.String(), "example-first") {
		t.Fatal("display leaked secret")
	}
	saved := filepath.Join(dir, "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	s, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := plan.Apply(context.Background(), s.Plan, config.Local{}, s.Save); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "example-first" {
			t.Fatal("saved plan did not preserve secret inputs", err)
		}
	}
}
