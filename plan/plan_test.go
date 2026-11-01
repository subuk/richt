package plan_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subuk/richt/plan"
)

type executor struct {
	mu    sync.Mutex
	calls []string
	fail  string
}

func (e *executor) Resolve(t plan.Task) ([]plan.Action, error) {
	return []plan.Action{{Kind: t.ID}}, nil
}
func (e *executor) Execute(_ context.Context, a plan.Action) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, a.Kind)
	if a.Kind == e.fail {
		return errors.New("planned failure")
	}
	return nil
}
func sample() *plan.Plan {
	return &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "first", Transport: "local", Tasks: []plan.Task{{ID: "one", Progress: plan.Progress{Status: "pending"}}, {ID: "two", Progress: plan.Progress{Status: "pending"}}, {ID: "three", Progress: plan.Progress{Status: "pending"}}}}, {Name: "second", Transport: "local", Tasks: []plan.Task{{ID: "other", Progress: plan.Progress{Status: "pending"}}}}}}
}
func TestContinueAndHostIsolation(t *testing.T) {
	p := sample()
	e := &executor{fail: "two"}
	saved := 0
	r, err := plan.Apply(context.Background(), p, e, func(*plan.Plan) error { saved++; return nil }, plan.ApplyOptions{HostConcurrency: 1})
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Join(e.calls, ",") != "one,two,other" {
		t.Fatal(e.calls)
	}
	if len(r.Hosts) != 2 || saved != 6 {
		t.Fatalf("result %+v saves %d", r, saved)
	}
	e.calls = nil
	e.fail = ""
	_, err = plan.Apply(context.Background(), p, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.calls, ",") != "two,three" {
		t.Fatal(e.calls)
	}
}
func TestPersistenceFailureStopsEverything(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		p := sample()
		e := &executor{}
		n := 0
		_, err := plan.Apply(context.Background(), p, e, func(*plan.Plan) error {
			n++
			if n == failAt {
				return errors.New("disk full")
			}
			return nil
		}, plan.ApplyOptions{HostConcurrency: 1})
		if err == nil {
			t.Fatal("expected failure")
		}
		want := 0
		if failAt > 1 {
			want = 1
		}
		if len(e.calls) != want {
			t.Fatalf("failure at %d executed %v", failAt, e.calls)
		}
	}
}
func TestStoreLockResumeAndVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	p := sample()
	if err := plan.Save(path, p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\n  \"version\"") {
		t.Fatal("JSON not indented")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	s, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Open(path); err == nil {
		t.Fatal("concurrent open accepted")
	}
	if err := plan.Save(path, p); err == nil {
		t.Fatal("concurrent overwrite accepted")
	}
	e := &executor{fail: "two"}
	plan.Apply(context.Background(), s.Plan, e, s.Save)
	s.Close()
	s, err = plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Plan.Hosts[0].Tasks[0].Progress.Status != "success" {
		t.Fatal("success not persisted")
	}
	s.Close()
	os.WriteFile(path, []byte(`{"version":"999","hosts":[]}`), 0600)
	if _, err := plan.Open(path); err == nil {
		t.Fatal("incompatible plan accepted")
	}
	for _, text := range []string{`{"version":"1.0","hosts":[],"surprise":true}`, `{"version":"1.0","hosts":[]} {}`} {
		os.WriteFile(path, []byte(text), 0600)
		if _, err := plan.Open(path); err == nil {
			t.Fatal("malformed plan accepted")
		}
	}
}
func TestStoreAliasLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := plan.Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	s, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := plan.Open(filepath.Join(alias, "plan.json")); err == nil {
		t.Fatal("alias bypassed lock")
	}
}

func TestStoreRejectsSymlinkLockAndHardlinkPlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := plan.Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "hardlink.json")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if s, err := plan.Open(alias); err == nil {
		s.Close()
		t.Fatal("hardlink plan accepted")
	}
	os.Remove(alias)
	os.Remove(path + ".lock")
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("preserved"), 0600)
	if err := os.Symlink(other, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if s, err := plan.Open(path); err == nil {
		s.Close()
		t.Fatal("symlink lock accepted")
	}
	content, _ := os.ReadFile(other)
	if string(content) != "preserved" {
		t.Fatal("lock followed symlink")
	}
}
