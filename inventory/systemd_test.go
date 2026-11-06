package inventory

import (
	"strings"
	"testing"
)

func TestSystemdUnitNames(t *testing.T) {
	for _, unit := range []string{"app.service", "app.socket", "app@instance.socket", `app\x2dname.socket`} {
		if err := validateService(Service{Processes: []Process{{Type: "systemd", Unit: unit}}}); err != nil {
			t.Fatalf("valid unit %q: %v", unit, err)
		}
	}
	for _, unit := range []string{"app", "app.timer", "app.target", "app.path", "app.mount", "/app.socket", "app.socket/child", "app socket", ".socket"} {
		if err := validateService(Service{Processes: []Process{{Type: "systemd", Unit: unit}}}); err == nil || !strings.Contains(err.Error(), ".service or .socket") {
			t.Fatalf("invalid unit %q: %v", unit, err)
		}
	}
}

func TestSocketAndServiceHaveSeparateOwnership(t *testing.T) {
	in := fixture()
	setService(in, "repo", Service{Processes: []Process{{Type: "systemd", Unit: "app.socket"}}})
	setService(in, "app", Service{Processes: []Process{{Type: "systemd", Unit: "app.service"}}})
	if _, err := in.Resolve(Selectors{}); err != nil {
		t.Fatal(err)
	}
}
