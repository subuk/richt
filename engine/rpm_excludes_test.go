package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

func TestRPMDisableExcludesUsesLocalAndBoundSSHTarget(t *testing.T) {
	for _, manager := range []string{"dnf", "yum"} {
		for _, remote := range []bool{false, true} {
			for _, scope := range []string{"main", "all", "repo.id_1:stable"} {
				for _, request := range []struct{ version, state, command, target string }{
					{"", "present", "install-n", "demo"},
					{"1.34.3", "present", "install", "demo-1.34.3"},
					{"0:1.34.3-1.noarch", "present", "install-nevra", "demo-0:1.34.3-1.noarch"},
					{"1.34.3", "absent", "remove", "demo"},
				} {
					t.Run(fmt.Sprintf("%s/ssh=%v/%s/%s", manager, remote, scope, request.command), func(t *testing.T) {
						calls, connects := 0, 0
						runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
							calls++
							want := []string{"-y", "--disableexcludes=" + scope, request.command, "--", request.target}
							if name != manager || !reflect.DeepEqual(args, want) {
								t.Fatalf("transaction = %s %q, want %s %q", name, args, manager, want)
							}
							return nil, nil
						}
						localRunner := runner
						if remote {
							localRunner = func(context.Context, string, ...string) ([]byte, error) {
								t.Fatal("SSH transaction used the local RPM runner")
								return nil, errors.New("unexpected local execution")
							}
						}
						e := Local{RPM: rpm.Backend{Run: localRunner}, Connect: func(c inventory.Transport) (config.Target, error) {
							connects++
							if !remote || c.Type != "ssh" || c.Host != "alias" || !c.Sudo {
								t.Fatal("lost saved SSH/sudo connection", c)
							}
							return config.Target{Run: runner}, nil
						}}
						h := plan.Host{Name: "target", Transport: "local"}
						if remote {
							h.Transport, h.Connection = "ssh", &plan.Connection{Host: "alias", Sudo: true}
						}
						bound, err := e.ForHost(h)
						if err != nil {
							t.Fatal(err)
						}
						wantConnects := 0
						if remote {
							wantConnects = 1
						}
						if connects != wantConnects {
							t.Fatalf("target bindings = %d, want %d", connects, wantConnects)
						}
						task := plan.Task{Operation: "rpm", RPM: &plan.RPMChange{Manager: manager, Package: "demo", Version: request.version, DisableExcludes: scope, State: request.state}}
						actions, err := bound.Resolve(task)
						if err != nil || calls != 0 {
							t.Fatal(err, calls)
						}
						if !strings.Contains(strings.Join(actions[0].Description, "\n"), "--disableexcludes="+scope) {
							t.Fatal(actions[0].Description)
						}
						if err := bound.Execute(context.Background(), actions[0]); err != nil || calls != 1 {
							t.Fatal(err, calls)
						}
						if connects != wantConnects {
							t.Fatalf("execution rebound target: bindings = %d, want %d", connects, wantConnects)
						}
					})
				}
			}
		}
	}
}

func TestSavedRPMDisableExcludesFrozenContinuation(t *testing.T) {
	s := inventory.Service{Binaries: []inventory.Binary{
		{Name: "first", Package: "first", DisableExcludes: "repo-one"},
		{Name: "second", Package: "second", Version: "1.34.3", DisableExcludes: "all"},
	}}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" && reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("4.20.0"), nil
		}
		if name == "rpm" && len(args) > 0 && args[0] == "-qa" {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected repository contact during diff: %s %q", name, args)
	}}}
	r := resolved(s)
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tempDir(t), "excludes.json")
	if err := plan.Save(path, p); err != nil {
		t.Fatal(err)
	}
	// The executable request must survive source mutation and a failed apply.
	r.Hosts[0].Services[0].Definition.Binaries[0].DisableExcludes = "changed"
	r.Hosts[0].Services[0].Definition.Binaries[1].DisableExcludes = "invalid scope"
	store, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var display bytes.Buffer
	if err := plan.Display(&display, store.Plan, e); err != nil || !strings.Contains(display.String(), "--disableexcludes=repo-one") || !strings.Contains(display.String(), "--disableexcludes=all") {
		t.Fatal(display.String(), err)
	}
	var commands [][]string
	fail := true
	e.RPM.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string{name}, args...))
		if args[len(args)-1] == "second-1.34.3" && fail {
			return nil, errors.New("repository temporarily unavailable")
		}
		return nil, nil
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err == nil {
		t.Fatal("expected transaction failure")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Plan.Hosts[0].Tasks[0].RPM.DisableExcludes != "repo-one" || store.Plan.Hosts[0].Tasks[1].RPM.DisableExcludes != "all" {
		t.Fatal("saved overrides changed")
	}
	fail = false
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"dnf", "-y", "--disableexcludes=repo-one", "install-n", "--", "first"},
		{"dnf", "-y", "--disableexcludes=all", "install", "--", "second-1.34.3"},
		{"dnf", "-y", "--disableexcludes=all", "install", "--", "second-1.34.3"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("continuation commands = %q, want %q", commands, want)
	}
}

func TestRPMDisableExcludesAloneDoesNotTriggerReactions(t *testing.T) {
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo\t0:1.34.3-1.noarch\n"), nil
	}}, Systemd: fakeSystemd("active", true)}
	for _, scope := range []string{"", "main", "all", "repo"} {
		s := inventory.Service{Binaries: []inventory.Binary{{Name: "app", Package: "demo", Version: "1.34.3", DisableExcludes: scope}}, Processes: []inventory.Process{
			{Unit: "demo.service", RestartOn: []string{"binary.app"}, ReloadOn: []string{"binary.app"}},
			oneshotProcess("hook", "binary.app"),
		}}
		p, err := e.Diff(context.Background(), resolved(s))
		if err != nil || len(p.Hosts[0].Tasks) != 0 {
			t.Fatalf("scope=%q plan=%+v error=%v", scope, p, err)
		}
	}
}

func TestInvalidRPMDisableExcludesPrecedesHostContact(t *testing.T) {
	calls := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		calls++
		return config.Target{}, errors.New("unexpected host contact")
	}}
	r := resolved(inventory.Service{Binaries: []inventory.Binary{{Package: "demo", DisableExcludes: "repo --setopt=x"}}})
	r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "alias", Sudo: true}
	if _, err := e.Diff(context.Background(), r); err == nil || calls != 0 {
		t.Fatal("invalid override reached target binding", err, calls)
	}
}
