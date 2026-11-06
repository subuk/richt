package plan_test

import (
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func TestAccountPlanVersionAndPayloadCompatibility(t *testing.T) {
	account := &plan.AccountChange{Kind: "group", Name: "app", State: "present", Operation: "create"}
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/config/0", Operation: "account", Account: account, Progress: plan.Progress{Status: "pending"}}}}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Version = "1.7"
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "prepare a new plan") {
		t.Fatalf("legacy account plan: %v", err)
	}
	p.Version = plan.Version
	task := &p.Hosts[0].Tasks[0]
	task.File = &plan.FileChange{Path: "/tmp/app", State: "present"}
	if err := p.Validate(); err == nil {
		t.Fatal("mixed account/file payload accepted")
	}
	task.File = nil
	task.Operation = "file"
	if err := p.Validate(); err == nil {
		t.Fatal("foreign account payload accepted")
	}
	task.Operation = "account"
	task.Account = nil
	if err := p.Validate(); err == nil {
		t.Fatal("missing account payload accepted")
	}
}

func TestAccountAutomaticGroupReferencesRequirePrecedingCreation(t *testing.T) {
	empty := ""
	groups := []string{}
	user := &plan.AccountChange{Kind: "user", Name: "app", State: "present", Operation: "create", PrimaryGroup: "app", Home: "/nonexistent", Shell: "/usr/sbin/nologin", Comment: &empty, SupplementaryGroups: &groups, Groups: []plan.AccountGroup{{Name: "app"}}}
	group := &plan.AccountChange{Kind: "group", Name: "app", State: "present", Operation: "create"}
	task := func(id string, c *plan.AccountChange) plan.Task {
		return plan.Task{ID: id, Operation: "account", Account: c, Progress: plan.Progress{Status: "pending"}}
	}
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{task("user", user)}}}}
	if err := p.Validate(); err == nil {
		t.Fatal("unbound automatic group reference accepted")
	}
	p.Hosts[0].Tasks = []plan.Task{task("user", user), task("group", group)}
	if err := p.Validate(); err == nil {
		t.Fatal("later automatic group reference accepted")
	}
	p.Hosts[0].Tasks = []plan.Task{task("group", group), task("user", user)}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	id := uint32(2000)
	group.GID = &id
	if err := p.Validate(); err == nil {
		t.Fatal("automatic reference accepted for an explicitly allocated group")
	}
	user.Groups[0].GID = &id
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}
