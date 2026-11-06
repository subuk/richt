package inventory

import (
	"strings"
	"testing"
)

func accountID(value uint32) *uint32 { return &value }

func TestAccountConfigValidation(t *testing.T) {
	empty := ""
	groups := []string{}
	for _, config := range []Config{
		{Type: "group", Group: "app", GID: accountID(4294967294)},
		{Type: "user", User: "app-user", Group: "root"},
		{Type: "user", User: "_app$", Group: "app", UID: accountID(1001), Home: "/nonexistent", Shell: "/usr/sbin/nologin", Comment: &empty, SupplementaryGroups: &groups},
	} {
		if err := validateConfig(config); err != nil {
			t.Fatalf("valid config rejected: %#v: %v", config, err)
		}
	}
	for _, config := range []Config{
		{Type: "user", User: "app"},
		{Type: "group", Group: "root"},
		{Type: "group", Group: "app", GID: accountID(0)},
		{Type: "group", Group: "app", GID: accountID(4294967295)},
		{Type: "group", Group: "app", UID: accountID(1001)},
		{Type: "group", Group: "app", Comment: &empty},
		{Type: "user", User: "root", Group: "app"},
		{Type: "user", User: "app", Group: "app", UID: accountID(0)},
		{Type: "user", User: "app", Group: "app", UID: accountID(4294967295)},
		{Type: "user", User: "app", Group: "app", GID: accountID(1001)},
		{Type: "user", User: "-option", Group: "app"},
		{Type: "user", User: strings.Repeat("a", 33), Group: "app"},
		{Type: "user", User: "app", Group: "1"},
		{Type: "user", User: "app", Group: "app", Home: "/srv/../home"},
		{Type: "user", User: "app", Group: "app", Shell: "/"},
		{Type: "user", User: "app", Group: "app", Home: "/bad:name"},
		{Type: "user", User: "app", Group: "app", Path: "/etc/passwd"},
		{Type: "file", Path: "/tmp/file", UID: accountID(1001)},
	} {
		if err := validateConfig(config); err == nil {
			t.Fatalf("invalid config accepted: %#v", config)
		}
	}
	for _, comment := range []string{"bad:comment", "bad\ncomment", "bad\tcomment", "bad\x00comment"} {
		if err := validateConfig(Config{Type: "user", User: "app", Group: "app", Comment: &comment}); err == nil {
			t.Fatalf("invalid comment accepted: %q", comment)
		}
	}
	for _, groups := range [][]string{{"app", "app"}, {"-bad"}, {"bad:group"}} {
		if err := validateConfig(Config{Type: "user", User: "app", Group: "app", SupplementaryGroups: &groups}); err == nil {
			t.Fatalf("invalid groups accepted: %q", groups)
		}
	}
}

func TestAccountOwnershipBeforeServiceFiltering(t *testing.T) {
	for _, pair := range [][2]Config{
		{{Type: "user", User: "app", Group: "app"}, {Type: "user", User: "app", Group: "other"}},
		{{Type: "group", Group: "app"}, {Type: "group", Group: "app"}},
		{{Type: "user", User: "app", Group: "app", UID: accountID(1001)}, {Type: "user", User: "other", Group: "app", UID: accountID(1001)}},
		{{Type: "group", Group: "app", GID: accountID(1001)}, {Type: "group", Group: "other", GID: accountID(1001)}},
	} {
		for _, sameService := range []bool{false, true} {
			in := fixture()
			if sameService {
				setService(in, "app", Service{Configs: []Config{pair[0], pair[1]}})
			} else {
				setService(in, "app", Service{Configs: []Config{pair[0]}})
				setService(in, "repo", Service{Configs: []Config{pair[1]}})
			}
			if _, err := in.Resolve(Selectors{Hosts: []string{"lanst"}, Services: []string{"app"}}); err == nil {
				t.Fatalf("conflicting accounts accepted: %#v", pair)
			}
		}
	}
	in := fixture()
	setService(in, "app", Service{Configs: []Config{
		{Type: "group", Group: "app", GID: accountID(1001)},
		{Type: "user", User: "app", Group: "app", UID: accountID(1001)},
	}})
	if _, err := in.Resolve(Selectors{}); err != nil {
		t.Fatalf("independent user/group namespaces conflict: %v", err)
	}
}

func TestAccountsConflictWithWholeAccountDatabaseOwnership(t *testing.T) {
	for _, database := range []string{"/etc/passwd", "/etc/shadow", "/etc/group", "/etc/gshadow"} {
		for _, account := range []Config{{Type: "user", User: "app", Group: "app"}, {Type: "group", Group: "app"}} {
			in := fixture()
			setService(in, "app", Service{Configs: []Config{account}})
			setService(in, "repo", Service{Configs: []Config{{Type: "file", Path: database}}})
			_, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}})
			if err == nil || !strings.Contains(err.Error(), database) {
				t.Fatalf("database conflict hidden for %s: %v", database, err)
			}
		}
	}
}

func TestAppendGroupsValidation(t *testing.T) {
	for _, value := range []bool{false, true} {
		for _, groups := range [][]string{{}, {"extra"}} {
			if err := validateConfig(Config{Type: "user", User: "app", Group: "app", SupplementaryGroups: &groups, AppendGroups: &value}); err != nil {
				t.Fatalf("valid mode %t: %v", value, err)
			}
		}
		for _, c := range []Config{
			{Type: "user", User: "app", Group: "app", AppendGroups: &value},
			{Type: "group", Group: "app", AppendGroups: &value},
			{Type: "file", Path: "/tmp/file", AppendGroups: &value},
			{Type: "directory", Path: "/tmp/dir", AppendGroups: &value},
			{Type: "mount", Source: "/dev/test", Target: "/mnt/test", AppendGroups: &value},
		} {
			if err := validateConfig(c); err == nil {
				t.Fatalf("invalid mode accepted: %#v", c)
			}
		}
	}
}
