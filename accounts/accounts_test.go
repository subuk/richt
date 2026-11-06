package accounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit %d", e) }
func (e exitStatus) ExitCode() int { return int(e) }

type invocation struct {
	name string
	args []string
}
type host struct {
	users          map[string]user
	groups         map[string]group
	externalUsers  map[string]user
	externalGroups map[string]group
	calls          []invocation
	fail           string
	mutate         func(string, []string) error
}

func fixture() *host {
	return &host{users: map[string]user{}, groups: map[string]group{"app": {name: "app", gid: 2100}, "extra": {name: "extra", gid: 2101}}, externalUsers: map[string]user{}, externalGroups: map[string]group{}}
}
func (h *host) run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, invocation{name, append([]string{}, args...)})
	if name == h.fail {
		return nil, errors.New("injected command failure")
	}
	if name == "uname" {
		return []byte("Linux\n"), nil
	}
	if name == "getent" {
		local := len(args) == 3 && args[0] == "-s"
		db, key := "", ""
		if local {
			if args[1] != "files" {
				return nil, errors.New("unexpected getent source")
			}
			db = args[2]
		} else if len(args) == 2 {
			db = args[0]
			key = args[1]
		} else {
			return nil, errors.New("unexpected getent arguments")
		}
		var lines []string
		if db == "passwd" {
			candidates := map[string]user{}
			for n, u := range h.users {
				candidates[n] = u
			}
			if !local {
				for n, u := range h.externalUsers {
					candidates[n] = u
				}
			}
			for _, u := range candidates {
				if key != "" && key != u.name && key != strconv.FormatUint(uint64(u.uid), 10) {
					continue
				}
				lines = append(lines, fmt.Sprintf("%s:x:%d:%d:%s:%s:%s", u.name, u.uid, u.gid, u.comment, u.home, u.shell))
			}
		} else if db == "group" {
			candidates := map[string]group{}
			for n, g := range h.groups {
				candidates[n] = g
			}
			if !local {
				for n, g := range h.externalGroups {
					candidates[n] = g
				}
			}
			for _, g := range candidates {
				if key != "" && key != g.name && key != strconv.FormatUint(uint64(g.gid), 10) {
					continue
				}
				lines = append(lines, fmt.Sprintf("%s:x:%d:%s", g.name, g.gid, strings.Join(g.members, ",")))
			}
		} else {
			return nil, errors.New("unexpected database")
		}
		sort.Strings(lines)
		if !local && len(lines) == 0 {
			return nil, exitStatus(2)
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	}
	if name == "sh" {
		return nil, nil
	}
	if name == "unshare" && strings.Contains(strings.Join(args, " "), "richt-account-probe") {
		return nil, nil
	}
	if h.mutate != nil {
		return nil, h.mutate(name, args)
	}
	return nil, fmt.Errorf("unexpected mutation %s", name)
}
func (h *host) backend() Backend { return Backend{Runner: h.run} }
func str(s string) *string       { return &s }
func list(s ...string) *[]string { return &s }
func (h *host) mutations() []invocation {
	var out []invocation
	for _, c := range h.calls {
		if c.name == "useradd" || c.name == "usermod" || c.name == "groupadd" || c.name == "groupdel" || c.name == "unshare" && strings.Contains(strings.Join(c.args, " "), "richt-account-delete") {
			out = append(out, c)
		}
	}
	return out
}
func execute(t *testing.T, b Backend, a *plan.AccountChange) error {
	t.Helper()
	actions, e := b.Resolve(plan.Task{Operation: "account", Account: a})
	if e != nil {
		t.Fatal(e)
	}
	return b.Execute(context.Background(), actions[0])
}

func TestCreateUserFreezesDefaultsAndSafeCommand(t *testing.T) {
	h := fixture()
	b := h.backend()
	a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app"}, "present", nil)
	if err != nil || !changed || a.Operation != "create" {
		t.Fatalf("prepare: %#v %v %v", a, changed, err)
	}
	if a.Home != "/nonexistent" || a.Shell != "/usr/sbin/nologin" || a.Comment == nil || *a.Comment != "" || a.SupplementaryGroups == nil || len(*a.SupplementaryGroups) != 0 {
		t.Fatalf("defaults not frozen: %#v", a)
	}
	h.mutate = func(name string, args []string) error {
		if name != "useradd" {
			t.Fatalf("unexpected mutation %s", name)
		}
		want := []string{"--no-user-group", "--no-create-home", "--password", "!", "--gid", "app", "--home", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "", "--groups", "", "--", "worker"}
		if !sameArgs(args, want) {
			t.Fatalf("args %#v want %#v", args, want)
		}
		h.users["worker"] = user{"worker", 3100, 2100, "/nonexistent", "/usr/sbin/nologin", ""}
		return nil
	}
	if err = execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	if err = execute(t, b, a); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(h.mutations()) != 1 {
		t.Fatalf("already matching create executed again: %#v", h.mutations())
	}
}
func sameArgs(a, b []string) bool {
	return len(a) == len(b) && strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func TestExistingOmittedFieldsAndExactMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		groups  *[]string
		changed bool
	}{{"preserve", nil, false}, {"clear", list(), true}, {"unchanged", list("extra"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture()
			h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
			g := h.groups["extra"]
			g.members = []string{"worker"}
			h.groups["extra"] = g
			b := h.backend()
			a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: tc.groups}, "present", nil)
			if err != nil || changed != tc.changed {
				t.Fatalf("prepare: %v %v", changed, err)
			}
			if a.Home != "" || a.Shell != "" || a.Comment != nil {
				t.Fatalf("omission overwritten: %#v", a)
			}
			if tc.changed {
				h.mutate = func(name string, args []string) error {
					if name != "usermod" || !sameArgs(args, []string{"--gid", "app", "--groups", "", "--", "worker"}) {
						t.Fatalf("unexpected command %s %#v", name, args)
					}
					g := h.groups["extra"]
					g.members = nil
					h.groups["extra"] = g
					return nil
				}
			}
			if err = execute(t, b, a); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNSSConflictsAndImmutableIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*host)
		c     inventory.Config
	}{
		{"external user name", func(h *host) { h.externalUsers["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", ""} }, inventory.Config{Type: "user", User: "worker", Group: "app"}},
		{"external user ID", func(h *host) { h.externalUsers["remote"] = user{"remote", 3100, 2100, "/srv/state", "/bin/sh", ""} }, inventory.Config{Type: "user", User: "worker", Group: "app", UID: ptr(3100)}},
		{"local duplicate ID", func(h *host) { h.users["other"] = user{"other", 3100, 2100, "/srv/state", "/bin/sh", ""} }, inventory.Config{Type: "user", User: "worker", Group: "app", UID: ptr(3100)}},
		{"external primary group", func(h *host) { delete(h.groups, "app"); h.externalGroups["app"] = group{"app", 2100, nil} }, inventory.Config{Type: "user", User: "worker", Group: "app"}},
		{"existing UID", func(h *host) { h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", ""} }, inventory.Config{Type: "user", User: "worker", Group: "app", UID: ptr(3101)}},
		{"existing GID", func(h *host) {}, inventory.Config{Type: "group", Group: "app", GID: ptr(2101)}},
		{"external group name", func(h *host) { h.externalGroups["app"] = group{"app", 9000, nil} }, inventory.Config{Type: "group", Group: "app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture()
			tc.setup(h)
			if _, _, err := h.backend().Prepare(context.Background(), tc.c, "present", nil); err == nil {
				t.Fatal("conflict accepted")
			}
			if len(h.mutations()) != 0 {
				t.Fatal("preparation mutated accounts")
			}
		})
	}
}

func TestPrecedingGroupCreationAndProjectedRemoval(t *testing.T) {
	h := fixture()
	b := h.backend()
	p := &Preparation{}
	g, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "future"}, "present", p)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "future"}, "present", p)
	if err != nil {
		t.Fatal(err)
	}
	if id, exists, known := p.Group("future"); id != nil || !exists || !known {
		t.Fatalf("projected group: %v %v %v", id, exists, known)
	}
	if len(a.Groups) != 1 || a.Groups[0].GID != nil {
		t.Fatalf("automatic group GID not deferred: %#v", a.Groups)
	}
	if err = execute(t, b, a); err == nil {
		t.Fatal("user accepted missing preceding group")
	}
	h.mutate = func(name string, args []string) error {
		if name != "groupadd" {
			t.Fatal(name)
		}
		h.groups["future"] = group{"future", 2200, nil}
		return nil
	}
	if err = execute(t, b, g); err != nil {
		t.Fatal(err)
	}
	h.mutate = func(name string, args []string) error {
		if name != "useradd" {
			t.Fatal(name)
		}
		h.users["worker"] = user{"worker", 3200, 2200, "/nonexistent", "/usr/sbin/nologin", ""}
		return nil
	}
	if err = execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	p = &Preparation{}
	if _, _, err = b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "future"}, "absent", p); err == nil {
		t.Fatal("primary group deletion accepted")
	}
	if _, _, err = b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "future"}, "absent", p); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "future"}, "absent", p); err != nil {
		t.Fatalf("preceding user removal not projected: %v", err)
	}
}

func TestFrozenIdentityAndPartialRetry(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "old"}
	b := h.backend()
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", Comment: str("new")}, "present", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.mutate = func(_ string, _ []string) error {
		u := h.users["worker"]
		u.comment = "new"
		h.users["worker"] = u
		return errors.New("lost command reply")
	}
	if err = execute(t, b, a); err == nil {
		t.Fatal("mutation failure hidden")
	}
	if err = execute(t, b, a); err != nil {
		t.Fatalf("partially completed retry: %v", err)
	}
	if len(h.mutations()) != 1 {
		t.Fatal("retry mutated already matching result")
	}
	u := h.users["worker"]
	u.uid = 3101
	h.users["worker"] = u
	if err = execute(t, b, a); err == nil {
		t.Fatal("replacement UID accepted")
	}
	u.uid = 3100
	h.users["worker"] = u
	g := h.groups["app"]
	g.gid = 2102
	h.groups["app"] = g
	if err = execute(t, b, a); err == nil {
		t.Fatal("replacement dependency GID accepted")
	}
}

func TestDeletionPreservesHomeAndSameNameGroup(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2200, "/srv/worker", "/bin/sh", ""}
	h.groups["worker"] = group{"worker", 2200, nil}
	b := h.backend()
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "worker"}, "absent", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.mutate = func(name string, args []string) error {
		if name != "unshare" || !sameArgs(args, []string{"--mount", "--propagation", "private", "sh", "-c", deleteUserScript, "richt-account-delete", "worker"}) {
			t.Fatalf("unsafe deletion command: %s %#v", name, args)
		}
		if strings.Contains(deleteUserScript, "userdel -r") || strings.Contains(deleteUserScript, "--remove") {
			t.Fatal("home removal enabled")
		}
		delete(h.users, "worker")
		return nil
	}
	if err = execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.groups["worker"]; !ok {
		t.Fatal("same-name group removed")
	}
	if err = execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	if len(h.mutations()) != 1 {
		t.Fatal("repeated deletion")
	}
}

func TestFailedProbeAndPostcondition(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/worker", "/bin/sh", ""}
	h.fail = "unshare"
	if _, _, err := h.backend().Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app"}, "absent", nil); err == nil {
		t.Fatal("unsupported deletion namespace accepted")
	}
	h = fixture()
	b := h.backend()
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "new"}, "present", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.mutate = func(string, []string) error { return nil }
	if err = execute(t, b, a); err == nil {
		t.Fatal("false successful mutation accepted")
	}
}

func TestFrozenOperationsRejectUnplannedBranches(t *testing.T) {
	t.Run("create conflicts", func(t *testing.T) {
		h := fixture()
		b := h.backend()
		a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app"}, "present", nil)
		if err != nil {
			t.Fatal(err)
		}
		h.users["worker"] = user{"worker", 3100, 2100, "/unexpected", "/bin/sh", ""}
		if err = execute(t, b, a); err == nil {
			t.Fatal("create silently adopted or updated an unexpected user")
		}
		if len(h.mutations()) != 0 {
			t.Fatal("conflict mutated account")
		}
	})
	t.Run("update missing", func(t *testing.T) {
		h := fixture()
		h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "old"}
		b := h.backend()
		a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", Comment: str("new")}, "present", nil)
		if err != nil {
			t.Fatal(err)
		}
		delete(h.users, "worker")
		if err = execute(t, b, a); err == nil {
			t.Fatal("update silently recreated removed user")
		}
		if len(h.mutations()) != 0 {
			t.Fatal("missing update target mutated account")
		}
	})
	t.Run("absent noop appeared", func(t *testing.T) {
		h := fixture()
		b := h.backend()
		a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "new"}, "absent", nil)
		if err != nil {
			t.Fatal(err)
		}
		h.groups["new"] = group{"new", 2200, nil}
		if err = execute(t, b, a); err == nil {
			t.Fatal("noop silently removed newly appeared group")
		}
		if len(h.mutations()) != 0 {
			t.Fatal("noop mutated account")
		}
	})
	t.Run("delete changed identity", func(t *testing.T) {
		h := fixture()
		b := h.backend()
		a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "extra"}, "absent", nil)
		if err != nil {
			t.Fatal(err)
		}
		h.groups["extra"] = group{"extra", 2200, nil}
		if err = execute(t, b, a); err == nil {
			t.Fatal("delete accepted replacement group")
		}
		if len(h.mutations()) != 0 {
			t.Fatal("replacement group mutated")
		}
	})
}

func TestAccountResolverRejectsForeignPayloadAndShowsCommands(t *testing.T) {
	h := fixture()
	b := h.backend()
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", Comment: str("Worker account")}, "present", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []plan.Task{{Operation: "file", Account: a}, {Operation: "account", Account: a, File: &plan.FileChange{}}} {
		if _, err := b.Resolve(task); err == nil {
			t.Fatal("invalid task accepted")
		}
	}
	actions, err := b.Resolve(plan.Task{Operation: "account", Account: a})
	if err != nil {
		t.Fatal(err)
	}
	display := strings.Join(actions[0].Description, "\n")
	if !strings.Contains(display, `execute useradd "--no-user-group"`) || !strings.Contains(display, `"--comment" "Worker account"`) || !strings.Contains(display, "require local group app GID=2100") {
		t.Fatalf("incomplete account display: %s", display)
	}
}

func TestExistingAccountsWithOmittedIDsRejectNumericConflictsDuringPreparation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c     inventory.Config
		setup func(*host)
	}{
		{"duplicate local UID", inventory.Config{Type: "user", User: "worker", Group: "app"}, func(h *host) {
			h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", ""}
			h.users["other"] = user{"other", 3100, 2100, "/srv/other", "/bin/sh", ""}
		}},
		{"external UID", inventory.Config{Type: "user", User: "worker", Group: "app"}, func(h *host) {
			h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", ""}
			h.externalUsers["remote"] = user{"remote", 3100, 2100, "/srv/remote", "/bin/sh", ""}
		}},
		{"duplicate local GID", inventory.Config{Type: "group", Group: "app"}, func(h *host) { h.groups["other"] = group{"other", 2100, nil} }},
		{"external GID", inventory.Config{Type: "group", Group: "app"}, func(h *host) { h.externalGroups["remote"] = group{"remote", 2100, nil} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture()
			tc.setup(h)
			if _, _, err := h.backend().Prepare(context.Background(), tc.c, "present", nil); err == nil {
				t.Fatal("numeric conflict accepted with omitted ID")
			}
			if len(h.mutations()) != 0 {
				t.Fatal("preparation mutated account")
			}
		})
	}
}

func boolValue(value bool) *bool { return &value }
func (h *host) addMembership(groupName, userName string) {
	g := h.groups[groupName]
	g.members = append(g.members, userName)
	h.groups[groupName] = g
}

func TestSupplementaryMembershipModes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		groups       *[]string
		appendGroups *bool
		changed      bool
	}{
		{"omitted", nil, nil, false},
		{"exact empty", list(), nil, true},
		{"explicit exact empty", list(), boolValue(false), true},
		{"exact requested subset", list("extra"), boolValue(false), true},
		{"append empty", list(), boolValue(true), false},
		{"append existing subset", list("extra"), boolValue(true), false},
		{"append missing", list("app"), boolValue(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture()
			h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
			h.groups["other"] = group{"other", 2102, []string{"worker"}}
			h.addMembership("extra", "worker")
			a, changed, err := h.backend().Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: tc.groups, AppendGroups: tc.appendGroups}, "present", nil)
			if err != nil || changed != tc.changed {
				t.Fatalf("prepare: changed=%t err=%v", changed, err)
			}
			if (a.AppendGroups == nil) != (tc.appendGroups == nil) || a.AppendGroups != nil && *a.AppendGroups != *tc.appendGroups {
				t.Fatal("membership mode not frozen")
			}
			if tc.name == "append missing" && a.Operation != "update" {
				t.Fatal("primary membership incorrectly counted as supplementary")
			}
			if !changed && execute(t, h.backend(), a) != nil {
				t.Fatal("matching noop failed")
			}
		})
	}
}

func TestAppendPreservesMembershipsAddedAfterPreparationAndInterruptedUpdate(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
	h.groups["before"] = group{"before", 2102, []string{"worker"}}
	b := h.backend()
	a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("extra"), AppendGroups: boolValue(true)}, "present", nil)
	if err != nil || !changed {
		t.Fatalf("prepare: %t %v", changed, err)
	}
	h.groups["after"] = group{"after", 2103, []string{"worker"}}
	h.mutate = func(name string, args []string) error {
		if name != "usermod" || !sameArgs(args, []string{"--gid", "app", "--append", "--groups", "extra", "--", "worker"}) {
			t.Fatalf("unsafe additive command: %s %#v", name, args)
		}
		h.addMembership("extra", "worker")
		return errors.New("lost reply after update")
	}
	actions, err := b.Resolve(plan.Task{Operation: "account", Account: a})
	if err != nil {
		t.Fatal(err)
	}
	display := strings.Join(actions[0].Description, "\n")
	if !strings.Contains(display, "append local supplementary groups (preserve other memberships): extra") || !strings.Contains(display, `"--append" "--groups" "extra"`) {
		t.Fatalf("mode missing from display: %s", display)
	}
	if err := execute(t, b, a); err == nil {
		t.Fatal("command error hidden")
	}
	if err := execute(t, b, a); err != nil {
		t.Fatalf("interrupted update retry: %v", err)
	}
	if len(h.mutations()) != 1 {
		t.Fatal("matching retry repeated mutation")
	}
	if changed, err := b.Observe(context.Background(), a); changed || err != nil {
		t.Fatalf("subset verification: %t %v", changed, err)
	}
	s, err := b.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(memberships(s, "worker"), []string{"before", "after", "extra"}) {
		t.Fatalf("unrequested memberships lost: %#v", memberships(s, "worker"))
	}
}

func TestAppendEmptyUpdateChangesOnlyOtherAttributes(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
	h.addMembership("extra", "worker")
	b := h.backend()
	a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", Comment: str("new"), SupplementaryGroups: list(), AppendGroups: boolValue(true)}, "present", nil)
	if err != nil || !changed {
		t.Fatalf("prepare: %t %v", changed, err)
	}
	h.mutate = func(name string, args []string) error {
		if name != "usermod" || !sameArgs(args, []string{"--gid", "app", "--comment", "new", "--", "worker"}) {
			t.Fatalf("empty append changed memberships: %s %#v", name, args)
		}
		u := h.users["worker"]
		u.comment = "new"
		h.users["worker"] = u
		return nil
	}
	if err := execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	if len(h.groups["extra"].members) != 1 {
		t.Fatal("empty append removed membership")
	}
}

func TestAdditiveCreationAndRetry(t *testing.T) {
	for _, groups := range [][]string{{}, {"extra"}} {
		t.Run(strings.Join(groups, ","), func(t *testing.T) {
			h := fixture()
			b := h.backend()
			a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: &groups, AppendGroups: boolValue(true)}, "present", nil)
			if err != nil || !changed || a.AppendGroups == nil || !*a.AppendGroups || a.SupplementaryGroups == nil {
				t.Fatalf("prepare: %#v %t %v", a, changed, err)
			}
			h.mutate = func(name string, args []string) error {
				want := []string{"--no-user-group", "--no-create-home", "--password", "!", "--gid", "app", "--home", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "", "--groups", strings.Join(groups, ","), "--", "worker"}
				if name != "useradd" || !sameArgs(args, want) {
					t.Fatalf("creation mode command: %s %#v", name, args)
				}
				h.users["worker"] = user{"worker", 3100, 2100, "/nonexistent", "/usr/sbin/nologin", ""}
				for _, g := range groups {
					h.addMembership(g, "worker")
				}
				return errors.New("lost reply after create")
			}
			if err := execute(t, b, a); err == nil {
				t.Fatal("creation failure hidden")
			}
			h.groups["unrelated"] = group{"unrelated", 2102, []string{"worker"}}
			if err := execute(t, b, a); err != nil {
				t.Fatalf("additive creation retry with extra membership: %v", err)
			}
			if len(h.mutations()) != 1 {
				t.Fatal("creation retry mutated account")
			}
			if len(groups) > 0 {
				g := h.groups[groups[0]]
				g.members = nil
				h.groups[groups[0]] = g
				if err := execute(t, b, a); err == nil {
					t.Fatal("creation retry accepted missing requested membership")
				}
			}
		})
	}
}

func TestAppendGroupsKeepsLocalDependenciesAndProjection(t *testing.T) {
	for _, setup := range []func(*host){
		func(h *host) { delete(h.groups, "extra") },
		func(h *host) { delete(h.groups, "extra"); h.externalGroups["extra"] = group{"extra", 2101, nil} },
		func(h *host) { h.externalGroups["remote"] = group{"remote", 2101, nil} },
	} {
		h := fixture()
		setup(h)
		if _, _, err := h.backend().Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("extra"), AppendGroups: boolValue(true)}, "present", nil); err == nil {
			t.Fatal("invalid additive local dependency accepted")
		}
		if len(h.mutations()) != 0 {
			t.Fatal("preparation mutated accounts")
		}
	}
	h := fixture()
	b := h.backend()
	p := &Preparation{}
	if _, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "future"}, "present", p); err != nil {
		t.Fatal(err)
	}
	a, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("future"), AppendGroups: boolValue(true)}, "present", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Groups) != 2 || a.Groups[1].Name != "future" || a.Groups[1].GID != nil {
		t.Fatalf("additive group projection lost: %#v", a.Groups)
	}
	p = &Preparation{}
	if _, _, err := b.Prepare(context.Background(), inventory.Config{Type: "group", Group: "extra"}, "absent", p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("extra"), AppendGroups: boolValue(true)}, "present", p); err == nil {
		t.Fatal("projected removed supplementary group accepted")
	}
}

func TestDeletionDiscardsMembershipIntent(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
	a, _, err := h.backend().Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("missing"), AppendGroups: boolValue(true)}, "absent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.AppendGroups != nil || a.SupplementaryGroups != nil || len(a.Groups) != 0 {
		t.Fatalf("deletion retained membership operation: %#v", a)
	}
}

func TestExplicitFalseReplacesMembershipsExactly(t *testing.T) {
	h := fixture()
	h.users["worker"] = user{"worker", 3100, 2100, "/srv/state", "/bin/sh", "existing"}
	h.groups["unrelated"] = group{"unrelated", 2102, []string{"worker"}}
	b := h.backend()
	a, changed, err := b.Prepare(context.Background(), inventory.Config{Type: "user", User: "worker", Group: "app", SupplementaryGroups: list("extra"), AppendGroups: boolValue(false)}, "present", nil)
	if err != nil || !changed {
		t.Fatalf("prepare: %t %v", changed, err)
	}
	h.mutate = func(name string, args []string) error {
		if name != "usermod" || !sameArgs(args, []string{"--gid", "app", "--groups", "extra", "--", "worker"}) {
			t.Fatalf("unexpected exact command: %s %#v", name, args)
		}
		h.addMembership("extra", "worker")
		g := h.groups["unrelated"]
		g.members = nil
		h.groups["unrelated"] = g
		return nil
	}
	actions, err := b.Resolve(plan.Task{Operation: "account", Account: a})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(actions[0].Description, "\n"), "exact local supplementary groups: extra") {
		t.Fatal("explicit false display lost exact mode")
	}
	if err := execute(t, b, a); err != nil {
		t.Fatal(err)
	}
	if changed, err := b.Observe(context.Background(), a); changed || err != nil {
		t.Fatalf("exact result: %t %v", changed, err)
	}
	h.addMembership("unrelated", "worker")
	if changed, err := b.Observe(context.Background(), a); !changed || err != nil {
		t.Fatalf("exact mode accepted extra membership: %t %v", changed, err)
	}
}
