package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/mount"
	"github.com/subuk/richt/oneshot"
	"github.com/subuk/richt/plan"
)

func TestTypedPathsPreserveOrderContentsAndReactions(t *testing.T) {
	root := tempDir(t)
	dir, marker := filepath.Join(root, "data"), filepath.Join(root, "data", "marker")
	r := resolved(inventory.Service{Configs: []inventory.Config{
		{Type: "directory", Path: dir, Mode: "0750"},
		{Name: "marker", Type: "file", Path: marker, Mode: "0640"},
		{Path: filepath.Join(dir, "settings"), Template: inventory.Template{Inline: inline("frozen")}},
	}, Processes: []inventory.Process{{Type: "oneshot", Command: []string{"observe"}, RunOn: []string{"config.marker"}}}})
	calls := 0
	e := Local{Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { calls++; return nil }}}
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var operations []string
	for _, task := range p.Hosts[0].Tasks {
		operations = append(operations, task.Operation)
	}
	if strings.Join(operations, ",") != "path,path,file,oneshot" {
		t.Fatal(operations)
	}
	var shown bytes.Buffer
	if err = plan.Display(&shown, p, e); err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Apply(context.Background(), p, e, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if err = os.WriteFile(marker, []byte("unmanaged contents"), 0640); err != nil {
		t.Fatal(err)
	}
	p, err = e.Diff(context.Background(), r)
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal(p, err)
	}
	r.Hosts[0].Services[0].Definition.Configs[1].Mode = "0600"
	p, err = e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hosts[0].Tasks) != 2 || p.Hosts[0].Tasks[1].Oneshot == nil {
		t.Fatal(p)
	}
	if _, err = plan.Apply(context.Background(), p, e, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "unmanaged contents" || calls != 2 {
		t.Fatal(string(got), calls, err)
	}
	if _, err = plan.Apply(context.Background(), p, e, nil); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
}

func TestTypedPathAbsenceUsesReverseConfigOrder(t *testing.T) {
	dir := filepath.Join(tempDir(t), "data")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "marker")
	if err := os.WriteFile(file, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	r := resolved(inventory.Service{Configs: []inventory.Config{{Type: "directory", Path: dir}, {Type: "file", Path: file}}})
	r.Hosts[0].Services[0].State = "absent"
	p, err := Diff(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hosts[0].Tasks) != 2 || p.Hosts[0].Tasks[0].Path.Path != file || p.Hosts[0].Tasks[1].Path.Path != dir {
		t.Fatal(p)
	}
	if _, err = plan.Apply(context.Background(), p, Local{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestMountSequenceDoesNotObserveHiddenDirectory(t *testing.T) {
	s := newConfigSequence()
	s.mounts = append(s.mounts, &plan.MountChange{Target: "/srv/data", State: "present", TargetPending: true, Identity: plan.DeviceIdentity{Fresh: true}})
	if missing, err := s.hidden("/srv/data/settings", "present"); err != nil || !missing {
		t.Fatal(missing, err)
	}
	if missing, err := s.hidden("/srv/database/settings", "present"); err != nil || missing {
		t.Fatal(missing, err)
	}
	s.mounts[0].Identity.Fresh = false
	if _, err := s.hidden("/srv/data/settings", "present"); err == nil {
		t.Fatal("inspected hidden directory")
	}
	s.mounts[0].TargetPending = false
	if missing, err := s.hidden("/srv/data/settings", "present"); err != nil || missing {
		t.Fatal(missing, err)
	}
	s.mounts[0].State = "absent"
	if _, err := s.hidden("/srv/data/settings", "absent"); err == nil {
		t.Fatal("allowed reversed filesystem content removal")
	}
}

func TestPreparationRejectsTargetAliasesAndHiddenPathAliases(t *testing.T) {
	e := Local{Mount: mount.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "readlink" {
			t.Fatalf("unexpected command %s", name)
		}
		path := args[len(args)-1]
		if strings.HasSuffix(path, "/settings") {
			t.Fatal("resolved hidden suffix before mounting")
		}
		path = strings.Replace(path, "/srv/alias", "/srv/data", 1)
		return []byte(path + "\n"), nil
	}}}
	services := []preparedService{{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "app"}}, files: []preparedFile{
		{component: "config/a", definition: &inventory.Config{Type: "mount", Target: "/srv/alias"}},
		{component: "config/b", definition: &inventory.Config{Type: "directory", Path: "/srv/data"}},
	}}}
	if err := e.checkConfigPaths(context.Background(), services); err == nil || !strings.Contains(err.Error(), "same path") {
		t.Fatal(err)
	}
	s := newConfigSequence()
	s.mounts = append(s.mounts, &plan.MountChange{Target: "/srv/data", State: "present", TargetPending: true})
	if _, err := e.hiddenConfigPath(context.Background(), s, "/srv/alias/settings", "present"); err == nil {
		t.Fatal("alias allowed hidden directory observation")
	}
	s.mounts[0].Identity.Fresh = true
	if missing, err := e.hiddenConfigPath(context.Background(), s, "/srv/alias/settings", "present"); err != nil || !missing {
		t.Fatal(missing, err)
	}
	services[0].files = []preparedFile{
		{component: "config/contents", file: &plan.FileChange{Path: "/srv/data/settings", State: "present"}},
		{component: "config/mount", definition: &inventory.Config{Type: "mount", Target: "/srv/alias"}},
	}
	if err := e.checkConfigPaths(context.Background(), services); err == nil || !strings.Contains(err.Error(), "precedes mount") {
		t.Fatal(err)
	}
	services[0].files[0], services[0].files[1] = services[0].files[1], services[0].files[0]
	if err := e.checkConfigPaths(context.Background(), services); err != nil {
		t.Fatal(err)
	}
}
