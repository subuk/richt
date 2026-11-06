// Package accounts prepares and executes local Linux user and group changes.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

type Runner func(context.Context, string, ...string) ([]byte, error)
type Backend struct{ Runner Runner }

func (b Backend) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.Runner != nil {
		return b.Runner(ctx, name, args...)
	}
	return rpm.Run(ctx, name, args...)
}

type user struct {
	name                 string
	uid, gid             uint32
	home, shell, comment string
}
type group struct {
	name    string
	gid     uint32
	members []string
}
type snapshot struct {
	users  map[string]user
	groups map[string]group
}
type projected struct {
	id     *uint32
	exists bool
}

// Preparation retains only preceding declared account results on one host.
// A nil ID with exists=true represents automatic ID allocation during apply.
type Preparation struct {
	users, groups map[string]projected
	userPrimary   map[string]string
}

func (p *Preparation) User(name string) (*uint32, bool, bool) {
	if p == nil {
		return nil, false, false
	}
	v, ok := p.users[name]
	return v.id, v.exists, ok
}
func (p *Preparation) Group(name string) (*uint32, bool, bool) {
	if p == nil {
		return nil, false, false
	}
	v, ok := p.groups[name]
	return v.id, v.exists, ok
}
func ptr(v uint32) *uint32 { return &v }

func (b Backend) Check(ctx context.Context) error {
	out, err := b.run(ctx, "uname", "-s")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "Linux" {
		return errors.New("local accounts require Linux")
	}
	_, err = b.read(ctx)
	return err
}

func parseID(s string) (uint32, error) { n, e := strconv.ParseUint(s, 10, 32); return uint32(n), e }
func parseUser(line string) (user, error) {
	f := strings.Split(line, ":")
	if len(f) != 7 || f[0] == "" {
		return user{}, errors.New("invalid passwd response")
	}
	uid, e := parseID(f[2])
	if e != nil {
		return user{}, errors.New("invalid passwd UID")
	}
	gid, e := parseID(f[3])
	if e != nil {
		return user{}, errors.New("invalid passwd GID")
	}
	return user{f[0], uid, gid, f[5], f[6], f[4]}, nil
}
func parseGroup(line string) (group, error) {
	f := strings.Split(line, ":")
	if len(f) != 4 || f[0] == "" {
		return group{}, errors.New("invalid group response")
	}
	gid, e := parseID(f[2])
	if e != nil {
		return group{}, errors.New("invalid group GID")
	}
	var members []string
	if f[3] != "" {
		members = strings.Split(f[3], ",")
	}
	return group{f[0], gid, members}, nil
}
func (b Backend) read(ctx context.Context) (snapshot, error) {
	s := snapshot{map[string]user{}, map[string]group{}}
	for _, db := range []string{"passwd", "group"} {
		out, err := b.run(ctx, "getent", "-s", "files", db)
		if err != nil {
			return s, fmt.Errorf("read local %s database: %w", db, err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
			if line == "" {
				continue
			}
			if db == "passwd" {
				v, e := parseUser(line)
				if e != nil {
					return s, e
				}
				if _, ok := s.users[v.name]; ok {
					return s, errors.New("duplicate local user name")
				}
				s.users[v.name] = v
			} else {
				v, e := parseGroup(line)
				if e != nil {
					return s, e
				}
				if _, ok := s.groups[v.name]; ok {
					return s, errors.New("duplicate local group name")
				}
				s.groups[v.name] = v
			}
		}
	}
	return s, nil
}

func (b Backend) lookup(ctx context.Context, db, key string) (string, error) {
	out, err := b.run(ctx, "getent", db, key)
	if err != nil {
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return "", nil
		}
		return "", fmt.Errorf("lookup %s: %w", db, err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

func (b Backend) userNSS(ctx context.Context, s snapshot, name string, id *uint32) error {
	keys := []string{name}
	if id != nil {
		keys = append(keys, strconv.FormatUint(uint64(*id), 10))
	}
	for _, key := range keys {
		line, err := b.lookup(ctx, "passwd", key)
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		v, err := parseUser(line)
		if err != nil {
			return err
		}
		local, ok := s.users[v.name]
		if !ok || local.uid != v.uid || v.name != name {
			return fmt.Errorf("user %s conflicts with an NSS account", name)
		}
	}
	if id != nil {
		for _, v := range s.users {
			if v.uid == *id && v.name != name {
				return fmt.Errorf("UID %d belongs to another local user", *id)
			}
		}
	}
	return nil
}
func (b Backend) groupNSS(ctx context.Context, s snapshot, name string, id *uint32) error {
	keys := []string{name}
	if id != nil {
		keys = append(keys, strconv.FormatUint(uint64(*id), 10))
	}
	for _, key := range keys {
		line, err := b.lookup(ctx, "group", key)
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		v, err := parseGroup(line)
		if err != nil {
			return err
		}
		local, ok := s.groups[v.name]
		if !ok || local.gid != v.gid || v.name != name {
			return fmt.Errorf("group %s conflicts with an NSS account", name)
		}
	}
	if id != nil {
		for _, v := range s.groups {
			if v.gid == *id && v.name != name {
				return fmt.Errorf("GID %d belongs to another local group", *id)
			}
		}
	}
	return nil
}

func memberships(s snapshot, name string) []string {
	var out []string
	for _, g := range s.groups {
		for _, m := range g.members {
			if m == name {
				out = append(out, g.name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
func sameStrings(a, b []string) bool {
	aa := append([]string{}, a...)
	bb := append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return strings.Join(aa, "\x00") == strings.Join(bb, "\x00")
}

func (b Backend) Prepare(ctx context.Context, c inventory.Config, state string, p *Preparation) (*plan.AccountChange, bool, error) {
	if err := inventory.ValidateAccountConfig(c); err != nil {
		return nil, false, err
	}
	if state == "" {
		state = "present"
	}
	kind := c.Type
	name := c.User
	if kind == "group" {
		name = c.Group
	}
	a := &plan.AccountChange{Kind: kind, Name: name, State: state, Operation: "noop"}
	s, err := b.read(ctx)
	if err != nil {
		return nil, false, err
	}
	if p != nil {
		entries := p.users
		id := c.UID
		if kind == "group" {
			entries = p.groups
			id = c.GID
		}
		for previous, v := range entries {
			if previous == name {
				return nil, false, errors.New("account already declared in preparation")
			}
			if v.exists && id != nil && v.id != nil && *id == *v.id {
				return nil, false, errors.New("account numeric ID conflicts with a preceding declaration")
			}
		}
	}
	if kind == "group" {
		id := c.GID
		if id == nil {
			if observed, exists := s.groups[name]; exists {
				id = ptr(observed.gid)
			}
		}
		if err = b.groupNSS(ctx, s, name, id); err != nil {
			return nil, false, err
		}
		g, exists := s.groups[name]
		if exists {
			a.Expected = &plan.AccountIdentity{GID: g.gid}
			if c.GID != nil && *c.GID != g.gid {
				return nil, false, errors.New("changing an existing group GID is unsupported")
			}
		}
		if state == "absent" {
			if exists {
				a.Operation = "delete"
				if err = protectPrimary(s, g, p); err != nil {
					return nil, false, err
				}
			}
		} else {
			a.GID = c.GID
			if !exists {
				a.Operation = "create"
				a.System = c.System
			}
		}
	} else if kind == "user" {
		id := c.UID
		if id == nil {
			if observed, exists := s.users[name]; exists {
				id = ptr(observed.uid)
			}
		}
		if err = b.userNSS(ctx, s, name, id); err != nil {
			return nil, false, err
		}
		u, exists := s.users[name]
		if exists {
			a.Expected = &plan.AccountIdentity{UID: u.uid, GID: u.gid}
			if c.UID != nil && *c.UID != u.uid {
				return nil, false, errors.New("changing an existing user UID is unsupported")
			}
		}
		if state == "absent" {
			if exists {
				a.Operation = "delete"
			}
		} else {
			a.UID = c.UID
			a.PrimaryGroup = c.Group
			a.Home = c.Home
			a.Shell = c.Shell
			a.Comment = c.Comment
			a.SupplementaryGroups = c.SupplementaryGroups
			a.AppendGroups = c.AppendGroups
			if !exists {
				a.Operation = "create"
				a.System = c.System
				if a.PrimaryGroup == "" {
					return nil, false, errors.New("new user requires an explicit primary group")
				}
				if a.Home == "" {
					a.Home = "/nonexistent"
				}
				if a.Shell == "" {
					a.Shell = "/usr/sbin/nologin"
				}
				if a.Comment == nil {
					empty := ""
					a.Comment = &empty
				}
				if a.SupplementaryGroups == nil {
					empty := []string{}
					a.SupplementaryGroups = &empty
				}
			}
			refs := map[string]bool{}
			if a.PrimaryGroup != "" {
				refs[a.PrimaryGroup] = true
			}
			if a.SupplementaryGroups != nil {
				for _, g := range *a.SupplementaryGroups {
					refs[g] = true
				}
			}
			for g := range refs {
				id, present, known := p.Group(g)
				if !known {
					v, ok := s.groups[g]
					if !ok {
						return nil, false, fmt.Errorf("required local group %s does not exist or is declared later", g)
					}
					id = ptr(v.gid)
					present = true
				}
				if !present {
					return nil, false, fmt.Errorf("required group %s was removed earlier", g)
				}
				if err = b.groupNSS(ctx, s, g, id); err != nil {
					return nil, false, err
				}
				a.Groups = append(a.Groups, plan.AccountGroup{Name: g, GID: id})
			}
			sort.Slice(a.Groups, func(i, j int) bool { return a.Groups[i].Name < a.Groups[j].Name })
			if exists && !matches(s, a) {
				a.Operation = "update"
			}
		}
	} else {
		return nil, false, errors.New("unsupported account kind")
	}
	if err = a.Validate(); err != nil {
		return nil, false, err
	}
	if a.Kind == "user" && a.Operation == "delete" {
		if err = b.checkDelete(ctx); err != nil {
			return nil, false, err
		}
	}
	if a.Operation != "noop" && !(a.Kind == "user" && a.Operation == "delete") {
		name, _ := command(a)
		if a.Operation == "delete" {
			name = "groupdel"
		}
		if _, err = b.run(ctx, "sh", "-c", `command -v "$1" >/dev/null`, "richt-account-check", name); err != nil {
			return nil, false, fmt.Errorf("required account executable %s is unavailable: %w", name, err)
		}
	}
	if p != nil {
		if p.users == nil {
			p.users = map[string]projected{}
			p.groups = map[string]projected{}
			p.userPrimary = map[string]string{}
		}
		id := a.UID
		if kind == "group" {
			id = a.GID
			if a.Expected != nil {
				id = ptr(a.Expected.GID)
			}
			p.groups[name] = projected{id, state == "present"}
		} else {
			if a.Expected != nil {
				id = ptr(a.Expected.UID)
			}
			p.users[name] = projected{id, state == "present"}
			p.userPrimary[name] = a.PrimaryGroup
		}
	}
	return a, a.Operation != "noop", nil
}

func protectPrimary(s snapshot, g group, p *Preparation) error {
	for _, u := range s.users {
		if u.gid != g.gid {
			continue
		}
		_, present, known := p.User(u.name)
		if known && !present {
			continue
		}
		if p != nil {
			if primary := p.userPrimary[u.name]; primary != "" && primary != g.name {
				continue
			}
		}
		return fmt.Errorf("group %s is primary for local user %s", g.name, u.name)
	}
	if p != nil {
		for name, gname := range p.userPrimary {
			_, present, _ := p.User(name)
			if present && gname == g.name {
				return fmt.Errorf("group %s is primary for preceding user %s", g.name, name)
			}
		}
	}
	return nil
}

func desiredGID(a *plan.AccountChange, s snapshot) (uint32, bool) {
	if a.PrimaryGroup == "" {
		return 0, false
	}
	g, ok := s.groups[a.PrimaryGroup]
	return g.gid, ok
}
func matches(s snapshot, a *plan.AccountChange) bool {
	if a.Kind == "group" {
		g, ok := s.groups[a.Name]
		return ok && (a.GID == nil || g.gid == *a.GID)
	}
	u, ok := s.users[a.Name]
	if !ok {
		return false
	}
	if a.UID != nil && u.uid != *a.UID {
		return false
	}
	if a.PrimaryGroup != "" {
		gid, ok := desiredGID(a, s)
		if !ok || u.gid != gid {
			return false
		}
	}
	if a.Home != "" && a.Home != u.home {
		return false
	}
	if a.Shell != "" && a.Shell != u.shell {
		return false
	}
	if a.Comment != nil && *a.Comment != u.comment {
		return false
	}
	if a.SupplementaryGroups == nil {
		return true
	}
	actual := memberships(s, a.Name)
	if a.AppendGroups != nil && *a.AppendGroups {
		for _, requested := range *a.SupplementaryGroups {
			found := false
			for _, name := range actual {
				if name == requested {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return sameStrings(*a.SupplementaryGroups, actual)
}
