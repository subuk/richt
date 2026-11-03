package plan

import "testing"

func accountNumber(v uint32) *uint32 { return &v }
func validAccountPlan() *AccountChange {
	groups := []string{}
	return &AccountChange{Kind: "user", Name: "app", State: "present", Operation: "create", PrimaryGroup: "app", Home: "/nonexistent", Shell: "/usr/sbin/nologin", Comment: new(string), SupplementaryGroups: &groups, Groups: []AccountGroup{{Name: "app", GID: accountNumber(1001)}}}
}

func TestAccountPlanValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AccountChange)
	}{
		{"unknown operation", func(a *AccountChange) { a.Operation = "execute" }},
		{"unknown state", func(a *AccountChange) { a.State = "running" }},
		{"command-like name", func(a *AccountChange) { a.Name = "-f" }},
		{"root", func(a *AccountChange) { a.Name = "root" }},
		{"zero UID", func(a *AccountChange) { a.UID = accountNumber(0) }},
		{"reserved UID", func(a *AccountChange) { a.UID = accountNumber(^uint32(0)) }},
		{"root expected UID", func(a *AccountChange) { a.Operation = "update"; a.Expected = &AccountIdentity{UID: 0, GID: 1001} }},
		{"create has expected", func(a *AccountChange) { a.Expected = &AccountIdentity{UID: 1001, GID: 1001} }},
		{"update missing expected", func(a *AccountChange) { a.Operation = "update" }},
		{"UID change", func(a *AccountChange) {
			a.Operation = "update"
			a.Expected = &AccountIdentity{UID: 1001, GID: 1001}
			a.UID = accountNumber(1002)
		}},
		{"user GID", func(a *AccountChange) { a.GID = accountNumber(1001) }},
		{"missing primary", func(a *AccountChange) { a.PrimaryGroup = ""; a.Groups = nil }},
		{"missing group precondition", func(a *AccountChange) { a.Groups = nil }},
		{"extra group precondition", func(a *AccountChange) {
			a.Groups = append(a.Groups, AccountGroup{Name: "other", GID: accountNumber(2000)})
		}},
		{"duplicate group precondition", func(a *AccountChange) { a.Groups = append(a.Groups, a.Groups[0]) }},
		{"relative home", func(a *AccountChange) { a.Home = "home" }},
		{"control home", func(a *AccountChange) { a.Home = "/tmp/\tbad" }},
		{"root home", func(a *AccountChange) { a.Home = "/" }},
		{"bad shell", func(a *AccountChange) { a.Shell = "/bin/sh:bad" }},
		{"bad comment", func(a *AccountChange) { s := "bad\nline"; a.Comment = &s }},
		{"duplicate supplementary", func(a *AccountChange) { groups := []string{"app", "app"}; a.SupplementaryGroups = &groups }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAccountPlan()
			tc.mutate(a)
			if err := a.Validate(); err == nil {
				t.Fatal("malformed account plan accepted")
			}
		})
	}
	if err := validAccountPlan().Validate(); err != nil {
		t.Fatal(err)
	}
	a := validAccountPlan()
	a.Name = "App.Worker"
	if err := a.Validate(); err != nil {
		t.Fatalf("inventory-compatible name rejected: %v", err)
	}
}

func TestGroupPlanRejectsUserAndAbsentFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AccountChange)
	}{
		{"UID", func(a *AccountChange) { a.UID = accountNumber(1001) }},
		{"home", func(a *AccountChange) { a.Home = "/nonexistent" }},
		{"absent GID", func(a *AccountChange) {
			a.State = "absent"
			a.Operation = "delete"
			a.Expected = &AccountIdentity{GID: 1001}
			a.GID = accountNumber(1001)
		}},
		{"zero GID", func(a *AccountChange) { a.GID = accountNumber(0) }},
		{"reserved GID", func(a *AccountChange) { a.GID = accountNumber(^uint32(0)) }},
		{"root expected GID", func(a *AccountChange) {
			a.State = "absent"
			a.Operation = "delete"
			a.Expected = &AccountIdentity{GID: 0}
		}},
		{"GID change", func(a *AccountChange) {
			a.Operation = "noop"
			a.Expected = &AccountIdentity{GID: 1001}
			a.GID = accountNumber(1002)
		}},
		{"group update", func(a *AccountChange) { a.Operation = "update"; a.Expected = &AccountIdentity{GID: 1001} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &AccountChange{Kind: "group", Name: "app", State: "present", Operation: "create"}
			tc.mutate(a)
			if err := a.Validate(); err == nil {
				t.Fatal("malformed group plan accepted")
			}
		})
	}
}
