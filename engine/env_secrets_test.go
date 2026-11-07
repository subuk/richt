package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/systemd"
)

func TestEnvSecretSnapshotSharedByConfigsUnitsDropInsAndHosts(t *testing.T) {
	const token = "RICHT_ENGINE_ENV_SNAPSHOT_TOKEN"
	const command = "RICHT_ENGINE_ENV_SNAPSHOT_COMMAND"
	const dropin = "RICHT_ENGINE_ENV_SNAPSHOT_DROPIN"
	t.Setenv(token, "fictional-current")
	t.Setenv(command, "/bin/true")
	t.Setenv(dropin, "/bin/true")
	unit := inventory.Config{Path: "/etc/systemd/system/richt-env-secret-test.service", Template: inventory.Template{Inline: inline("[Service]\nExecReload={{secret \"command\"}}\n")}}
	override := inventory.Config{Path: "/etc/systemd/system/richt-env-secret-test.service.d/20-reload.conf", Template: inventory.Template{Inline: inline("[Service]\nExecReload={{secret \"dropin.command\"}}\n")}}
	r := resolved(inventory.Service{
		Configs: []inventory.Config{{Name: "main", Path: "/tmp/richt-env-secret-test", Template: inventory.Template{Inline: inline(`{{secret "config.token"}}{{call .rotate}}`)}}},
		Processes: []inventory.Process{{Name: "main", Unit: "richt-env-secret-test.service", UnitFile: &unit, ReloadOn: []string{"config.main"}, Overrides: map[string]inventory.Config{
			"20-reload.conf": override,
		}}},
	})
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "env", Env: map[string]string{"config.token": token, "command": command, "dropin.command": dropin}}}
	r.Hosts[0].Vars = map[string]any{"rotate": func() string {
		t.Setenv(token, "fictional-changed")
		t.Setenv(command, "/bin/false")
		t.Setenv(dropin, "/bin/false")
		return ""
	}}
	h := r.Hosts[0]
	h.Name = "second"
	r.Hosts = append(r.Hosts, h)
	e := Local{Files: &config.Target{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("-\n-\n"), nil
	}, RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
		return fileObservation("absent", ""), nil
	}}, Systemd: fakeSystemd("active", true)}
	e.Systemd.Glob = func(context.Context, string) ([]string, error) { return nil, nil }
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal("environment snapshot preparation failed")
	}
	if len(p.Hosts) != 2 {
		t.Fatal("expected both hosts in the environment snapshot test")
	}
	for _, host := range p.Hosts {
		files, restarts := 0, 0
		for _, task := range host.Tasks {
			if task.File != nil {
				files++
				want := "[Service]\nExecReload=/bin/true\n"
				if task.File.Path == "/tmp/richt-env-secret-test" {
					want = "fictional-current"
				}
				if !task.File.Sensitive || task.File.Content != want {
					t.Fatal("environment source snapshot was not shared across prepared files and hosts")
				}
			}
			if task.Process != nil && task.Process.Reaction == "restart" {
				restarts++
			}
		}
		if files != 3 || restarts != 1 {
			t.Fatal("owned environment secret files did not trigger restart")
		}
	}
}

func TestEnvSecretByteChangesTriggerRealReload(t *testing.T) {
	const variable = "RICHT_ENGINE_ENV_RELOAD_TOKEN"
	t.Setenv(variable, "fictional-current")
	target := filepath.Join(tempDir(t), "target")
	if err := os.WriteFile(target, []byte("fictional-current"), 0600); err != nil {
		t.Fatal("write fictional target fixture failed")
	}
	r := resolved(inventory.Service{
		Configs:   []inventory.Config{{Name: "main", Path: target, Template: inventory.Template{Inline: inline(`{{secret "token"}}`)}}},
		Processes: []inventory.Process{{Name: "main", Unit: "richt-env-secret-test.service", ReloadOn: []string{"config.main"}}},
	})
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "env", Env: map[string]string{"token": variable}}}
	e := Local{Systemd: fakeSystemd("active", true)}
	p, err := e.Diff(context.Background(), r)
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal("unchanged environment secret caused work")
	}
	t.Setenv(variable, "fictional-next\n")
	p, err = e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal("changed environment secret preparation failed")
	}
	if len(p.Hosts[0].Tasks) != 2 || p.Hosts[0].Tasks[0].File == nil || p.Hosts[0].Tasks[1].Process == nil || p.Hosts[0].Tasks[1].Process.Reaction != "reload" {
		t.Fatal("changed environment secret did not trigger reload")
	}
	file := p.Hosts[0].Tasks[0].File
	if !file.Sensitive || file.Content != "fictional-next\n" || !strings.Contains(file.Patch, "fictional-current") || !strings.Contains(file.Patch, "fictional-next") {
		t.Fatal("environment secret comparison did not retain real sensitive bytes")
	}
}

func TestMissingEnvSecretBeforeAnyHostContactOrObservation(t *testing.T) {
	const marker = "FICTIONAL_ENGINE_ENV_PRIVATE_MARKER"
	t.Setenv(marker+"_PRESENT", marker+"_VALUE")
	t.Setenv(marker+"_MISSING", "")
	if err := os.Unsetenv(marker + "_MISSING"); err != nil {
		t.Fatal("unset fictional environment fixture failed")
	}
	for _, kind := range []string{"config", "unit", "dropin"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			unexpected := func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return nil, errors.New("unexpected target observation")
			}
			e := Local{Connect: func(inventory.Transport) (config.Target, error) {
				calls++
				return config.Target{}, errors.New("unexpected target binding")
			}, Files: &config.Target{Run: unexpected, RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
				calls++
				return nil, errors.New("unexpected file observation")
			}}, Systemd: systemd.Backend{Run: unexpected}}
			// The first host has no secret calls, so this also verifies preparation
			// finishes across all hosts before any target observation starts.
			r := resolved(inventory.Service{Configs: []inventory.Config{{Path: "/tmp/richt-env-first-host", Template: inventory.Template{Inline: inline("fictional-first-host")}}}})
			r.SecretSources = map[string]inventory.SecretSource{marker: {Type: "env", Env: map[string]string{marker: marker + "_PRESENT", "unrequested": marker + "_MISSING"}}}
			content := `{{secret "` + marker + `" "` + marker + `"}}`
			c := inventory.Config{Path: "/tmp/richt-env-second-host", Template: inventory.Template{Inline: inline(content)}}
			s := inventory.Service{Configs: []inventory.Config{c}}
			if kind != "config" {
				c.Path = "/etc/systemd/system/richt-env-secret-test.service"
				c.Template.Inline = inline("[Service]\nExecReload=" + content + "\n")
				process := inventory.Process{Unit: "richt-env-secret-test.service", UnitFile: &c}
				if kind == "dropin" {
					process.UnitFile = nil
					c.Path += ".d/20-reload.conf"
					process.Overrides = map[string]inventory.Config{"20-reload.conf": c}
				}
				s = inventory.Service{Processes: []inventory.Process{process}}
			}
			second := resolved(s).Hosts[0]
			second.Name = "second"
			second.Transport = inventory.Transport{Type: "ssh", Host: "example"}
			r.Hosts = append(r.Hosts, second)
			if _, err := e.Diff(context.Background(), r); err == nil || strings.Contains(err.Error(), marker) || calls != 0 {
				t.Fatal("environment source failure was unsafe or followed host contact")
			}
		})
	}
}
