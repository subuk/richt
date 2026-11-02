package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func oneshotPlan(c *plan.OneshotChange) *plan.Plan {
	return &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/process/0", Operation: "oneshot", Oneshot: c, Progress: plan.Progress{Status: "pending"}}}}}}
}

func TestOneshotSavedPayloadValidation(t *testing.T) {
	valid := func() *plan.OneshotChange {
		return &plan.OneshotChange{Command: []string{"tool", ""}, Timeout: "5s", Triggers: []string{"config.main", "binary.app"}}
	}
	for _, mutate := range []func(*plan.Plan){
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot = nil },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].File = &plan.FileChange{} },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Operation = "file" },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Command = nil },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Command = []string{" \t"} },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Command = []string{"tool", "\x00"} },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Timeout = "0" },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Timeout = "" },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Triggers = nil },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Triggers = []string{"config."} },
		func(p *plan.Plan) { p.Hosts[0].Tasks[0].Oneshot.Triggers = []string{"config.main", "config.main"} },
	} {
		p := oneshotPlan(valid())
		mutate(p)
		if err := p.Validate(); err == nil {
			t.Fatal("accepted malformed oneshot", p)
		}
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := plan.Save(path, oneshotPlan(valid())); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := store.Plan.Hosts[0].Tasks[0].Oneshot
	if len(c.Command) != 2 || len(c.Triggers) != 2 || c.Timeout != "5s" {
		t.Fatal(c)
	}
}

func TestOneshotUnknownJSONFieldsAreRejectedSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	json := `{"version":"` + plan.Version + `","hosts":[{"name":"local","transport":"local","tasks":[{"id":"x","operation":"oneshot","oneshot":{"command":["tool"],"timeout":"5s","triggers":["config.main"],"PRIVATE_CANARY":true},"progress":{"status":"pending"}}]}]}`
	if err := os.WriteFile(path, []byte(json), 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := plan.Open(path); err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		if store != nil {
			store.Close()
		}
		t.Fatal("unsafe unknown field error", err)
	}
}

func TestSavedOneshotArgvRejectsNullAndNonstringEntries(t *testing.T) {
	for _, argv := range []string{`["tool",null]`, `["tool",42]`, `["tool",false]`, `["tool",{}]`, `["tool",[]]`, `["-tool"]`, `["tool",""]`} {
		path := filepath.Join(t.TempDir(), "plan.json")
		data := `{"version":"` + plan.Version + `","hosts":[{"name":"local","transport":"local","tasks":[{"id":"x","operation":"oneshot","oneshot":{"command":` + argv + `,"timeout":"5s","triggers":["config.main"]},"progress":{"status":"pending"}}]}]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		store, err := plan.Open(path)
		if (err == nil) != (argv == `["tool",""]`) {
			if store != nil {
				store.Close()
			}
			t.Fatal("incorrect argv acceptance", argv, err)
		}
		if store != nil {
			store.Close()
		}
	}
}
