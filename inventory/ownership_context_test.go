package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveRetainsUnfilteredOwnershipServices(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool")}})
	resolved, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"repo"}})
	if err != nil {
		t.Fatal(err)
	}
	host := resolved.Hosts[0]
	if len(host.Services) != 1 || host.Services[0].Name != "repo" {
		t.Fatalf("execution selection changed: %#v", host.Services)
	}
	if len(host.OwnershipServices) != 2 || host.OwnershipServices[0].Name != "repo" || host.OwnershipServices[1].Name != "app" {
		t.Fatalf("ownership assignments lost or unordered: %#v", host.OwnershipServices)
	}
	for _, service := range host.OwnershipServices {
		if service.State != "present" {
			t.Fatalf("unnormalized assignment state: %#v", service.Assignment)
		}
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"OwnershipServices", "ownership_services", "/opt/tool", "https://example.test"} {
		if strings.Contains(string(data), hidden) {
			t.Fatalf("ownership context leaked into inspection JSON: %s", data)
		}
	}
	var view struct {
		Hosts []struct {
			Services []ResolvedService `json:"services"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	wantServices, err := json.Marshal(host.Services)
	if err != nil {
		t.Fatal(err)
	}
	gotServices, err := json.Marshal(view.Hosts[0].Services)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotServices) != string(wantServices) {
		t.Fatal("visible selected services changed in JSON")
	}
}

func TestOwnershipContextKeepsStatesAndNormalizedDefinitions(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Binaries: []Binary{{Type: "rpm", Package: "app"}}})
	resolved, err := in.Resolve(Selectors{Hosts: []string{"lanst"}, Services: []string{"repo"}})
	if err != nil {
		t.Fatal(err)
	}
	ownership := resolved.Hosts[0].OwnershipServices
	if ownership[1].State != "absent" || ownership[1].Definition.Binaries[0].Manager != "dnf" {
		t.Fatalf("lost normalized excluded assignment: %#v", ownership[1])
	}
	if in.Services["app"].Binaries[0].Manager != "" {
		t.Fatal("ownership normalization mutated inventory")
	}
}
