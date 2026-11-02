package inventory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func oneshotService(p Process) Service {
	return Service{
		Binaries:  []Binary{{Name: "package", Type: "rpm", Package: "app"}, {Type: "rpm", Package: "unnamed"}},
		Configs:   []Config{namedConfig("main", inlineConfig("/etc/app.conf")), inlineConfig("/etc/unnamed.conf")},
		Processes: []Process{p},
	}
}

func validOneshot() Process {
	return Process{Type: "oneshot", Command: []string{"app", "literal argument", ""}, RunOn: []string{"binary.package", "config.main"}}
}

func TestOneshotValidation(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name string
		edit func(*Process)
		want string
	}{
		{"missing command", func(p *Process) { p.Command = nil }, "command"},
		{"empty command", func(p *Process) { p.Command = []string{} }, "command"},
		{"empty executable", func(p *Process) { p.Command[0] = "" }, "command"},
		{"blank executable", func(p *Process) { p.Command[0] = " \t" }, "command"},
		{"option executable", func(p *Process) { p.Command[0] = "--help" }, "executable cannot begin with a hyphen"},
		{"NUL executable", func(p *Process) { p.Command[0] = "app\x00" }, "NUL"},
		{"NUL argument", func(p *Process) { p.Command[1] = "value\x00" }, "NUL"},
		{"missing references", func(p *Process) { p.RunOn = nil }, "run_on"},
		{"empty references", func(p *Process) { p.RunOn = []string{} }, "run_on"},
		{"duplicate reference", func(p *Process) { p.RunOn = []string{"config.main", "config.main"} }, "duplicate component"},
		{"unknown reference", func(p *Process) { p.RunOn = []string{"config.missing"} }, "unknown component"},
		{"process reference", func(p *Process) { p.RunOn = []string{"process.other"} }, "unknown component"},
		{"cross service reference", func(p *Process) { p.RunOn = []string{"other.config.main"} }, "unknown component"},
		{"unnamed package reference", func(p *Process) { p.RunOn = []string{"binary.unnamed"} }, "unknown component"},
		{"position reference", func(p *Process) { p.RunOn = []string{"config.1"} }, "unknown component"},
		{"empty name reference", func(p *Process) { p.RunOn = []string{"binary."} }, "unknown component"},
		{"zero timeout", func(p *Process) { p.Timeout = "0s" }, "positive duration"},
		{"negative timeout", func(p *Process) { p.Timeout = "-1s" }, "positive duration"},
		{"invalid timeout", func(p *Process) { p.Timeout = "later" }, "positive duration"},
		{"unit", func(p *Process) { p.Unit = "app.service" }, "oneshot cannot set"},
		{"state", func(p *Process) { p.State = "running" }, "oneshot cannot set"},
		{"enabled false", func(p *Process) { p.Enabled = &no }, "oneshot cannot set"},
		{"masked false", func(p *Process) { p.Masked = &no }, "oneshot cannot set"},
		{"restart", func(p *Process) { p.RestartOn = []string{} }, "oneshot cannot set"},
		{"reload", func(p *Process) { p.ReloadOn = []string{} }, "oneshot cannot set"},
		{"unit file", func(p *Process) { p.UnitFile = &Config{} }, "oneshot cannot set"},
		{"overrides", func(p *Process) { p.Overrides = map[string]Config{} }, "oneshot cannot set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := validOneshot()
			tc.edit(&p)
			err := validateService(oneshotService(p))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, name := range []string{"", "configure"} {
		p := validOneshot()
		p.Name, p.Timeout = name, "1m"
		if err := validateService(oneshotService(p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, executable := range []string{"./-name", "/tmp/-name"} {
		p := validOneshot()
		p.Command[0] = executable
		if err := validateService(oneshotService(p)); err != nil {
			t.Fatalf("explicit executable path %q: %v", executable, err)
		}
	}
	for _, field := range []string{"command", "run_on"} {
		p := Process{Type: "systemd", Unit: "app.service"}
		if field == "command" {
			p.Command = []string{}
		} else {
			p.RunOn = []string{}
		}
		if err := validateService(Service{Processes: []Process{p}}); err == nil || !strings.Contains(err.Error(), "systemd cannot set") {
			t.Fatalf("systemd accepted %s: %v", field, err)
		}
	}
}

func TestOneshotResolutionDefaultsAndOwnership(t *testing.T) {
	in := fixture()
	s := oneshotService(validOneshot())
	s.Processes = append(s.Processes, validOneshot(), Process{Name: "daemon", Type: "systemd", Unit: "app.service"})
	setService(in, "app", s)
	// Different oneshots never claim an empty systemd unit, even across services.
	setService(in, "repo", Service{Configs: []Config{namedConfig("main", inlineConfig("/etc/repo.conf"))}, Processes: []Process{{Type: "oneshot", Command: []string{"/bin/true"}, RunOn: []string{"config.main"}}}})
	out, err := in.Resolve(Selectors{Hosts: []string{"gena"}})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Hosts[0].Services[1].Definition
	for i, p := range got.Processes {
		if p.Timeout != "5s" || (p.Type == "oneshot" && p.State != "") || (p.Type == "systemd" && p.State != "running") {
			t.Fatalf("process %d defaults: %#v", i, p)
		}
	}
	if !reflect.DeepEqual(in.Services["app"].Service, s) {
		t.Fatal("normalization changed original process definitions")
	}
	encoded, err := json.Marshal(got.Processes[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"run_on":["binary.package","config.main"]`) || !strings.Contains(string(encoded), `"command":["app","literal argument",""]`) || strings.Contains(string(encoded), `"state"`) || strings.Contains(string(encoded), `"unit"`) {
		t.Fatalf("oneshot inspection: %s", encoded)
	}
}
