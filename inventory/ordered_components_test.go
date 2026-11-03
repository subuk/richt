package inventory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOrderedComponentsAndOptionalNames(t *testing.T) {
	in := fixture()
	s := Service{
		Binaries:  []Binary{{Name: "z-last", Type: "rpm", Package: "first"}, {Type: "rpm", Package: "second"}, {Type: "rpm", Package: "third"}, {Name: "a-first", Type: "rpm", Package: "fourth"}},
		Configs:   []Config{namedConfig("z-last", inlineConfig("/etc/first")), inlineConfig("/etc/second"), inlineConfig("/etc/third"), namedConfig("a-first", inlineConfig("/etc/fourth"))},
		Processes: []Process{{Name: "z-last", Type: "systemd", Unit: "first.service", RestartOn: []string{"binary.z-last", "config.a-first"}}, {Type: "systemd", Unit: "second.service"}, {Type: "systemd", Unit: "third.service"}, {Name: "a-first", Type: "systemd", Unit: "fourth.service"}},
	}
	setService(in, "app", s)
	out, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Hosts[0].Services[0].Definition
	for i := range s.Binaries {
		if got.Binaries[i].Package != s.Binaries[i].Package || got.Configs[i].Path != s.Configs[i].Path || got.Processes[i].Unit != s.Processes[i].Unit {
			t.Fatalf("declaration order changed: %#v", got)
		}
	}
	if got.Binaries[1].Name != "" || got.Configs[2].Name != "" || got.Processes[1].Name != "" {
		t.Fatal("unnamed components acquired labels")
	}
	if !reflect.DeepEqual(in.Services["app"].Service, s) {
		t.Fatal("normalization mutated source components")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"name":""`) {
		t.Fatalf("empty optional names serialized: %s", encoded)
	}
}

func TestComponentNameUniquenessAndExplicitReferences(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service Service
		want    string
	}{
		{"binary duplicate", Service{Binaries: []Binary{{Name: "same", Type: "rpm", Package: "a"}, {Name: "same", Type: "rpm", Package: "b"}}}, "duplicate name"},
		{"config duplicate", Service{Configs: []Config{namedConfig("same", inlineConfig("/etc/a")), namedConfig("same", inlineConfig("/etc/b"))}}, "duplicate name"},
		{"process duplicate", Service{Processes: []Process{{Name: "same", Type: "systemd", Unit: "a.service"}, {Name: "same", Type: "systemd", Unit: "b.service"}}}, "duplicate name"},
		{"package is not name", Service{Binaries: []Binary{{Type: "rpm", Package: "app"}}, Processes: []Process{{Type: "systemd", Unit: "app.service", RestartOn: []string{"binary.app"}}}}, "unknown component"},
		{"position is not name", Service{Configs: []Config{inlineConfig("/etc/app")}, Processes: []Process{{Type: "systemd", Unit: "app.service", ReloadOn: []string{"config.0"}}}}, "unknown component"},
		{"empty name is not reference", Service{Binaries: []Binary{{Type: "rpm", Package: "app"}}, Processes: []Process{{Type: "systemd", Unit: "app.service", RestartOn: []string{"binary."}}}}, "unknown component"},
		{"invalid config type", Service{Configs: []Config{{Type: "invalid", Path: "/etc/app"}}}, "type: expected template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateService(tc.service)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	// Labels need be unique only within their collection.
	c := namedConfig("same", inlineConfig("/etc/app"))
	c.Type = "template"
	if err := validateService(Service{Binaries: []Binary{{Name: "same", Type: "rpm", Package: "app"}}, Configs: []Config{c}, Processes: []Process{{Name: "same", Type: "systemd", Unit: "app.service", RestartOn: []string{"binary.same", "config.same"}}}}); err != nil {
		t.Fatal(err)
	}
}
