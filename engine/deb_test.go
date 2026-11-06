package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/apt"
	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/oneshot"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

// This fake accepts only capability checks and installed-database observation
// during diff. Any repository query or update is an unexpected command.
func debRunner(t *testing.T, installed string, apply func(*plan.DEBChange) error) apt.Runner {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-get":
			if reflect.DeepEqual(args, []string{"--version"}) {
				return []byte("apt 2.8.3 (amd64)\n"), nil
			}
		case "dpkg":
			if reflect.DeepEqual(args, []string{"--print-architecture"}) {
				return []byte("amd64\n"), nil
			}
		case "dpkg-query":
			if reflect.DeepEqual(args, []string{"--version"}) {
				return []byte("Debian dpkg-query version 1.22.6\n"), nil
			}
			if len(args) == 2 && args[0] == "--show" && strings.HasPrefix(args[1], "--showformat=") {
				return []byte(installed), nil
			}
		case "python3":
			if len(args) != 4 || args[0] != "-I" || args[1] != "-c" {
				t.Fatalf("unexpected APT helper arguments: %q", args)
			}
			var request struct {
				Op     string          `json:"op"`
				Change *plan.DEBChange `json:"change"`
			}
			if err := json.Unmarshal([]byte(args[3]), &request); err != nil {
				t.Fatal(err)
			}
			if request.Op == "check" {
				return []byte(`{}`), nil
			}
			if request.Op == "apply" && apply != nil {
				if err := apply(request.Change); err != nil {
					return nil, err
				}
				return []byte(`{}`), nil
			}
		}
		return nil, fmt.Errorf("unexpected APT command or repository activity: %s %q", name, args)
	}
}

func TestDEBDiffReadsInstalledStateWithoutRepositoryActivityOrRPM(t *testing.T) {
	for _, test := range []struct {
		name, installed, state, version string
		changed                         bool
	}{
		{name: "missing", changed: true},
		{name: "unversioned preserves installed", installed: "demo\t1.0-1\tii \tamd64\n"},
		{name: "exact matches", installed: "demo\t1:2.0-3\tii \tamd64\n", version: "1:2.0-3"},
		{name: "absent missing", state: "absent"},
		{name: "remove installed", installed: "demo\t1.0-1\tii \tamd64\n", state: "absent", changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := Local{APT: apt.Backend{Run: debRunner(t, test.installed, nil)}, RPM: rpm.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("DEB-only service required RPM")
				return nil, nil
			}}}
			r := resolved(inventory.Service{Binaries: []inventory.Binary{{Name: "app", Type: "deb", Package: "demo", Version: test.version}}})
			if test.state != "" {
				r.Hosts[0].Services[0].State = test.state
			}
			p, err := e.Diff(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if (len(p.Hosts[0].Tasks) == 1) != test.changed {
				t.Fatalf("unexpected tasks: %+v", p.Hosts[0].Tasks)
			}
			if test.changed && (p.Hosts[0].Tasks[0].Operation != "deb" || p.Hosts[0].Tasks[0].DEB == nil || p.Hosts[0].Tasks[0].RPM != nil) {
				t.Fatal("DEB lost its independent operation")
			}
		})
	}
}

func TestMixedPackageAndArtifactBinariesPreserveLifecycleOrder(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprint(absent), func(t *testing.T) {
			service := inventory.Service{Binaries: []inventory.Binary{{Type: "deb", Package: "demo"}, artifactBinary("downloaded"), {Type: "rpm", Package: "rpm-demo"}, {Type: "deb", Package: "second"}}}
			installed := ""
			if absent {
				installed = "demo\t1.0-1\tii \tamd64\nsecond\t1.0-1\tii \tamd64\n"
			}
			e := Local{APT: apt.Backend{Run: debRunner(t, installed, nil)}, Artifacts: artifactFake(t, true, nil), Mount: pathFake(), RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "dnf" && reflect.DeepEqual(args, []string{"--version"}) {
					return []byte("4.20.0"), nil
				}
				if name == "rpm" {
					if absent {
						return []byte("rpm-demo\t0:1-1.noarch\n"), nil
					}
					return nil, nil
				}
				return nil, errors.New("unexpected RPM command")
			}}}
			r := resolved(service)
			if absent {
				r.Hosts[0].Services[0].State = "absent"
			}
			p, err := e.Diff(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"app/binary/0", "app/binary/1", "app/binary/2", "app/binary/3"}
			if absent {
				want = []string{"app/binary/3", "app/binary/2", "app/binary/1", "app/binary/0"}
			}
			if !reflect.DeepEqual(taskIDs(p), want) {
				t.Fatal(taskIDs(p))
			}
			executor := &orderedExecutor{}
			if _, err := plan.Apply(context.Background(), p, executor, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(executor.executed, want) {
				t.Fatal(executor.executed)
			}
		})
	}
}

func TestDEBInstallationSuppliesMissingUnitAndTriggersReactions(t *testing.T) {
	yes := true
	service := inventory.Service{Binaries: []inventory.Binary{{Name: "package", Type: "deb", Package: "demo"}}, Processes: []inventory.Process{
		{Name: "daemon", Type: "systemd", Unit: "demo.service", Enabled: &yes, RestartOn: []string{"binary.package"}},
		oneshotProcess("notify", "binary.package"),
	}}
	e := Local{APT: apt.Backend{Run: debRunner(t, "", nil)}, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=disabled\nCanReload=no\n"), nil
	}}}
	p, err := e.Diff(context.Background(), resolved(service))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app/binary/0", "app/systemd/daemon-reload", "app/process/0/enabled", "app/process/0", "app/process/1"}
	if !reflect.DeepEqual(taskIDs(p), want) || p.Hosts[0].Tasks[3].Process.Reaction != "restart" {
		t.Fatal(p.Hosts[0].Tasks)
	}
	service.Processes[0].ReloadOn = []string{"binary.package"}
	if _, err := e.Diff(context.Background(), resolved(service)); err == nil || !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("unverified reload capability accepted: %v", err)
	}
	service.Processes[0].RestartOn = nil
	e.Systemd = fakeSystemd("active", true)
	p, err = e.Diff(context.Background(), resolved(service))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range p.Hosts[0].Tasks {
		if task.Operation == "systemd" && task.Process.Reaction != "reload" {
			t.Fatal("DEB change lost verified reload reaction", task)
		}
	}
}

func TestDEBSavedRequestContinuationSkipsSuccessfulPackages(t *testing.T) {
	service := inventory.Service{Binaries: []inventory.Binary{{Name: "first", Type: "deb", Package: "first", Version: "1:2.0-3"}, {Name: "second", Type: "deb", Package: "second"}}, Processes: []inventory.Process{oneshotProcess("notify", "binary.first", "binary.second")}}
	var calls []string
	fail := true
	e := Local{APT: apt.Backend{Run: debRunner(t, "", func(c *plan.DEBChange) error {
		calls = append(calls, c.Package)
		if c.Package == "first" && c.Version != "1:2.0-3" {
			t.Fatal("frozen version changed")
		}
		if c.Package == "second" && fail {
			return errors.New("simulated package failure")
		}
		return nil
	})}, Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { calls = append(calls, "reaction"); return nil }}}
	p, err := e.Diff(context.Background(), resolved(service))
	if err != nil {
		t.Fatal(err)
	}
	var display bytes.Buffer
	if err := plan.Display(&display, p, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(display.String(), "refresh APT indexes") || !strings.Contains(display.String(), "first=1:2.0-3") {
		t.Fatal(display.String())
	}
	filename := filepath.Join(tempDir(t), "plan.json")
	if err := plan.Save(filename, p); err != nil {
		t.Fatal(err)
	}
	service.Binaries = nil
	store, err := plan.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err == nil {
		t.Fatal("expected package failure")
	}
	if !reflect.DeepEqual(calls, []string{"first", "second"}) || store.Plan.Hosts[0].Tasks[0].Progress.Status != "success" || store.Plan.Hosts[0].Tasks[2].Progress.Status != "pending" {
		t.Fatal(calls, store.Plan.Hosts[0].Tasks)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = plan.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fail = false
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second", "second", "reaction"}) {
		t.Fatal("continuation replayed success or skipped unfinished work", calls)
	}
}

func TestDEBHostBindingAndMalformedPayloads(t *testing.T) {
	c, err := apt.Prepare(inventory.Binary{Type: "deb", Package: "demo"}, "present")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		return config.Target{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++
			return debRunner(t, "", func(*plan.DEBChange) error { return nil })(context.Background(), name, args...)
		}}, nil
	}}
	executor, err := e.ForHost(plan.Host{Name: "remote", Transport: "ssh", Connection: &plan.Connection{Host: "alias", Sudo: true}})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := executor.Resolve(plan.Task{Operation: "deb", DEB: c})
	if err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	if err := executor.Execute(context.Background(), actions[0]); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	for _, op := range []string{"file", "rpm", "artifact", "path", "storage", "mount", "account", "oneshot", "systemd", "systemd-enable", "systemd-mask", "daemon-reload"} {
		if _, err := (Local{}).Resolve(plan.Task{Operation: op, DEB: c}); err == nil {
			t.Fatal("accepted foreign DEB payload", op)
		}
	}
	for _, task := range []plan.Task{{Operation: "deb"}, {Operation: "deb", DEB: c, RPM: &plan.RPMChange{}}, {Operation: "deb", DEB: c, File: &plan.FileChange{}}, {Operation: "deb", RPM: &plan.RPMChange{}}} {
		if _, err := (Local{}).Resolve(task); err == nil {
			t.Fatal("accepted malformed DEB task", task)
		}
	}
}

func TestDEBManagerChecksRunOnceBeforeInstalledObservation(t *testing.T) {
	checks := 0
	base := debRunner(t, "", nil)
	e := Local{APT: apt.Backend{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" {
			checks++
		}
		if name == "dpkg" && checks != 1 {
			t.Fatal("installed observation preceded capability checks")
		}
		return base(ctx, name, args...)
	}}}
	r := resolved(inventory.Service{Binaries: []inventory.Binary{{Type: "deb", Package: "demo"}, {Type: "deb", Package: "second"}}})
	r.Hosts[0].Services = append(r.Hosts[0].Services, inventory.ResolvedService{Assignment: inventory.Assignment{Name: "later", State: "present"}, Definition: inventory.Service{Binaries: []inventory.Binary{{Type: "deb", Package: "third"}}}})
	if _, err := e.Diff(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Fatalf("APT capability checks=%d", checks)
	}
	// Unused APT is never checked for a process-only service.
	e.APT.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unused APT capability contacted")
		return nil, nil
	}
	e.Systemd = fakeSystemd("active", true)
	if _, err := e.Diff(context.Background(), resolved(inventory.Service{Processes: []inventory.Process{{Type: "systemd", Unit: "demo.service"}}})); err != nil {
		t.Fatal(err)
	}
}
