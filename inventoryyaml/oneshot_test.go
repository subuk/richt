package inventoryyaml

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const oneshotInputs = "binaries: [{name: package, type: rpm, package: app}]\nconfigs: [{name: main, path: /etc/app.conf, template: {file: missing-template}}]\n"

func TestOneshotYAMLNamedUnnamedAndLiteralArguments(t *testing.T) {
	service := oneshotInputs + `processes:
  - name: configure
    type: oneshot
    command: [app, "literal $HOME; argument", "", "true", "42"]
    run_on: [binary.package, config.main]
  - type: oneshot
    command: [/bin/true]
    run_on: [config.main]
    timeout: 2s
`
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprintf("file-%t", external), func(t *testing.T) {
			in, err := loadServiceFixture(t, service, external)
			if err != nil {
				t.Fatal(err)
			}
			ps := in.Services["app"].Processes
			if len(ps) != 2 || ps[0].Name != "configure" || ps[1].Name != "" || ps[0].State != "" || ps[1].Timeout != "2s" || !reflect.DeepEqual(ps[0].Command, []string{"app", "literal $HOME; argument", "", "true", "42"}) || !reflect.DeepEqual(ps[0].RunOn, []string{"binary.package", "config.main"}) {
				t.Fatalf("unexpected processes: %#v", ps)
			}
		})
	}
}

func TestOneshotYAMLStrictCommandAndReferences(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tc := range []struct{ name, fields, want string }{
			{"missing command", "run_on: [config.main]", "command"},
			{"empty command", "command: [], run_on: [config.main]", "command"},
			{"empty executable", "command: [''], run_on: [config.main]", "command"},
			{"option executable", "command: [--help], run_on: [config.main]", "executable cannot begin with a hyphen"},
			{"scalar command", "command: '/bin/true arg', run_on: [config.main]", "list of strings"},
			{"null command", "command: null, run_on: [config.main]", "list of strings"},
			{"numeric executable", "command: [42], run_on: [config.main]", "list of strings"},
			{"numeric arg", "command: [app, 42], run_on: [config.main]", "list of strings"},
			{"boolean arg", "command: [app, true], run_on: [config.main]", "list of strings"},
			{"null arg", "command: [app, null], run_on: [config.main]", "list of strings"},
			{"map arg", "command: [app, {}], run_on: [config.main]", "list of strings"},
			{"list arg", "command: [app, []], run_on: [config.main]", "list of strings"},
			{"NUL executable", `command: ["app\0"], run_on: [config.main]`, "NUL"},
			{"NUL argument", `command: [app, "value\0"], run_on: [config.main]`, "NUL"},
			{"missing references", "command: [app]", "run_on"},
			{"empty references", "command: [app], run_on: []", "run_on"},
			{"scalar references", "command: [app], run_on: config.main", "list of strings"},
			{"null references", "command: [app], run_on: null", "list of strings"},
			{"numeric reference", "command: [app], run_on: [42]", "list of strings"},
			{"duplicate reference", "command: [app], run_on: [config.main, config.main]", "duplicate component"},
			{"unknown reference", "command: [app], run_on: [config.missing]", "unknown component"},
			{"bad timeout", "command: [app], run_on: [config.main], timeout: 0s", "positive duration"},
		} {
			t.Run(fmt.Sprintf("file-%t/%s", external, tc.name), func(t *testing.T) {
				_, err := loadServiceFixture(t, oneshotInputs+"processes: [{type: oneshot, "+tc.fields+"}]\n", external)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			})
		}
	}
}

func TestProcessBackendFieldsRejectedByPresence(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, backend := range []struct {
			typ, fields string
			forbidden   []string
		}{
			{"oneshot", "command: [app], run_on: [config.main]", []string{"unit", "state", "enabled", "masked", "restart_on", "reload_on", "unit_file", "overrides"}},
			{"systemd", "unit: app.service", []string{"command", "run_on"}},
		} {
			for _, field := range backend.forbidden {
				for _, value := range []string{"null", "''", "[]", "{}", "false"} {
					for _, merged := range []bool{false, true} {
						t.Run(fmt.Sprintf("file-%t/%s/%s/%s/merge-%t", external, backend.typ, field, value, merged), func(t *testing.T) {
							extra := field + ": " + value
							if merged {
								extra = "<<: {" + extra + "}"
							}
							_, err := loadServiceFixture(t, oneshotInputs+"processes: [{type: "+backend.typ+", "+backend.fields+", "+extra+"}]\n", external)
							want := backend.typ + " process cannot set " + field
							if err == nil || !strings.Contains(err.Error(), want) {
								t.Fatalf("want %q, got %v", want, err)
							}
						})
					}
				}
			}
		}
	}
}
