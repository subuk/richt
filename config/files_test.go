package config_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	c.Name = "main"
	service := inventory.ResolvedService{Assignment: inventory.Assignment{Name: "test", State: "present"}, Definition: inventory.Service{Configs: []inventory.Config{c}}}
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
	c := r.Hosts[0].Services[0].Definition.Configs[0]
	c.Template = inventory.Template{File: source}
	r.Hosts[0].Services[0].Definition.Configs[0] = c
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
		c := r.Hosts[0].Services[0].Definition.Configs[0]
		c.Template.Inline = ptr(source)
		r.Hosts[0].Services[0].Definition.Configs[0] = c
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
		f, err := config.Prepare(c, "absent", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.Observe(f); err == nil {
			t.Fatalf("expected %s error", field)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preparation created target")
	}
}

func TestFollowSymlinksIncludingParentAndRemoveNamedLink(t *testing.T) {
	dir := directory(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unmanaged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	apply(t, diff(t, resolved(link, "through target", false)))
	parent := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, parent); err != nil {
		t.Fatal(err)
	}
	apply(t, diff(t, resolved(filepath.Join(parent, "target"), "through parent", false)))
	path := filepath.Join(dir, "later")
	p := diff(t, resolved(path, "saved", false))
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	apply(t, p)
	b, _ := os.ReadFile(target)
	if string(b) != "saved\n" {
		t.Fatal(string(b))
	}
	r := resolved(link, "", false)
	r.Hosts[0].Services[0].State = "absent"
	apply(t, diff(t, r))
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("named link remains", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("removed symlink target", err)
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

func TestPreparationPreservesRequestedOwnerNamesAndRemoteLabel(t *testing.T) {
	f, err := config.Prepare(inventory.Config{Path: "/etc/richt-example", Owner: "target-only-user", Group: "target-only-group", SELinux: "system_u:object_r:etc_t:s0", Template: inventory.Template{Inline: ptr("prepared locally")}}, "present", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Owner != "target-only-user" || f.Group != "target-only-group" {
		t.Fatal(f)
	}
	if _, err = (config.Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		t.Fatal("resolution used controller capabilities", err)
	}
	if _, err = config.Prepare(inventory.Config{Path: "/etc/richt-example", SELinux: "bad", Template: inventory.Template{Inline: ptr("text")}}, "present", nil); err == nil {
		t.Fatal("accepted malformed label")
	}
}

func TestConfigListOrderAndOriginalTaskPositions(t *testing.T) {
	dir := directory(t)
	r := resolved(filepath.Join(dir, "unused"), "", false)
	r.Hosts[0].Services[0].Definition.Configs = []inventory.Config{
		{Name: "zeta", Path: filepath.Join(dir, "first"), Template: inventory.Template{Inline: ptr("first")}},
		{Path: filepath.Join(dir, "second"), Template: inventory.Template{Inline: ptr("second")}},
		{Name: "alpha", Path: filepath.Join(dir, "third"), Template: inventory.Template{Inline: ptr("third")}},
		{Path: filepath.Join(dir, "fourth"), Template: inventory.Template{Inline: ptr("fourth")}},
	}
	p := diff(t, r)
	for i, task := range p.Hosts[0].Tasks {
		if task.File.Path != r.Hosts[0].Services[0].Definition.Configs[i].Path || task.ID != fmt.Sprintf("test/config/%d", i) {
			t.Fatalf("task %d lost declaration position: %#v", i, task)
		}
	}
	apply(t, p)
	// Filtering unchanged files must not renumber remaining task identities.
	if err := os.WriteFile(filepath.Join(dir, "third"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	p = diff(t, r)
	if len(p.Hosts[0].Tasks) != 1 || p.Hosts[0].Tasks[0].ID != "test/config/2" {
		t.Fatalf("changed task position: %#v", p.Hosts[0].Tasks)
	}
	r.Hosts[0].Services[0].State = "absent"
	p = diff(t, r)
	if len(p.Hosts[0].Tasks) != 4 {
		t.Fatalf("absent tasks: %#v", p.Hosts[0].Tasks)
	}
	for i, task := range p.Hosts[0].Tasks {
		position := 3 - i
		if task.File.Path != r.Hosts[0].Services[0].Definition.Configs[position].Path || task.ID != fmt.Sprintf("test/config/%d", position) {
			t.Fatalf("absent task %d lost reverse order: %#v", i, task)
		}
	}
	apply(t, p)
	for _, c := range r.Hosts[0].Services[0].Definition.Configs {
		if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
			t.Fatalf("file remains after removal: %s (%v)", c.Path, err)
		}
	}
}

func TestPrepareConfigType(t *testing.T) {
	for _, kind := range []string{"", "template", "unknown"} {
		_, err := config.Prepare(inventory.Config{Type: kind, Path: "/tmp/type-test", Template: inventory.Template{Inline: ptr("content")}}, "present", nil)
		if (err != nil) != (kind == "unknown") {
			t.Fatalf("type %q: %v", kind, err)
		}
	}
}
