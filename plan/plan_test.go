package plan_test

import (
	"bytes"
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
	for _, text := range []string{`{"version":"1.5","hosts":[],"surprise":true}`, `{"version":"1.5","hosts":[]} {}`} {
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

func TestMalformedPlanDiagnosticsDoNotQuotePayload(t *testing.T) {
	marker := "synthetic-confidential-marker"
	path := filepath.Join(t.TempDir(), "plan.json")
	for _, payload := range []string{
		`{"version":"1.5","hosts":[],"` + marker + `":true}`,
		`{"version":"1.5","hosts":[{"tasks":[{"file":{"sensitive":true,"mode":"` + marker + `"}}]}]}`,
		`{"version":"1.5","hosts":[{"tasks":[{"file":{"content":{"` + marker + `":true}}}]}]}`,
		`{"version":"1.5","hosts":[]} {"` + marker + `":true}`,
	} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal("could not write test plan")
		}
		if s, err := plan.Open(path); err == nil {
			s.Close()
			t.Fatal("malformed plan accepted")
		} else if strings.Contains(err.Error(), marker) {
			t.Fatal("plan diagnostic exposed confidential payload")
		}
		// A rejected read must release the sidecar lock.
		if err := plan.Save(path, sample()); err != nil {
			t.Fatal("rejected plan retained its lock")
		}
	}
}

type sensitiveExecutor struct{ failResolve bool }

func (e sensitiveExecutor) Resolve(t plan.Task) ([]plan.Action, error) {
	if e.failResolve {
		return nil, errors.New(t.File.Content)
	}
	return []plan.Action{{Kind: "overwrite-file", File: t.File, Description: []string{t.File.Content}}}, nil
}
func (sensitiveExecutor) Execute(_ context.Context, a plan.Action) error {
	return errors.New(a.File.Content)
}

func TestSensitivePlanOutputAndProgress(t *testing.T) {
	marker := "synthetic-confidential-marker\ntransformed-marker"
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{
		{ID: "s/config/main", Operation: "file", File: &plan.FileChange{Path: "/example", Sensitive: true, Content: marker, Patch: marker}, Progress: plan.Progress{Status: "pending", Error: marker}},
		{ID: "s/config/later", Progress: plan.Progress{Status: "pending"}},
	}}}}
	// Keep the second operation nonsensitive while still using the same fake.
	p.Hosts[0].Tasks[1].File = &plan.FileChange{Path: "/later"}
	for i := range p.Hosts[0].Tasks {
		p.Hosts[0].Tasks[i].Operation = "file"
		prepareFileFixture(p.Hosts[0].Tasks[i].File)
	}
	var output bytes.Buffer
	if err := plan.Display(&output, p, sensitiveExecutor{}); err != nil {
		t.Fatal("sensitive plan display failed")
	}
	if strings.Contains(output.String(), "synthetic-confidential") || !strings.Contains(output.String(), "Content hidden: sensitive") {
		t.Fatal("sensitive action description or patch was exposed")
	}
	for _, apply := range []bool{false, true} {
		var err error
		if apply {
			_, err = plan.Apply(context.Background(), p, sensitiveExecutor{failResolve: true}, nil)
		} else {
			err = plan.Display(&output, p, sensitiveExecutor{failResolve: true})
		}
		if err == nil || strings.Contains(err.Error(), "synthetic-confidential") {
			t.Fatal("sensitive resolution diagnostic was not protected")
		}
	}
	result, err := plan.Apply(context.Background(), p, sensitiveExecutor{}, nil)
	if err == nil || strings.Contains(err.Error(), "synthetic-confidential") {
		t.Fatal("sensitive execution diagnostic was not protected")
	}
	output.Reset()
	if err := plan.DisplayResult(&output, result); err != nil {
		t.Fatal("result display failed")
	}
	if strings.Contains(output.String(), "synthetic-confidential") || result.Hosts[0].Tasks[1].Status != "blocked" {
		t.Fatal("sensitive failure or blocked reason was exposed")
	}
	if p.Hosts[0].Tasks[0].Progress.Error != "sensitive file operation failed" {
		t.Fatal("sensitive progress retained confidential payload")
	}
}
