package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

func tempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func inline(s string) *string { return &s }
func resolved(s inventory.Service) *inventory.Resolved {
	return &inventory.Resolved{Hosts: []inventory.ResolvedHost{{Name: "local", Transport: inventory.Transport{Type: "local"}, Services: []inventory.ResolvedService{{Assignment: inventory.Assignment{Name: "app", State: "present"}, Definition: s}}}}}
}
func fakeSystemd(active string, canReload bool) systemd.Backend {
	return systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=%s\nUnitFileState=disabled\nCanReload=%v\n", active, map[bool]string{true: "yes", false: "no"}[canReload])), nil
	}}
}
func TestRealInputsMergeAndProcessAfter(t *testing.T) {
	path := filepath.Join(tempDir(t), "app.conf")
	s := inventory.Service{Binaries: map[string]inventory.Binary{"app": {Type: "rpm", Package: "demo", Version: "0:2.0-1.noarch"}}, Configs: map[string]inventory.Config{"main": {Path: path, Template: inventory.Template{Inline: inline("new")}}}, Processes: map[string]inventory.Process{
		"z-first": {Unit: "first.service", RestartOn: []string{"binary.app"}, ReloadOn: []string{"config.main"}},
		"a-last":  {Unit: "last.service", After: []string{"z-first"}, ReloadOn: []string{"config.main"}},
	}}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0\n"), nil
		}
		return []byte("demo\t0:1.0-1.noarch\n"), nil
	}}, Systemd: fakeSystemd("active", true)}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	var order, reactions []string
	for _, task := range p.Hosts[0].Tasks {
		if task.Process != nil {
			order = append(order, task.Component)
			reactions = append(reactions, task.Process.Reaction)
		}
	}
	if strings.Join(order, ",") != "process/z-first,process/a-last" || strings.Join(reactions, ",") != "restart,reload" {
		t.Fatal(order, reactions)
	}
	// Once both resources match, declarations alone cause no reaction.
	if err = os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	e.RPM.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo\t0:2.0-1.noarch"), nil
	}
	p, err = e.Diff(context.Background(), resolved(s))
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal(p, err)
	}
}
func TestResumeKeepsPendingReactionAfterSuccessfulConfig(t *testing.T) {
	path := filepath.Join(tempDir(t), "app.conf")
	calls := 0
	fail := true
	s := inventory.Service{Configs: map[string]inventory.Config{"main": {Path: path, Template: inventory.Template{Inline: inline("frozen")}}}, Processes: map[string]inventory.Process{"main": {Unit: "demo.service", ReloadOn: []string{"config.main"}}}}
	e := Local{Systemd: systemd.Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=disabled\nCanReload=yes\n"), nil
		}
		if args[0] == "reload" {
			calls++
			if fail {
				return nil, errors.New("reload command failed")
			}
		}
		return nil, nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	saves := 0
	persist := func(*plan.Plan) error { saves++; return nil }
	if _, err = plan.Apply(context.Background(), p, e, persist); err == nil {
		t.Fatal("expected process failure")
	}
	if p.Hosts[0].Tasks[0].Progress.Status != "success" || p.Hosts[0].Tasks[1].Progress.Status != "error" {
		t.Fatal(p)
	}
	if err = os.WriteFile(path, []byte("changed after failure"), 0600); err != nil {
		t.Fatal(err)
	}
	fail = false
	if _, err = plan.Apply(context.Background(), p, e, persist); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "changed after failure" || calls != 2 || saves < 6 {
		t.Fatal(string(data), calls, saves)
	}
}
func TestMissingPackageUnitRequiresReloadProof(t *testing.T) {
	s := inventory.Service{Binaries: map[string]inventory.Binary{"app": {Type: "rpm", Package: "demo"}}, Configs: map[string]inventory.Config{"main": {Path: filepath.Join(tempDir(t), "c"), Template: inventory.Template{Inline: inline("x")}}}, Processes: map[string]inventory.Process{"main": {Unit: "new.service", ReloadOn: []string{"config.main"}}}}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return nil, rpm.ErrNotInstalled
	}}, Systemd: systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\nCanReload=no\n"), nil
	}}}
	if _, err := e.Diff(context.Background(), resolved(s)); err == nil || !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatal(err)
	}
	v := s.Processes["main"]
	v.Overrides = map[string]inventory.Config{"reload.conf": {Path: filepath.Join(tempDir(t), "reload.conf"), Template: inventory.Template{Inline: inline("[Service]\nExecReload=/bin/true\n")}}}
	s.Processes["main"] = v
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range p.Hosts[0].Tasks {
		found = found || task.Operation == "daemon-reload"
	}
	if !found {
		t.Fatal("missing daemon-reload")
	}
}
func TestEnablementHasIndependentProgressTask(t *testing.T) {
	yes := true
	s := inventory.Service{Processes: map[string]inventory.Process{"main": {Unit: "demo.service", Enabled: &yes}}}
	e := Local{Systemd: fakeSystemd("active", false)}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hosts[0].Tasks) != 1 || p.Hosts[0].Tasks[0].Operation != "systemd-enable" {
		t.Fatal(p)
	}
}
func TestPreparationBeforeObservationAndManagerAvailability(t *testing.T) {
	calls := 0
	e := Local{RPM: rpm.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("missing dnf")
	}}}
	s := inventory.Service{Binaries: map[string]inventory.Binary{"app": {Type: "rpm", Package: "demo"}}, Configs: map[string]inventory.Config{"bad": {Path: filepath.Join(tempDir(t), "c"), Template: inventory.Template{Inline: inline("{{.missing}}")}}}}
	if _, err := e.Diff(context.Background(), resolved(s)); err == nil || calls != 0 {
		t.Fatal(err, calls)
	}
	s.Configs = nil
	if _, err := e.Diff(context.Background(), resolved(s)); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
