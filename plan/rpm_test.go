package plan_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func rpmPlan(value, state string) *plan.Plan {
	return &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/binary/0", Operation: "rpm", RPM: &plan.RPMChange{Manager: "dnf", Package: "app", State: state, DisableExcludes: value}, Progress: plan.Progress{Status: "pending"}}}}}}
}

func TestRPMDisableExcludesSavedRoundTrip(t *testing.T) {
	for _, state := range []string{"present", "absent"} {
		for _, value := range []string{"", "all", "main", "Vendor_Repo-9.2:arch"} {
			t.Run(state+"/"+value, func(t *testing.T) {
				p := rpmPlan(value, state)
				path := filepath.Join(t.TempDir(), "plan.json")
				if err := plan.Save(path, p); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "disable_excludes") != (value != "") {
					t.Fatalf("incorrect omission: %s", data)
				}
				store, err := plan.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				c := store.Plan.Hosts[0].Tasks[0].RPM
				if c.DisableExcludes != value || c.State != state || store.Plan.Version != "1.5" {
					t.Fatalf("lost frozen input: %+v", c)
				}
			})
		}
	}
}

func TestRPMDisableExcludesRejectsMalformedSavedValues(t *testing.T) {
	for _, value := range []string{`null`, `""`, `true`, `123`, `[]`, `{}`, `" all"`, `"all\n"`, `"repo*"`, `"one,two"`, `"a/b"`} {
		t.Run(value, func(t *testing.T) {
			p := rpmPlan("all", "present")
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.Replace(string(data), `"disable_excludes":"all"`, `"disable_excludes":`+value, 1))
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if store, err := plan.Open(path); err == nil {
				store.Close()
				t.Fatal("accepted malformed saved RPM setting")
			}
		})
	}
}

func TestRPMDisableExcludesValidatedBeforeExecution(t *testing.T) {
	p := rpmPlan("repo*", "present")
	e := &executor{}
	if _, err := plan.Apply(context.Background(), p, e, nil); err == nil {
		t.Fatal("applied malformed RPM setting")
	}
	if len(e.calls) != 0 {
		t.Fatalf("executed invalid plan: %v", e.calls)
	}
}

func TestRPMUnknownJSONFieldsRemainStrict(t *testing.T) {
	var change plan.RPMChange
	if err := json.Unmarshal([]byte(`{"manager":"dnf","package":"app","state":"present","PRIVATE_CANARY":true}`), &change); err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatalf("unsafe unknown field error: %v", err)
	}
}
