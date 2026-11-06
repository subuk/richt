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

func definition(order int, service Service) ServiceDefinition {
	return ServiceDefinition{Order: &order, Service: service}
}

func setService(in *Inventory, name string, service Service) {
	d := in.Services[name]
	d.Service = service
	in.Services[name] = d
}

func fixture() *Inventory {
	return &Inventory{
		Tags: map[string]Tag{
			"common": {Order: 10, Services: map[string]ServiceAssignment{"repo": {}}, Vars: map[string]any{"app": map[string]any{"port": 8080, "name": "default"}, "list": []any{"old"}}},
			"home":   {Order: 20, Services: map[string]ServiceAssignment{"app": {}}, Vars: map[string]any{"app": map[string]any{"name": "Home"}, "list": []any{"new"}}},
			"net":    {Order: 20, Services: map[string]ServiceAssignment{"app": {State: "absent"}}, Vars: map[string]any{"app": map[string]any{"name": "Net"}}},
		},
		Services: map[string]ServiceDefinition{"app": definition(20, Service{Configs: []Config{namedConfig("main", inlineConfig("/etc/app.conf"))}}), "repo": definition(10, Service{Configs: []Config{namedConfig("repo", inlineConfig("/etc/yum.repos.d/app.repo"))}})},
		Hosts:    []Host{{Name: "gena", Tags: []string{"home", "common"}, Transport: Transport{Type: "local"}}, {Name: "lanst", Tags: []string{"common", "net"}, Transport: Transport{Type: "ssh", Host: "lanst.example", Sudo: true}}},
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

func TestEqualOrderConflictsBeforeOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
	}{{"scalar", 1, 2}, {"list", []any{1}, []any{2}}, {"map-scalar", map[string]any{"x": 1}, "value"}, {"null", nil, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			tags := map[string]Tag{"a": {Order: 10, Vars: map[string]any{"nested": map[string]any{"value": tc.a}}}, "b": {Order: 10, Vars: map[string]any{"nested": map[string]any{"value": tc.b}}}, "later": {Order: 20, Vars: map[string]any{"nested": "overridden"}}}
			for _, names := range [][]string{{"a", "b", "later"}, {"later", "b", "a"}} {
				_, err := MergeTags("gena", names, tags)
				if err == nil {
					t.Fatal("expected conflict")
				}
				for _, part := range []string{"gena", "vars.nested.value", `"a"`, `"b"`, "order 10"} {
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
		"a": {Order: 10, Vars: map[string]any{"nested": map[string]any{"same": []any{1}, "left": 1}, "replace": map[string]any{"old": 1}}},
		"b": {Order: 10, Vars: map[string]any{"nested": map[string]any{"same": []any{1}, "right": 2}}},
		"c": {Order: 20, Vars: map[string]any{"replace": []any{3}, "nested": map[string]any{"left": 9}}},
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
	delete(in.Tags["net"].Services, "app")
	out, err := in.Resolve(Selectors{Services: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts[0].Services) != 1 || len(out.Hosts[1].Services) != 0 {
		t.Fatal("filter assigned an unassigned service")
	}
}

func TestOwnershipChecksAllAssignments(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			for _, tc := range []struct {
				name          string
				first, second Service
				resource      string
			}{
				{"file", Service{Configs: []Config{namedConfig("a", inlineConfig("/etc/app/../same"))}}, Service{Configs: []Config{namedConfig("b", inlineConfig("/etc/same"))}}, "file /etc/same"},
				{"package", Service{Binaries: []Binary{{Name: "a", Type: "rpm", Manager: "dnf", Package: "shared"}}}, Service{Binaries: []Binary{{Name: "b", Type: "rpm", Manager: "yum", Package: "shared"}}}, "package rpm:shared"},
				{"unit", Service{Processes: []Process{{Name: "a", Type: "systemd", Unit: "shared." + suffix}}}, Service{Processes: []Process{{Name: "b", Type: "systemd", Unit: "shared." + suffix}}}, "systemd unit shared." + suffix},
				{"unit-file", Service{Configs: []Config{namedConfig("unit", inlineConfig("/usr/lib/systemd/system/shared."+suffix))}}, Service{Processes: []Process{{Name: "b", Type: "systemd", Unit: "shared." + suffix}}}, "systemd unit shared." + suffix},
				{"dropin", Service{Configs: []Config{namedConfig("override", inlineConfig("/etc/systemd/system/shared."+suffix+".d/override.conf"))}}, Service{Processes: []Process{{Name: "b", Type: "systemd", Unit: "shared." + suffix}}}, "systemd unit shared." + suffix},
			} {
				t.Run(tc.name, func(t *testing.T) {
					in := fixture()
					setService(in, "repo", tc.first)
					setService(in, "app", tc.second)
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
		{"file", Service{Configs: []Config{namedConfig("a", inlineConfig("/etc/same")), namedConfig("b", inlineConfig("/etc/dir/../same"))}}, "file /etc/same"},
		{"package", Service{Binaries: []Binary{{Name: "a", Type: "rpm", Package: "same"}, {Name: "b", Type: "rpm", Package: "same", Version: "2"}}}, "package rpm:same"},
		{"process", Service{Processes: []Process{{Name: "a", Type: "systemd", Unit: "same.service"}, {Name: "b", Type: "systemd", Unit: "same.service"}}}, "systemd unit same.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			setService(in, "app", tc.service)
			_, err := in.Resolve(Selectors{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestProcessCanOwnItsUnitAndMultipleDropins(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			in := fixture()
			unit := inlineConfig("/etc/systemd/system/app." + suffix)
			setService(in, "app", Service{Processes: []Process{{Name: "web", Type: "systemd", Unit: "app." + suffix, UnitFile: &unit, Overrides: map[string]Config{"a.conf": inlineConfig("/etc/systemd/system/app." + suffix + ".d/a.conf"), "b.conf": inlineConfig("/etc/systemd/system/app." + suffix + ".d/b.conf")}}}})
			if _, err := in.Resolve(Selectors{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceReferencesAndFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Inventory)
		want   string
	}{
		{"empty", func(in *Inventory) { setService(in, "app", Service{}) }, "at least one"},
		{"component-name", func(in *Inventory) {
			setService(in, "app", Service{Configs: []Config{namedConfig("bad.name", inlineConfig("/etc/a"))}})
		}, "config"},
		{"reaction", func(in *Inventory) {
			setService(in, "app", Service{Processes: []Process{{Name: "web", Type: "systemd", Unit: "a.service", RestartOn: []string{"config.missing"}}}})
		}, "restart_on"},
		{"cross-service", func(in *Inventory) {
			setService(in, "app", Service{Processes: []Process{{Name: "web", Type: "systemd", Unit: "a.service", ReloadOn: []string{"repo.config.main"}}}})
		}, "reload_on"},
		{"order", func(in *Inventory) {
			d := in.Services["repo"]
			d.Order = in.Services["app"].Order
			in.Services["repo"] = d
		}, "order 20"},
		{"unknown-service", func(in *Inventory) { in.Tags["home"].Services["unknown"] = ServiceAssignment{} }, "unknown service"},
		{"unknown-tag", func(in *Inventory) { in.Hosts[0].Tags = append(in.Hosts[0].Tags, "unknown") }, "unknown tag"},
		{"timeout", func(in *Inventory) {
			setService(in, "app", Service{Processes: []Process{{Name: "web", Type: "systemd", Unit: "a.service", Timeout: "0s"}}})
		}, "timeout"},
		{"relative-file", func(in *Inventory) {
			setService(in, "app", Service{Configs: []Config{namedConfig("main", inlineConfig("relative"))}})
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

func TestProcessDefaultsAndValidReferences(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Configs: []Config{namedConfig("main", inlineConfig("/etc/app"))}, Binaries: []Binary{{Name: "rpm", Type: "rpm", Package: "app"}}, Processes: []Process{{Name: "db", Type: "systemd", Unit: "db.service"}, {Name: "web", Type: "systemd", Unit: "web.service", RestartOn: []string{"binary.rpm"}, ReloadOn: []string{"config.main"}}}})
	out, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	p := out.Hosts[0].Services[1].Definition.Processes[1]
	if p.State != "running" || p.Timeout != "5s" {
		t.Fatalf("defaults: %#v", p)
	}
	if in.Services["app"].Processes[1].State != "" {
		t.Fatal("defaults mutated inventory")
	}
}

func TestSSHTransportConnectionSyntax(t *testing.T) {
	for _, host := range []string{"server.example", "deployment-alias", "192.0.2.1", "2001:db8::1", "[2001:db8::1]"} {
		in := fixture()
		in.Hosts[1].Transport = Transport{Type: "ssh", Host: host, User: "deploy", Port: 2222, Sudo: true}
		if _, err := in.Resolve(Selectors{}); err != nil {
			t.Errorf("host %q: %v", host, err)
		}
	}
	for _, host := range []string{"", "user@server", "ssh://server", "server/path", "-option", "a b", "a\tb", "a\nb", "a\x00b", "a\u00a0b", "a\x7fb"} {
		in := fixture()
		in.Hosts[1].Transport = Transport{Type: "ssh", Host: host}
		if err := in.Validate(); err == nil {
			t.Errorf("accepted host %q", host)
		}
	}
	for _, user := range []string{"-option", "a b", "a\tb", "a\nb", "a\x00b", "a\u00a0b", "a\x7fb"} {
		in := fixture()
		in.Hosts[1].Transport = Transport{Type: "ssh", Host: "alias", User: user}
		if err := in.Validate(); err == nil {
			t.Errorf("accepted user %q", user)
		}
	}
}

func TestSSHInspectionDoesNotOpenTemplatesOrSecrets(t *testing.T) {
	in := fixture()
	in.SecretSources = map[string]SecretSource{"example": {Type: "yaml", File: "/does-not-exist/richt-secret.yaml"}}
	s := in.Services["app"]
	c := s.Configs[0]
	c.Template = Template{File: "/does-not-exist/richt-template"}
	s.Configs[0] = c
	in.Services["app"] = s
	if _, err := in.Resolve(Selectors{Hosts: []string{"lanst"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateTagAssignmentsBeforeServiceFiltering(t *testing.T) {
	for _, state := range []string{"", "present", "absent"} {
		for _, tagOrder := range []int{0, 20, 100} {
			in := fixture()
			in.Tags["extra"] = Tag{Order: tagOrder, Services: map[string]ServiceAssignment{"app": {State: state}}}
			in.Hosts[0].Tags = append(in.Hosts[0].Tags, "extra")
			var previous string
			for _, names := range [][]string{{"home", "common", "extra"}, {"extra", "common", "home"}} {
				in.Hosts[0].Tags = names
				_, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"repo"}})
				if err == nil {
					t.Fatalf("accepted duplicate assignment with state %q at tag order %d", state, tagOrder)
				}
				for _, part := range []string{`host "gena"`, `service "app"`, `"extra"`, `"home"`} {
					if !strings.Contains(err.Error(), part) {
						t.Fatalf("missing %s: %v", part, err)
					}
				}
				if previous != "" && err.Error() != previous {
					t.Fatalf("tag listing order changed error: %q versus %q", previous, err.Error())
				}
				previous = err.Error()
			}
		}
	}
}

func TestAssignmentConflictsOnlyOnSelectedHosts(t *testing.T) {
	for _, kind := range []string{"duplicate", "order", "ownership"} {
		t.Run(kind, func(t *testing.T) {
			in := fixture()
			extra := Service{Configs: []Config{namedConfig("extra", inlineConfig("/etc/extra"))}}
			switch kind {
			case "duplicate":
				in.Tags["extra"] = Tag{Services: map[string]ServiceAssignment{"app": {}}}
			case "order":
				in.Services["extra"] = definition(20, extra)
				in.Tags["extra"] = Tag{Services: map[string]ServiceAssignment{"extra": {}}}
			case "ownership":
				extra.Configs[0] = inlineConfig("/etc/app.conf")
				in.Services["extra"] = definition(30, extra)
				in.Tags["extra"] = Tag{Services: map[string]ServiceAssignment{"extra": {}}}
			}
			in.Hosts[1].Tags = append(in.Hosts[1].Tags, "extra")
			if err := in.Validate(); err != nil {
				t.Fatalf("global validation rejected effective host conflict: %v", err)
			}
			if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}}); err != nil {
				t.Fatalf("unselected host conflict escaped scope: %v", err)
			}
			if _, err := in.Resolve(Selectors{Hosts: []string{"lanst"}, Services: []string{"repo"}}); err == nil {
				t.Fatal("service filter hid effective host conflict")
			}
		})
	}
}

func TestServiceOrderRequiredGloballyAndExplicitZeroValid(t *testing.T) {
	in := fixture()
	in.Services["unused"] = definition(0, Service{Configs: []Config{namedConfig("unused", inlineConfig("/etc/unused"))}})
	d := in.Services["app"]
	zero := 0
	d.Order = &zero
	in.Services["app"] = d
	out, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range out.Hosts {
		if h.Services[0].Name != "app" || h.Services[0].Order != 0 {
			t.Fatalf("explicit zero order was lost: %#v", h.Services)
		}
	}
	unused := in.Services["unused"]
	unused.Order = nil
	in.Services["unused"] = unused
	_, err = in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"repo"}})
	if err == nil || !strings.Contains(err.Error(), `service "unused" order: required integer`) {
		t.Fatalf("missing order on unassigned definition: %v", err)
	}
}

func TestSameOrderOnSeparateHosts(t *testing.T) {
	in := fixture()
	in.Hosts[0].Tags = []string{"home"}
	in.Hosts[1].Tags = []string{"common"}
	d := in.Services["repo"]
	d.Order = in.Services["app"].Order
	in.Services["repo"] = d
	if _, err := in.Resolve(Selectors{}); err != nil {
		t.Fatalf("globally repeated order rejected: %v", err)
	}
}

func TestUnusedTagReferencesAndStatesValidated(t *testing.T) {
	for _, tc := range []struct {
		name, service, state, want string
	}{
		{"unknown-service", "missing", "", "unknown service"},
		{"bad-state", "app", "stopped", "expected present or absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			in.Tags["unused"] = Tag{Services: map[string]ServiceAssignment{tc.service: {State: tc.state}}}
			_, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"repo"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `tag "unused"`) {
				t.Fatalf("unused tag validation: %v", err)
			}
		})
	}
}

func TestDefaultTagOrderUsesExistingMergeRules(t *testing.T) {
	tags := map[string]Tag{
		"a":     {Vars: map[string]any{"same": "value", "left": 1}},
		"b":     {Vars: map[string]any{"same": "value", "right": 2}},
		"later": {Order: 1, Vars: map[string]any{"left": 3}},
	}
	merged, err := MergeTags("host", []string{"later", "b", "a"}, tags)
	if err != nil || !reflect.DeepEqual(merged, map[string]any{"same": "value", "left": 3, "right": 2}) {
		t.Fatalf("default order merging: %#v, %v", merged, err)
	}
	tags["b"].Vars["same"] = "different"
	_, err = MergeTags("host", []string{"later", "b", "a"}, tags)
	if err == nil || !strings.Contains(err.Error(), "at order 0") {
		t.Fatalf("default order conflict: %v", err)
	}
}

func TestEffectiveServicesIndependentOfTagAndMappingOrder(t *testing.T) {
	in := fixture()
	want, err := in.Resolve(Selectors{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		in.Hosts[0].Tags = []string{"common", "home"}
		in.Hosts[1].Tags = []string{"net", "common"}
		in.Tags = map[string]Tag{"net": in.Tags["net"], "home": in.Tags["home"], "common": in.Tags["common"]}
		in.Services = map[string]ServiceDefinition{"repo": in.Services["repo"], "app": in.Services["app"]}
		got, err := in.Resolve(Selectors{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("source ordering changed effective services: %#v, %v", got, err)
		}
	}
}

func TestHostWithoutTagAssignmentsResolvesEmptyServices(t *testing.T) {
	for _, tags := range [][]string{nil, {"variables"}} {
		in := fixture()
		in.Tags["variables"] = Tag{Vars: map[string]any{"greeting": "hello"}}
		in.Hosts[0].Tags = tags
		out, err := in.Resolve(Selectors{Hosts: []string{"gena"}})
		if err != nil {
			t.Fatal(err)
		}
		if services := out.Hosts[0].Services; services == nil || len(services) != 0 {
			t.Fatalf("host without assignments: %#v", services)
		}
	}
}

func namedConfig(name string, c Config) Config    { c.Name = name; return c }
func namedProcess(name string, p Process) Process { p.Name = name; return p }
