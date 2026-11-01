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

func directory(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func resolved(path, content string, sensitive bool) *inventory.Resolved {
	c := inventory.Config{Path: path, Template: inventory.Template{Inline: ptr("{{.message}}\n")}, Mode: "0640", Sensitive: sensitive}
	service := inventory.ResolvedService{Assignment: inventory.Assignment{Name: "test", State: "present"}, Definition: inventory.Service{Configs: map[string]inventory.Config{"main": c}}}
	host := inventory.ResolvedHost{Name: "local", Transport: inventory.Transport{Type: "local"}, Vars: map[string]any{"message": content}, Services: []inventory.ResolvedService{service}}
	return &inventory.Resolved{Hosts: []inventory.ResolvedHost{host}}
}

func ptr(s string) *string { return &s }
func diff(t *testing.T, r *inventory.Resolved) *plan.Plan {
	t.Helper()
	p, err := config.Diff(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func apply(t *testing.T, p *plan.Plan) {
	t.Helper()
	if _, err := plan.Apply(context.Background(), p, config.Local{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFileLifecycleAndFrozenPlan(t *testing.T) {
	dir := directory(t)
	path := filepath.Join(dir, "new", "config")
	r := resolved(path, "first", false)
	p := diff(t, r)
	if len(p.Hosts[0].Tasks) != 1 {
		t.Fatal("expected create task")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("diff created directories")
	}
	apply(t, p)
	b, _ := os.ReadFile(path)
	if string(b) != "first\n" {
		t.Fatalf("content %q", b)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	p = diff(t, r)
	if len(p.Hosts[0].Tasks) != 0 {
		t.Fatal("expected no changes")
	}
	r.Hosts[0].Vars["message"] = "second"
	p = diff(t, r)
	before, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte("manual later change\n"), 0640); err != nil {
		t.Fatal(err)
	}
	r.Hosts[0].Vars["message"] = "third"
	apply(t, p)
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("target was atomically replaced")
	}
	b, _ = os.ReadFile(path)
	if string(b) != "second\n" {
		t.Fatal("apply rebuilt the plan")
	}
	r.Hosts[0].Services[0].State = "absent"
	p = diff(t, r)
	apply(t, p)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if len(diff(t, r).Hosts[0].Tasks) != 0 {
		t.Fatal("absent must be idempotent")
	}
}

func TestTemplateFileCanDisappearAfterSave(t *testing.T) {
	dir := directory(t)
	path := filepath.Join(dir, "target")
	source := filepath.Join(dir, "source.tmpl")
	os.WriteFile(source, []byte("{{.message}}\n"), 0600)
	r := resolved(path, "frozen", false)
	c := r.Hosts[0].Services[0].Definition.Configs["main"]
	c.Template = inventory.Template{File: source}
	r.Hosts[0].Services[0].Definition.Configs["main"] = c
	p := diff(t, r)
	saved := filepath.Join(dir, "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	os.Remove(source)
	s, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = plan.Apply(context.Background(), s.Plan, config.Local{}, s.Save); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "frozen\n" {
		t.Fatal(string(b))
	}
}

func TestSensitiveHidesBothSidesContextAndErrors(t *testing.T) {
	dir := directory(t)
	path := filepath.Join(dir, "file")
	os.WriteFile(path, []byte("old EXAMPLE_SENSITIVE_VALUE\ncontext EXAMPLE_SENSITIVE_VALUE\n"), 0600)
	p := diff(t, resolved(path, "new EXAMPLE_SENSITIVE_VALUE", true))
	var out bytes.Buffer
	if err := plan.Display(&out, p, config.Local{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "EXAMPLE_SENSITIVE_VALUE") {
		t.Fatalf("leaked sensitive patch: %s", out.String())
	}
	os.Remove(path)
	os.Mkdir(path, 0755)
	result, err := plan.Apply(context.Background(), p, config.Local{}, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	out.Reset()
	plan.DisplayResult(&out, result)
	if !strings.Contains(out.String(), "sensitive file operation failed") {
		t.Fatal(out.String())
	}
	c := inventory.Config{Path: path, Template: inventory.Template{Inline: ptr("literal private {{.missing}}")}, Sensitive: true}
	_, err = config.Prepare(c, "present", nil)
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe template error: %v", err)
	}
}

func TestPreparationErrorsBeforeChanges(t *testing.T) {
	dir := directory(t)
	path := filepath.Join(dir, "target")
	for _, source := range []string{"{{.missing}}", "{{secret \"fake-example\"}}"} {
		r := resolved(path, "value", false)
		c := r.Hosts[0].Services[0].Definition.Configs["main"]
		c.Template.Inline = ptr(source)
		r.Hosts[0].Services[0].Definition.Configs["main"] = c
		if _, err := config.Diff(r); err == nil {
			t.Fatalf("expected preparation error for %s", source)
		}
	}
	for _, field := range []string{"owner", "group"} {
		c := inventory.Config{Path: path}
		if field == "owner" {
			c.Owner = "richt-user-does-not-exist"
		} else {
			c.Group = "richt-group-does-not-exist"
		}
		if _, err := config.Prepare(c, "absent", nil); err == nil {
			t.Fatalf("expected %s error", field)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preparation created target")
	}
}

func TestRejectSymlinksIncludingParent(t *testing.T) {
	dir := directory(t)
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("unmanaged"), 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if _, err := config.Diff(resolved(link, "bad", false)); err == nil {
		t.Fatal("accepted target symlink")
	}
	parent := filepath.Join(dir, "parent")
	os.Symlink(dir, parent)
	if _, err := config.Diff(resolved(filepath.Join(parent, "target"), "bad", false)); err == nil {
		t.Fatal("accepted parent symlink")
	}
	path := filepath.Join(dir, "later")
	p := diff(t, resolved(path, "bad", false))
	os.Symlink(target, path)
	if _, err := plan.Apply(context.Background(), p, config.Local{}, nil); err == nil {
		t.Fatal("accepted apply symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "unmanaged" {
		t.Fatal("followed symlink")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }
func TestDisplayFailure(t *testing.T) {
	p := diff(t, resolved(filepath.Join(directory(t), "file"), "hello", false))
	if err := plan.Display(failingWriter{}, p, config.Local{}); err == nil {
		t.Fatal("ignored output failure")
	}
}
