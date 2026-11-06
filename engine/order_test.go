package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

// Each action represents one validated task so its identity is observable during
// execution without changing the host resources used to prepare it.
type orderedExecutor struct {
	executed []string
	fail     string
}

func (e *orderedExecutor) Resolve(t plan.Task) ([]plan.Action, error) {
	if _, err := (Local{}).Resolve(t); err != nil {
		return nil, err
	}
	return []plan.Action{{Kind: t.ID}}, nil
}
func (e *orderedExecutor) Execute(_ context.Context, a plan.Action) error {
	e.executed = append(e.executed, a.Kind)
	if a.Kind == e.fail {
		return errors.New("deliberate task failure")
	}
	return nil
}
func taskIDs(p *plan.Plan) []string {
	var ids []string
	for _, t := range p.Hosts[0].Tasks {
		ids = append(ids, t.ID)
	}
	return ids
}
func TestListsFreezeDeclaredOrderIdentityAndContinuation(t *testing.T) {
	dir := tempDir(t)
	yes := true
	s := inventory.Service{}
	// Numeric labels and unnamed items both use position-based internal IDs.
	names := []string{"", "z-last-alphabetically", "0", "", "a-first-alphabetically"}
	for i, name := range names {
		s.Binaries = append(s.Binaries, inventory.Binary{Name: name, Type: "rpm", Package: fmt.Sprintf("demo-%d", i)})
		s.Configs = append(s.Configs, inventory.Config{Name: name, Path: filepath.Join(dir, fmt.Sprint(i)), Template: inventory.Template{Inline: inline("frozen")}})
		s.Processes = append(s.Processes, inventory.Process{Name: name, Unit: fmt.Sprintf("demo-%d.service", i), Enabled: &yes})
	}
	// Referring to the last binary/config cannot move the first process.
	s.Processes[0].RestartOn = []string{"binary.a-first-alphabetically", "config.a-first-alphabetically"}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if command == "dnf" {
			return []byte("4.20.0"), nil
		}
		return nil, rpm.ErrNotInstalled
	}}, Systemd: fakeSystemd("inactive", true)}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, category := range []string{"binary", "config"} {
		for i := range names {
			expected = append(expected, fmt.Sprintf("app/%s/%d", category, i))
		}
	}
	expected = append(expected, "app/systemd/daemon-reload")
	for i := range names {
		expected = append(expected, fmt.Sprintf("app/process/%d/enabled", i), fmt.Sprintf("app/process/%d", i))
	}
	if !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatalf("tasks: %v; expected %v", taskIDs(p), expected)
	}
	if p.Hosts[0].Tasks[12].Process.Reaction != "restart" {
		t.Fatal("reference reaction was lost")
	}
	var display bytes.Buffer
	if err := plan.Display(&display, p, e); err != nil {
		t.Fatal(err)
	}
	remaining := display.String()
	for _, task := range p.Hosts[0].Tasks {
		marker := "  " + task.Service + "/" + task.Component + " [pending]"
		position := strings.Index(remaining, marker)
		if position < 0 {
			t.Fatalf("missing ordered label %s in %s", marker, remaining)
		}
		remaining = remaining[position+len(marker):]
	}
	if p.Hosts[0].Tasks[0].Component != "binary/[1]" || p.Hosts[0].Tasks[2].Component != "binary/0" || p.Hosts[0].Tasks[3].Component != "binary/[4]" {
		t.Fatal("unexpected named/unnamed labels")
	}
	filename := filepath.Join(dir, "plan.json")
	if err := plan.Save(filename, p); err != nil {
		t.Fatal(err)
	}
	// A changed inventory cannot affect persisted identities or task order.
	s.Binaries = nil
	s.Configs = nil
	s.Processes = nil
	store, err := plan.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(taskIDs(store.Plan), expected) {
		t.Fatal("saved identity/order changed")
	}
	executor := &orderedExecutor{fail: "app/config/1"}
	result, err := plan.Apply(context.Background(), store.Plan, executor, store.Save)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !reflect.DeepEqual(executor.executed, expected[:7]) {
		t.Fatal(executor.executed)
	}
	for _, r := range result.Hosts[0].Tasks[7:] {
		if r.Status != "blocked" {
			t.Fatal("failure did not block remaining host tasks", r)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = plan.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executor = &orderedExecutor{}
	if _, err := plan.Apply(context.Background(), store.Plan, executor, store.Save); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.executed, expected[6:]) {
		t.Fatal("continuation reordered or replayed successful tasks", executor.executed)
	}
	if !reflect.DeepEqual(taskIDs(store.Plan), expected) {
		t.Fatal("progress changed task identities")
	}
}

func TestAbsentReversesEachListAndKeepsStopBeforeDisable(t *testing.T) {
	dir := tempDir(t)
	s := inventory.Service{}
	for i, name := range []string{"z-last-alphabetically", "", "a-first-alphabetically"} {
		path := filepath.Join(dir, fmt.Sprint(i))
		if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		s.Binaries = append(s.Binaries, inventory.Binary{Name: name, Package: fmt.Sprintf("demo-%d", i)})
		s.Configs = append(s.Configs, inventory.Config{Name: name, Path: path, Template: inventory.Template{Inline: inline("unused")}})
		s.Processes = append(s.Processes, inventory.Process{Name: name, Unit: fmt.Sprintf("demo-%d.service", i)})
	}
	r := resolved(s)
	r.Hosts[0].Services[0].State = "absent"
	// A later service still follows the first service's removal work.
	laterPath := filepath.Join(dir, "later")
	if err := os.WriteFile(laterPath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	later := inventory.ResolvedService{Assignment: inventory.Assignment{Name: "later", State: "absent"}, Definition: inventory.Service{Configs: []inventory.Config{{Path: laterPath, Template: inventory.Template{Inline: inline("unused")}}}}}
	r.Hosts[0].Services = append(r.Hosts[0].Services, later)
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo-0\t0:1-1.noarch\ndemo-1\t0:1-1.noarch\ndemo-2\t0:1-1.noarch"), nil
	}}, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=enabled\n"), nil
	}}}
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, service := range []string{"app"} {
		for i := 2; i >= 0; i-- {
			expected = append(expected, fmt.Sprintf("%s/process/%d", service, i), fmt.Sprintf("%s/process/%d/enabled", service, i))
		}
		for _, category := range []string{"config", "binary"} {
			for i := 2; i >= 0; i-- {
				expected = append(expected, fmt.Sprintf("%s/%s/%d", service, category, i))
			}
		}
		expected = append(expected, service+"/systemd/daemon-reload")
	}
	expected = append(expected, "later/config/0")
	if !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatalf("absence tasks %v; expected %v", taskIDs(p), expected)
	}
	executor := &orderedExecutor{}
	if _, err := plan.Apply(context.Background(), p, executor, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.executed, expected) {
		t.Fatal("execution changed prepared order", executor.executed)
	}
}

func TestUnnamedProcessesKeepOwnedAndOrdinaryUnitAssociations(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(fmt.Sprint(ordinary), func(t *testing.T) {
			s := inventory.Service{}
			for i := 0; i < 2; i++ {
				unit := fmt.Sprintf("richt-order-%d.service", i)
				content := "[Service]\nExecStart=/bin/true\n"
				if i == 0 {
					content += "ExecReload=/bin/true\n"
				}
				definition := inventory.Config{Path: "/etc/systemd/system/" + unit, Template: inventory.Template{Inline: inline(content)}}
				process := inventory.Process{Unit: unit, UnitFile: &definition}
				if ordinary {
					process.UnitFile = nil
					s.Configs = append(s.Configs, definition)
				}
				// Only the first process needs its own prepared reload capability.
				if i == 0 {
					process.ReloadOn = []string{"config.trigger"}
				}
				s.Processes = append(s.Processes, process)
			}
			s.Configs = append(s.Configs, inventory.Config{Name: "trigger", Path: "/tmp/richt-order-trigger", Template: inventory.Template{Inline: inline("changed")}})
			target := config.Target{Run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("-\n-\n"), nil
			}, RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
				return fileObservation("absent", ""), nil
			}}
			e := Local{Files: &target, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=\nCanReload=no\n"), nil
			}}}
			p, err := e.Diff(context.Background(), resolved(s))
			if err != nil {
				t.Fatal(err)
			}
			var processes []plan.Task
			for _, task := range p.Hosts[0].Tasks {
				if task.Operation == "systemd" {
					processes = append(processes, task)
				}
			}
			if len(processes) != 2 {
				t.Fatal("unnamed process association lost", processes)
			}
			for i, task := range processes {
				if task.ID != fmt.Sprintf("app/process/%d", i) || task.Process.Reaction != "restart" {
					t.Fatal(task)
				}
			}
			// The second unit's absent ExecReload must not inherit the first one's capability.
			s.Processes[0].ReloadOn = nil
			s.Processes[1].ReloadOn = []string{"config.trigger"}
			if _, err := e.Diff(context.Background(), resolved(s)); err == nil || !strings.Contains(err.Error(), "cannot be verified") {
				t.Fatal("unnamed unit reload capabilities were conflated", err)
			}
		})
	}
}

func TestMultipleUnmasksPreserveProcessOrderAroundFilePhase(t *testing.T) {
	no, yes := false, true
	s := inventory.Service{}
	for i, name := range []string{"z", "", "a"} {
		unit := fmt.Sprintf("richt-unmask-order-%d.service", i)
		definition := inventory.Config{Path: "/etc/systemd/system/" + unit, Template: inventory.Template{Inline: inline("[Service]\nExecStart=/bin/true\n")}}
		s.Processes = append(s.Processes, inventory.Process{Name: name, Unit: unit, Masked: &no, Enabled: &yes, UnitFile: &definition})
	}
	target := config.Target{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	e := Local{Files: &target, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=masked\nActiveState=inactive\nUnitFileState=masked\n"), nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"app/process/0/masked", "app/process/1/masked", "app/process/2/masked", "app/process/0/unit", "app/process/1/unit", "app/process/2/unit", "app/systemd/daemon-reload", "app/process/0/enabled", "app/process/0", "app/process/1/enabled", "app/process/1", "app/process/2/enabled", "app/process/2"}
	if !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatalf("tasks: %v; expected %v", taskIDs(p), expected)
	}
	executor := &orderedExecutor{}
	if _, err := plan.Apply(context.Background(), p, executor, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.executed, expected) {
		t.Fatal(executor.executed)
	}
}

func TestUnchangedEntriesDoNotRenumberTaskIdentities(t *testing.T) {
	dir := tempDir(t)
	s := inventory.Service{}
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprint(i))
		if i != 1 {
			if err := os.WriteFile(path, []byte("matching"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		s.Binaries = append(s.Binaries, inventory.Binary{Package: fmt.Sprintf("demo-%d", i)})
		s.Configs = append(s.Configs, inventory.Config{Path: path, Template: inventory.Template{Inline: inline("matching")}})
		s.Processes = append(s.Processes, inventory.Process{Unit: fmt.Sprintf("demo-%d.service", i)})
	}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if command == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo-0\t0:1-1.noarch\ndemo-2\t0:1-1.noarch"), nil
	}}, Systemd: systemd.Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		active := "active"
		if args[len(args)-1] == "demo-1.service" {
			active = "inactive"
		}
		return []byte("LoadState=loaded\nActiveState=" + active + "\nUnitFileState=disabled\n"), nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"app/binary/1", "app/config/1", "app/systemd/daemon-reload", "app/process/1"}
	if !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatal("unchanged entries changed original positions", taskIDs(p))
	}
}
