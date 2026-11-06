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

func debPlan() *plan.Plan {
	return &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/binary/0", Operation: "deb", DEB: &plan.DEBChange{Manager: "apt", Package: "app", Version: "2:1.0~rc1-1", State: "present", CacheMaxAge: "48h", LockTimeout: "60s", Timeout: "15m", ConffilePolicy: "keep"}, Progress: plan.Progress{Status: "pending"}}}}}}
}

func TestDEBSavedRoundTrip(t *testing.T) {
	for _, state := range []string{"present", "absent"} {
		for _, version := range []string{"", "2:1.0~rc1-1"} {
			p := debPlan()
			p.Hosts[0].Tasks[0].DEB.State = state
			p.Hosts[0].Tasks[0].DEB.Version = version
			filename := filepath.Join(t.TempDir(), "plan.json")
			if err := plan.Save(filename, p); err != nil {
				t.Fatal(err)
			}
			store, err := plan.Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			got := *store.Plan.Hosts[0].Tasks[0].DEB
			store.Close()
			if want := *p.Hosts[0].Tasks[0].DEB; got != want {
				t.Fatalf("lost frozen policy: got %+v, want %+v", got, want)
			}
		}
	}
}

func TestDEBValidationBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*plan.Task)
	}{
		{"missing", func(t *plan.Task) { t.DEB = nil }},
		{"wrong-operation", func(t *plan.Task) { t.Operation = "rpm" }},
		{"mixed", func(t *plan.Task) { t.RPM = &plan.RPMChange{Manager: "dnf", Package: "app", State: "present"} }},
		{"manager", func(t *plan.Task) { t.DEB.Manager = "dnf" }},
		{"package", func(t *plan.Task) { t.DEB.Package = "app:amd64" }},
		{"version", func(t *plan.Task) { t.DEB.Version = "v1.0" }},
		{"state", func(t *plan.Task) { t.DEB.State = "purge" }},
		{"cache", func(t *plan.Task) { t.DEB.CacheMaxAge = "0s" }},
		{"conffiles", func(t *plan.Task) { t.DEB.ConffilePolicy = "ask" }},
		{"missing-timeout", func(t *plan.Task) { t.DEB.Timeout = "" }},
		{"negative-lock", func(t *plan.Task) { t.DEB.LockTimeout = "-1s" }},
		{"unbounded-lock", func(t *plan.Task) { t.DEB.LockTimeout = "25h" }},
		{"unbounded-timeout", func(t *plan.Task) { t.DEB.Timeout = "25h" }},
		{"lock-over-total", func(t *plan.Task) { t.DEB.LockTimeout = "16m" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := debPlan()
			tc.edit(&p.Hosts[0].Tasks[0])
			e := &executor{}
			if _, err := plan.Apply(context.Background(), p, e, nil); err == nil {
				t.Fatal("accepted malformed DEB task")
			}
			if len(e.calls) != 0 {
				t.Fatalf("executed invalid plan: %v", e.calls)
			}
		})
	}
}

func TestDEBMalformedSavedInputs(t *testing.T) {
	for _, field := range []string{"manager", "package", "version", "state", "cache_max_age", "lock_timeout", "timeout", "conffile_policy"} {
		for _, value := range []string{`null`, `""`, `true`, `123`, `[]`, `{}`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				data, err := json.Marshal(debPlan())
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				deb := doc["hosts"].([]any)[0].(map[string]any)["tasks"].([]any)[0].(map[string]any)["deb"].(map[string]any)
				var bad any
				if err := json.Unmarshal([]byte(value), &bad); err != nil {
					t.Fatal(err)
				}
				deb[field] = bad
				data, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				filename := filepath.Join(t.TempDir(), "plan.json")
				if err := os.WriteFile(filename, data, 0600); err != nil {
					t.Fatal(err)
				}
				if store, err := plan.Open(filename); err == nil {
					store.Close()
					t.Fatal("accepted malformed DEB JSON")
				}
			})
		}
	}
}

func TestDEBUnknownJSONFieldsRemainStrict(t *testing.T) {
	var change plan.DEBChange
	if err := json.Unmarshal([]byte(`{"manager":"apt","package":"app","PRIVATE_CANARY":true}`), &change); err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatalf("unsafe unknown field error: %v", err)
	}
}

func TestDEBMissingSavedPolicyRejected(t *testing.T) {
	for _, field := range []string{"manager", "package", "state", "cache_max_age", "lock_timeout", "timeout", "conffile_policy"} {
		t.Run(field, func(t *testing.T) {
			p := debPlan()
			data, err := json.Marshal(p.Hosts[0].Tasks[0].DEB)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, field)
			data, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var change plan.DEBChange
			if err := json.Unmarshal(data, &change); err != nil {
				t.Fatal(err)
			}
			p.Hosts[0].Tasks[0].DEB = &change
			if err := p.Validate(); err == nil {
				t.Fatal("accepted omitted frozen DEB policy")
			}
		})
	}
	var nilChange *plan.DEBChange
	if err := nilChange.Validate(); err == nil {
		t.Fatal("accepted nil DEB change")
	}
}
