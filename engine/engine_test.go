package engine

import (
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

func TestMixedHostsUseTargetCapabilitiesAndPersistConnections(t *testing.T) {
	calls := map[string][]string{}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls["local"] = append(calls["local"], name)
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return nil, nil
	}}, Connect: func(c inventory.Transport) (config.Target, error) {
		return config.Target{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls[c.Host] = append(calls[c.Host], name)
			if name == "dnf" {
				return []byte("4.20.0"), nil
			}
			return nil, nil
		}}, nil
	}}
	r := resolved(inventory.Service{Binaries: map[string]inventory.Binary{"main": {Package: "demo"}}})
	h := r.Hosts[0]
	h.Name = "remote"
	h.Transport = inventory.Transport{Type: "ssh", Host: "server-alias", User: "deploy", Port: 2222, Sudo: true}
	r.Hosts = append(r.Hosts, h)
	h.Name = "remote-two"
	h.Transport.Host = "second-alias"
	r.Hosts = append(r.Hosts, h)
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local", "server-alias", "second-alias"} {
		if strings.Join(calls[name], ",") != "dnf,rpm" {
			t.Fatal(name, calls)
		}
	}
	c := p.Hosts[1].Connection
	if c == nil || c.Host != "server-alias" || c.User != "deploy" || c.Port != 2222 || !c.Sudo || p.Hosts[1].Transport != "ssh" {
		t.Fatal(p.Hosts[1])
	}
	if p.Hosts[0].Connection != nil {
		t.Fatal("plain local has connection")
	}
}

func TestRemoteTemplatesRenderBeforeAnyTargetBinding(t *testing.T) {
	connects := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		connects++
		return config.Target{}, errors.New("unexpected target contact")
	}}
	r := resolved(inventory.Service{Configs: map[string]inventory.Config{"bad": {Path: "/etc/richt-test", Template: inventory.Template{Inline: inline("{{.missing}}")}}}})
	r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "alias"}
	if _, err := e.Diff(context.Background(), r); err == nil || !strings.Contains(err.Error(), "render template") || connects != 0 {
		t.Fatal(err, connects)
	}
}

func TestHostBindingAndResolutionDoNotContactTargets(t *testing.T) {
	calls := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		return config.Target{Run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("unexpected target command")
		}}, nil
	}}
	executor, err := e.ForHost(plan.Host{Name: "remote", Transport: "ssh", Connection: &plan.Connection{Host: "not-resolved-alias", Sudo: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Resolve(plan.Task{Operation: "file", File: &plan.FileChange{Path: "/etc/richt-test", State: "present", Owner: "target-only", Group: "target-only", SELinux: "system_u:object_r:etc_t:s0", Write: true}})
	if err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
}

func TestSecretFailureBeforeAnyHostContact(t *testing.T) {
	calls := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		calls++
		return config.Target{}, errors.New("unexpected contact")
	}}
	for _, source := range []string{`{{secret "missing"}}`, `{{secret 1}}`, `{{secret "missing" "other"}}`} {
		r := resolved(inventory.Service{Configs: map[string]inventory.Config{"main": {Path: "/etc/richt-test", Template: inventory.Template{Inline: inline(source)}}}})
		r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "example"}
		h := r.Hosts[0]
		h.Name = "second"
		r.Hosts = append(r.Hosts, h)
		if _, err := e.Diff(context.Background(), r); err == nil || calls != 0 {
			t.Fatal("secret preparation did not precede target binding", calls)
		}
	}
}

func TestSecretUnitParseFailureBeforeHostContact(t *testing.T) {
	source := filepath.Join(tempDir(t), "source.yaml")
	if err := os.WriteFile(source, []byte("unit: EXAMPLE_PRIVATE_UNIT_CONTENT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dropin := range []bool{false, true} {
		calls := 0
		c := inventory.Config{Path: "/etc/systemd/system/richt-secret-test.service", Template: inventory.Template{Inline: inline("[Service]\n{{secret \"unit\"}}\n")}}
		process := inventory.Process{Unit: "richt-secret-test.service", UnitFile: &c}
		if dropin {
			process.UnitFile = nil
			c.Path += ".d/20-example.conf"
			process.Overrides = map[string]inventory.Config{"20-example.conf": c}
		}
		r := resolved(inventory.Service{Processes: map[string]inventory.Process{"main": process}})
		r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: source}}
		r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "example"}
		e := Local{Connect: func(inventory.Transport) (config.Target, error) {
			calls++
			return config.Target{}, errors.New("unexpected contact")
		}}
		if _, err := e.Diff(context.Background(), r); err == nil || strings.Contains(err.Error(), "EXAMPLE_PRIVATE") || calls != 0 {
			t.Fatal("unsafe or late unit parsing failure")
		}
	}
}

func TestSecretSnapshotSharedByConfigsUnitsDropInsAndHosts(t *testing.T) {
	source := filepath.Join(tempDir(t), "source.yaml")
	if err := os.WriteFile(source, []byte("command: /bin/true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unit := inventory.Config{Path: "/etc/systemd/system/richt-secret-test.service", Template: inventory.Template{Inline: inline("[Service]\nExecReload={{secret \"command\"}}\n")}}
	r := resolved(inventory.Service{
		Configs:   map[string]inventory.Config{"main": {Path: "/tmp/richt-secret-test", Template: inventory.Template{Inline: inline(`{{secret "command"}}{{call .rotate}}`)}}},
		Processes: map[string]inventory.Process{"main": {Unit: "richt-secret-test.service", UnitFile: &unit, ReloadOn: []string{"config.main"}, Overrides: map[string]inventory.Config{"20-reload.conf": {Path: "/etc/systemd/system/richt-secret-test.service.d/20-reload.conf", Template: unit.Template}}}},
	})
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: source}}
	r.Hosts[0].Vars = map[string]any{"rotate": func() string {
		if err := os.WriteFile(source, []byte("command: /bin/false\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return ""
	}}
	h := r.Hosts[0]
	h.Name = "second"
	r.Hosts = append(r.Hosts, h)
	e := Local{Files: &config.Target{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("missing\n0\n0\n0\n-\n-\n\n"), nil
	}}, Systemd: fakeSystemd("active", true)}
	e.Systemd.Glob = func(context.Context, string) ([]string, error) { return nil, nil }
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range p.Hosts {
		files, restarts := 0, 0
		for _, task := range host.Tasks {
			if task.File != nil {
				files++
				if !task.File.Sensitive || !strings.Contains(task.File.Content, "/bin/true") || strings.Contains(task.File.Content, "/bin/false") {
					t.Fatal("secret snapshot was not shared")
				}
			}
			if task.Process != nil && task.Process.Reaction == "restart" {
				restarts++
			}
		}
		if files != 3 || restarts != 1 {
			t.Fatal("owned secret files did not trigger restart", files, restarts)
		}
	}
}

func TestSecretByteChangesTriggerRealReload(t *testing.T) {
	dir := tempDir(t)
	source, target := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("token: example-current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("example-current"), 0600); err != nil {
		t.Fatal(err)
	}
	r := resolved(inventory.Service{Configs: map[string]inventory.Config{"main": {Path: target, Template: inventory.Template{Inline: inline(`{{secret "token"}}`)}}}, Processes: map[string]inventory.Process{"main": {Unit: "richt-secret-test.service", ReloadOn: []string{"config.main"}}}})
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: source}}
	e := Local{Systemd: fakeSystemd("active", true)}
	p, err := e.Diff(context.Background(), r)
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal("unchanged secret caused work", err)
	}
	if err := os.WriteFile(source, []byte("token: example-next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err = e.Diff(context.Background(), r)
	if err != nil || len(p.Hosts[0].Tasks) != 2 || p.Hosts[0].Tasks[1].Process.Reaction != "reload" {
		t.Fatal("changed secret did not trigger reload", err)
	}
}
