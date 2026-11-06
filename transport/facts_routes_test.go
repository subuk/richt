package transport

import (
	"testing"

	"github.com/subuk/richt/facts"
)

func TestDefaultRouteSelectionFixtures(t *testing.T) {
	interfaces := map[string]facts.Interface{"eth0": {Name: "eth0"}, "eth1": {Name: "eth1"}}
	for _, tt := range []struct {
		name, data, status, iface, gateway string
		v6                                 bool
	}{
		{"absent", `[]`, "absent", "", "", false},
		{"direct", `[{"dev":"eth0"}]`, "selected", "eth0", "", false},
		{"lowest metric", `[{"dev":"eth1","metric":100},{"dev":"eth0","metric":10,"gateway":"192.0.2.1"}]`, "selected", "eth0", "192.0.2.1", false},
		{"lowest priority", `[{"dev":"eth1","priority":100},{"dev":"eth0","priority":10}]`, "selected", "eth0", "", false},
		{"equal distinct", `[{"dev":"eth0","metric":10},{"dev":"eth1","metric":10}]`, "ambiguous", "", "", false},
		{"same path", `[{"dev":"eth0"},{"dev":"eth0"}]`, "selected", "eth0", "", false},
		{"different gateways", `[{"dev":"eth0","gateway":"192.0.2.1"},{"dev":"eth0","gateway":"192.0.2.3"}]`, "ambiguous", "", "", false},
		{"multipath", `[{"nexthops":[{"dev":"eth0","weight":2},{"dev":"eth1","weight":1}]}]`, "ambiguous", "", "", false},
		{"multipath single usable", `[{"nexthops":[{"dev":"eth0"},{"dev":"eth1","flags":["dead"]}]}]`, "selected", "eth0", "", false},
		{"unresolved nhid", `[{"nhid":12}]`, "ambiguous", "", "", false},
		{"unresolved cross family via", `[{"dev":"eth0","via":{"family":"inet6","host":"2001:db8::1"}}]`, "ambiguous", "", "", false},
		{"source specific", `[{"dev":"eth0","from":"2001:db8::/64"}]`, "ambiguous", "", "", true},
		{"encapsulation", `[{"dev":"eth0","encap":{"type":"mpls","labels":[100]}}]`, "ambiguous", "", "", false},
		{"missing path", `[{"gateway":"192.0.2.1"}]`, "ambiguous", "", "", false},
		{"unknown interface", `[{"dev":"missing"}]`, "ambiguous", "", "", false},
		{"unusable", `[{"type":"blackhole"},{"dev":"eth0","flags":["linkdown"]}]`, "absent", "", "", false},
		{"unusable better", `[{"metric":1,"nexthops":[{"dev":"eth1","flags":["dead"]}]},{"dev":"eth0","metric":10}]`, "selected", "eth0", "", false},
		{"exclude non-main", `[{"dev":"eth1","table":100},{"dev":"eth0","table":"main"}]`, "selected", "eth0", "", false},
		{"main numeric", `[{"dev":"eth0","table":254}]`, "selected", "eth0", "", false},
		{"exclude nondefault", `[{"dev":"eth1","dst":"192.0.2.0/24"}]`, "absent", "", "", false},
		{"ipv6 preference", `[{"dev":"eth0","metric":100,"pref":"low"},{"dev":"eth1","metric":100,"pref":"high","gateway":"2001:db8::1"}]`, "selected", "eth1", "2001:db8::1", true},
		{"metric precedes preference", `[{"dev":"eth0","metric":10,"pref":"low"},{"dev":"eth1","metric":100,"pref":"high"}]`, "selected", "eth0", "", true},
		{"ipv6 equal preference", `[{"dev":"eth0","pref":"medium"},{"dev":"eth1"}]`, "ambiguous", "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDefaultRoutes([]byte(tt.data), tt.v6, interfaces)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.status || got.Interface != tt.iface || got.Gateway != tt.gateway {
				t.Fatalf("got %+v", got)
			}
		})
	}
	for _, raw := range []string{`null`, `[null]`, `{}`, `[] []`, `[{"metric":"bad"}]`, `[{"dev":"eth0","gateway":"not-ip"}]`, `[{"dev":"eth0","metric":-1}]`, `[{"metric":2,"priority":3}]`, `[{"table":null}]`, `[{"table":{}}]`, `[{"table":-1}]`} {
		if _, err := parseDefaultRoutes([]byte(raw), false, interfaces); err == nil {
			t.Fatal("accepted malformed route", raw)
		}
	}
}
