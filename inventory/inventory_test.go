package inventory

import (
	"reflect"
	"strings"
	"testing"
)

func inlineConfig(p string) Config {
	s := "example"
	return Config{Path: p, Template: Template{Inline: &s}}
}

func fixture() *Inventory {
	return &Inventory{
		Tags:     map[string]Tag{"common": {Weight: 10, Vars: map[string]any{"app": map[string]any{"port": 8080, "name": "default"}, "list": []any{"old"}}}, "home": {Weight: 20, Vars: map[string]any{"app": map[string]any{"name": "Home"}, "list": []any{"new"}}}, "net": {Weight: 20, Vars: map[string]any{"app": map[string]any{"name": "Net"}}}},
		Services: map[string]Service{"app": {Configs: map[string]Config{"main": inlineConfig("/etc/app.conf")}}, "repo": {Configs: map[string]Config{"repo": inlineConfig("/etc/yum.repos.d/app.repo")}}},
		Hosts:    []Host{{Name: "gena", Tags: []string{"home", "common"}, Transport: Transport{Type: "local"}, Services: []Assignment{{Name: "app", Order: 20}, {Name: "repo", Order: 10}}}, {Name: "lanst", Tags: []string{"common", "net"}, Transport: Transport{Type: "ssh", Host: "lanst.example", Sudo: true}, Services: []Assignment{{Name: "repo", Order: 10}, {Name: "app", Order: 20, State: "absent"}}}},
	}
}

func TestResolvedVariablesAndOrder(t *testing.T) {
	in := fixture()
	out, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"Home", "Net"} {
		h := out.Hosts[i]
		app := h.Vars["app"].(map[string]any)
		if app["name"] != want || app["port"] != 8080 {
			t.Fatalf("host %s vars: %#v", h.Name, h.Vars)
		}
		if h.Services[0].Name != "repo" || h.Services[1].Name != "app" {
			t.Fatalf("order: %#v", h.Services)
		}
	}
	if out.Hosts[0].Services[1].State != "present" || out.Hosts[1].Services[1].State != "absent" {
		t.Fatal("assignment state defaults")
	}
	if !reflect.DeepEqual(out.Hosts[0].Vars["list"], []any{"new"}) {
		t.Fatal("list concatenated")
	}
	out.Hosts[0].Vars["app"].(map[string]any)["name"] = "mutated"
	again, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Hosts[0].Vars["app"].(map[string]any)["name"] != "Home" {
		t.Fatal("resolution mutated source vars")
	}
}

func TestEqualWeightConflictsBeforeOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
	}{{"scalar", 1, 2}, {"list", []any{1}, []any{2}}, {"map-scalar", map[string]any{"x": 1}, "value"}, {"null", nil, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			tags := map[string]Tag{"a": {Weight: 10, Vars: map[string]any{"nested": map[string]any{"value": tc.a}}}, "b": {Weight: 10, Vars: map[string]any{"nested": map[string]any{"value": tc.b}}}, "later": {Weight: 20, Vars: map[string]any{"nested": "overridden"}}}
			for _, names := range [][]string{{"a", "b", "later"}, {"later", "b", "a"}} {
				_, err := MergeTags("gena", names, tags)
				if err == nil {
					t.Fatal("expected conflict")
				}
				for _, part := range []string{"gena", "vars.nested.value", `"a"`, `"b"`, "weight 10"} {
					if !strings.Contains(err.Error(), part) {
						t.Fatalf("missing %s: %v", part, err)
					}
				}
			}
		})
	}
}

func TestMergeCompatibleGroupsAndReplacement(t *testing.T) {
	tags := map[string]Tag{
		"a": {Weight: 10, Vars: map[string]any{"nested": map[string]any{"same": []any{1}, "left": 1}, "replace": map[string]any{"old": 1}}},
		"b": {Weight: 10, Vars: map[string]any{"nested": map[string]any{"same": []any{1}, "right": 2}}},
		"c": {Weight: 20, Vars: map[string]any{"replace": []any{3}, "nested": map[string]any{"left": 9}}},
	}
	a, err := MergeTags("h", []string{"a", "b", "c"}, tags)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MergeTags("h", []string{"c", "b", "a"}, tags)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"nested": map[string]any{"same": []any{1}, "left": 9, "right": 2}, "replace": []any{3}}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, want) {
		t.Fatalf("merge = %#v; reordered = %#v", a, b)
	}
}

func TestSelectors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		s        Selectors
		hosts    []string
		services []string
	}{
		{"all", Selectors{}, []string{"gena", "lanst"}, []string{"repo", "app"}},
		{"services", Selectors{Services: []string{"app"}}, []string{"gena", "lanst"}, []string{"app"}},
		{"host", Selectors{Hosts: []string{"gena"}}, []string{"gena"}, []string{"repo", "app"}},
		{"host-services", Selectors{Hosts: []string{"lanst"}, Services: []string{"repo"}}, []string{"lanst"}, []string{"repo"}},
		{"tag", Selectors{Tags: []string{"common"}}, []string{"gena", "lanst"}, []string{"repo", "app"}},
		{"tag-services", Selectors{Tags: []string{"home"}, Services: []string{"app"}}, []string{"gena"}, []string{"app"}},
		{"union", Selectors{Hosts: []string{"gena"}, Tags: []string{"net"}, Services: []string{"repo", "app"}}, []string{"gena", "lanst"}, []string{"repo", "app"}},
		{"repeat-tags", Selectors{Tags: []string{"home", "net", "home"}}, []string{"gena", "lanst"}, []string{"repo", "app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := fixture().Resolve(tc.s)
			if err != nil {
				t.Fatal(err)
			}
			var hosts []string
			for _, h := range out.Hosts {
				hosts = append(hosts, h.Name)
				var services []string
				for _, s := range h.Services {
					services = append(services, s.Name)
				}
				if !reflect.DeepEqual(services, tc.services) {
					t.Fatalf("%s services %v", h.Name, services)
				}
			}
			if !reflect.DeepEqual(hosts, tc.hosts) {
				t.Fatalf("hosts %v", hosts)
			}
		})
	}
	for _, s := range []Selectors{{Hosts: []string{"missing"}}, {Tags: []string{"missing"}}, {Services: []string{"missing"}}} {
		if _, err := fixture().Resolve(s); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("missing selector: %v", err)
		}
	}
}

func TestServiceFilterDoesNotAssignServices(t *testing.T) {
	in := fixture()
	in.Hosts[1].Services = in.Hosts[1].Services[:1]
	out, err := in.Resolve(Selectors{Services: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts[0].Services) != 1 || len(out.Hosts[1].Services) != 0 {
		t.Fatal("filter assigned an unassigned service")
	}
}

func TestOwnershipChecksAllAssignments(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second Service
		resource      string
	}{
		{"file", Service{Configs: map[string]Config{"a": inlineConfig("/etc/app/../same")}}, Service{Configs: map[string]Config{"b": inlineConfig("/etc/same")}}, "file /etc/same"},
		{"package", Service{Binaries: map[string]Binary{"a": {Type: "rpm", Manager: "dnf", Package: "shared"}}}, Service{Binaries: map[string]Binary{"b": {Type: "rpm", Manager: "yum", Package: "shared"}}}, "package rpm:shared"},
		{"unit", Service{Processes: map[string]Process{"a": {Type: "systemd", Unit: "shared.service"}}}, Service{Processes: map[string]Process{"b": {Type: "systemd", Unit: "shared.service"}}}, "systemd unit shared.service"},
		{"unit-file", Service{Configs: map[string]Config{"unit": inlineConfig("/usr/lib/systemd/system/shared.service")}}, Service{Processes: map[string]Process{"b": {Type: "systemd", Unit: "shared.service"}}}, "systemd unit shared.service"},
		{"dropin", Service{Configs: map[string]Config{"override": inlineConfig("/etc/systemd/system/shared.service.d/override.conf")}}, Service{Processes: map[string]Process{"b": {Type: "systemd", Unit: "shared.service"}}}, "systemd unit shared.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			in.Services["repo"], in.Services["app"] = tc.first, tc.second
			_, err := in.Resolve(Selectors{Hosts: []string{"lanst"}, Services: []string{"app"}})
			if err == nil {
				t.Fatal("filter hid ownership conflict")
			}
			for _, part := range []string{"lanst", tc.resource, "repo/", "app/"} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("missing %s: %v", part, err)
				}
			}
		})
	}
}

func TestUnselectedHostConflictsAreOutsideScope(t *testing.T) {
	in := fixture()
	in.Hosts[1].Tags = append(in.Hosts[1].Tags, "home")
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Resolve(Selectors{Services: []string{"repo"}}); err == nil {
		t.Fatal("service filter hid tag conflict")
	}
}

func TestDuplicateResourcesWithinService(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service Service
		want    string
	}{
		{"file", Service{Configs: map[string]Config{"a": inlineConfig("/etc/same"), "b": inlineConfig("/etc/dir/../same")}}, "file /etc/same"},
		{"package", Service{Binaries: map[string]Binary{"a": {Type: "rpm", Package: "same"}, "b": {Type: "rpm", Package: "same", Version: "2"}}}, "package rpm:same"},
		{"process", Service{Processes: map[string]Process{"a": {Type: "systemd", Unit: "same.service"}, "b": {Type: "systemd", Unit: "same.service"}}}, "systemd unit same.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			in.Services["app"] = tc.service
			_, err := in.Resolve(Selectors{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestProcessCanOwnItsUnitAndMultipleDropins(t *testing.T) {
	in := fixture()
	unit := inlineConfig("/etc/systemd/system/app.service")
	in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "app.service", UnitFile: &unit, Overrides: map[string]Config{"a.conf": inlineConfig("/etc/systemd/system/app.service.d/a.conf"), "b.conf": inlineConfig("/etc/systemd/system/app.service.d/b.conf")}}}}
	if _, err := in.Resolve(Selectors{}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceReferencesCyclesAndFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Inventory)
		want   string
	}{
		{"empty", func(in *Inventory) { in.Services["app"] = Service{} }, "at least one"},
		{"component-name", func(in *Inventory) {
			in.Services["app"] = Service{Configs: map[string]Config{"bad.name": inlineConfig("/etc/a")}}
		}, "config"},
		{"reaction", func(in *Inventory) {
			in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "a.service", RestartOn: []string{"config.missing"}}}}
		}, "restart_on"},
		{"cross-service", func(in *Inventory) {
			in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "a.service", ReloadOn: []string{"repo.config.main"}}}}
		}, "reload_on"},
		{"after", func(in *Inventory) {
			in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "a.service", After: []string{"unknown"}}}}
		}, "after"},
		{"cycle", func(in *Inventory) {
			in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "a.service", After: []string{"db"}}, "db": {Type: "systemd", Unit: "b.service", After: []string{"web"}}}}
		}, "cycle"},
		{"order", func(in *Inventory) { in.Hosts[0].Services[1].Order = 20 }, "order 20"},
		{"unknown-service", func(in *Inventory) { in.Hosts[0].Services[0].Name = "unknown" }, "unknown service"},
		{"unknown-tag", func(in *Inventory) { in.Hosts[0].Tags = append(in.Hosts[0].Tags, "unknown") }, "unknown tag"},
		{"timeout", func(in *Inventory) {
			in.Services["app"] = Service{Processes: map[string]Process{"web": {Type: "systemd", Unit: "a.service", Timeout: "0s"}}}
		}, "timeout"},
		{"relative-file", func(in *Inventory) {
			in.Services["app"] = Service{Configs: map[string]Config{"main": inlineConfig("relative")}}
		}, "absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			tc.mutate(in)
			_, err := in.Resolve(Selectors{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestProcessDefaultsAndValidGraph(t *testing.T) {
	in := fixture()
	in.Services["app"] = Service{Configs: map[string]Config{"main": inlineConfig("/etc/app")}, Binaries: map[string]Binary{"rpm": {Type: "rpm", Package: "app"}}, Processes: map[string]Process{"db": {Type: "systemd", Unit: "db.service"}, "web": {Type: "systemd", Unit: "web.service", After: []string{"db"}, RestartOn: []string{"binary.rpm"}, ReloadOn: []string{"config.main"}}}}
	out, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	p := out.Hosts[0].Services[1].Definition.Processes["web"]
	if p.State != "running" || p.Timeout != "5s" {
		t.Fatalf("defaults: %#v", p)
	}
	if in.Services["app"].Processes["web"].State != "" {
		t.Fatal("defaults mutated inventory")
	}
}
