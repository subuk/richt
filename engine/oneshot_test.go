package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/oneshot"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

func oneshotProcess(name string, refs ...string) inventory.Process {
	return inventory.Process{Name: name, Type: "oneshot", Command: []string{"tool", "literal argument"}, RunOn: refs}
}

func TestOneshotFreezesChangedTriggersCommandAndIndexedOrder(t *testing.T) {
	dir := tempDir(t)
	s := inventory.Service{
		Configs: []inventory.Config{
			{Name: "main", Path: filepath.Join(dir, "main"), Template: inventory.Template{Inline: inline("frozen")}},
			{Name: "same", Path: filepath.Join(dir, "same"), Template: inventory.Template{Inline: inline("same")}},
		},
		Processes: []inventory.Process{
			oneshotProcess("named", "config.same", "config.main"),
			{Type: "systemd", Unit: "demo.service", RestartOn: []string{"config.main"}},
			oneshotProcess("", "config.main"),
		},
	}
	if err := os.WriteFile(s.Configs[1].Path, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := Local{Systemd: fakeSystemd("active", true), Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { calls++; return nil }}}
	r := resolved(s)
	p, err := e.Diff(context.Background(), r)
	if err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	if expected := []string{"app/config/0", "app/process/0", "app/process/1", "app/process/2"}; !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatal(taskIDs(p))
	}
	for _, index := range []int{1, 3} {
		c := p.Hosts[0].Tasks[index].Oneshot
		if c == nil || c.Timeout != "5s" || !reflect.DeepEqual(c.Triggers, []string{"config.main"}) {
			t.Fatal(c)
		}
	}
	r.Hosts[0].Services[0].Definition.Processes[0].Command[0] = "mutated"
	r.Hosts[0].Services[0].Definition.Processes[0].RunOn[1] = "mutated"
	if p.Hosts[0].Tasks[1].Oneshot.Command[0] != "tool" || p.Hosts[0].Tasks[1].Oneshot.Triggers[0] != "config.main" {
		t.Fatal("plan retained inventory aliases")
	}
	path := filepath.Join(dir, "plan.json")
	if err := plan.Save(path, p); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var display bytes.Buffer
	if err := plan.Display(&display, store.Plan, e); err != nil || !strings.Contains(display.String(), "process/[3]") || !strings.Contains(display.String(), "exit status 0") {
		t.Fatal(display.String(), err)
	}
	store.Plan.Hosts[0].Tasks[1].Progress.Status = "success"
	store.Plan.Hosts[0].Tasks[3].Progress.Status = "success"
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil || calls != 0 {
		t.Fatal("completed named and unnamed oneshots were not skipped", err, calls)
	}
}

func TestOneshotNoChangeAndAbsentNeverObserveSystemdOrRun(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	s := inventory.Service{Configs: []inventory.Config{{Name: "main", Path: path, Template: inventory.Template{Inline: inline("same")}}}, Processes: []inventory.Process{oneshotProcess("", "config.main")}}
	e := Local{Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("observed systemd for oneshot")
		return nil, nil
	}}, Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { t.Fatal("ran during diff"); return nil }}}
	for _, absent := range []bool{false, true} {
		r := resolved(s)
		if absent {
			r.Hosts[0].Services[0].State = "absent"
			r.Hosts[0].Services[0].Definition.Configs[0].Template.Inline = inline("{{.missing}}")
		}
		p, err := e.Diff(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range p.Hosts[0].Tasks {
			if task.Operation != "file" || !absent {
				t.Fatal("unexpected task", task)
			}
		}
	}
}

func TestOneshotAttributeAndBinaryChangesTriggerOnceWithoutSystemd(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	s := inventory.Service{
		Binaries:  []inventory.Binary{{Name: "package", Package: "demo"}},
		Configs:   []inventory.Config{{Name: "main", Path: path, Mode: "0600", Template: inventory.Template{Inline: inline("same")}}},
		Processes: []inventory.Process{oneshotProcess("hook", "binary.package", "config.main")},
	}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return nil, rpm.ErrNotInstalled
	}}, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("systemd required by oneshot")
		return nil, nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(taskIDs(p), []string{"app/binary/0", "app/config/0", "app/process/0"}) || p.Hosts[0].Tasks[1].File.Write || !reflect.DeepEqual(p.Hosts[0].Tasks[2].Oneshot.Triggers, []string{"binary.package", "config.main"}) {
		t.Fatal(p.Hosts[0].Tasks)
	}
}

func TestOneshotContinuationUsesFrozenInputsAndBlocksLaterTasks(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "config")
	s := inventory.Service{Configs: []inventory.Config{{Name: "main", Path: path, Sensitive: true, Template: inventory.Template{Inline: inline("ONESHOT_PRIVATE_CANARY")}}}, Processes: []inventory.Process{oneshotProcess("first", "config.main"), oneshotProcess("second", "config.main")}}
	fail, calls := true, 0
	e := Local{Oneshot: oneshot.Backend{Run: func(_ context.Context, name string, args ...string) error {
		calls++
		if name != "tool" || !reflect.DeepEqual(args, []string{"literal argument"}) {
			t.Fatal("lost prepared argv", name, args)
		}
		if fail {
			return errors.New("ONESHOT_PRIVATE_CANARY")
		}
		return nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := plan.Save(planPath, p); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(planPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Apply(context.Background(), store.Plan, e, store.Save)
	if err == nil || calls != 1 || result.Hosts[0].Tasks[2].Status != "blocked" || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatal(result, err, calls)
	}
	var display bytes.Buffer
	if displayErr := plan.Display(&display, store.Plan, e); displayErr != nil || strings.Contains(display.String(), "PRIVATE_CANARY") {
		t.Fatal("sensitive bytes escaped display", displayErr)
	}
	if displayErr := plan.DisplayResult(&display, result); displayErr != nil || strings.Contains(display.String(), "PRIVATE_CANARY") {
		t.Fatal("sensitive bytes escaped result", displayErr)
	}
	if store.Plan.Hosts[0].Tasks[0].Progress.Status != "success" {
		t.Fatal("file did not succeed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external edit"), 0600); err != nil {
		t.Fatal(err)
	}
	fail = false
	store, err = plan.Open(planPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err = plan.Apply(context.Background(), store.Plan, e, store.Save)
	data, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || string(data) != "external edit" || calls != 3 || result.Hosts[0].Tasks[0].Status != "previously completed" {
		t.Fatal(result, err, readErr, string(data), calls)
	}
}

func TestInterruptedOneshotMayRepeatSideEffect(t *testing.T) {
	dir := tempDir(t)
	effects, saves := 0, 0
	e := Local{Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { effects++; return nil }}}
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/process/0", Operation: "oneshot", Oneshot: &plan.OneshotChange{Command: []string{"tool"}, Timeout: "5s", Triggers: []string{"config.main"}}, Progress: plan.Progress{Status: "pending"}}}}}}
	path := filepath.Join(dir, "plan.json")
	if err := plan.Save(path, p); err != nil {
		t.Fatal(err)
	}
	persist := func(p *plan.Plan) error {
		saves++
		if saves == 2 {
			return errors.New("interrupted after side effect, before completion persisted")
		}
		return plan.Save(path, p)
	}
	if _, err := plan.Apply(context.Background(), p, e, persist); err == nil || effects != 1 {
		t.Fatal(err, effects)
	}
	store, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Plan.Hosts[0].Tasks[0].Progress.Status != "running" {
		t.Fatal("interruption not retained")
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil || effects != 2 {
		t.Fatal(err, effects)
	}
}

func TestOneshotUsesBoundSSHAndSudoTarget(t *testing.T) {
	calls := 0
	e := Local{Connect: func(c inventory.Transport) (config.Target, error) {
		if c.Type != "ssh" || c.Host != "alias" || !c.Sudo {
			t.Fatal(c)
		}
		return config.Target{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if name != "tool" || !reflect.DeepEqual(args, []string{"literal argument"}) {
				t.Fatal(name, args)
			}
			return nil, nil
		}}, nil
	}}
	bound, err := e.ForHost(plan.Host{Name: "remote", Transport: "ssh", Connection: &plan.Connection{Host: "alias", Sudo: true}})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := bound.Resolve(plan.Task{Operation: "oneshot", Oneshot: &plan.OneshotChange{Command: []string{"tool", "literal argument"}, Timeout: "5s", Triggers: []string{"config.main"}}})
	if err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	if err := bound.Execute(context.Background(), actions[0]); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestAllResourceResolversRejectForeignOneshotPayload(t *testing.T) {
	c := &plan.OneshotChange{Command: []string{"tool"}, Timeout: "5s", Triggers: []string{"config.main"}}
	for _, operation := range []string{"file", "rpm", "systemd", "systemd-enable", "systemd-mask", "daemon-reload"} {
		if _, err := (Local{}).Resolve(plan.Task{Operation: operation, Oneshot: c}); err == nil {
			t.Fatal("accepted foreign oneshot payload", operation)
		}
	}
	if _, err := (Local{}).Resolve(plan.Task{Operation: "oneshot", Oneshot: c, Process: &plan.ProcessChange{}}); err == nil {
		t.Fatal("accepted foreign process payload")
	}
}
