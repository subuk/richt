package accounts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/subuk/richt/plan"
)

// userdel consults login.defs but has no per-command USERGROUPS_ENAB override.
// Bind a private policy file only inside a private mount namespace, preserving
// both the host's policy and every group. Never use userdel's home-removal flag.
const deleteUserScript = `set -eu
umask 077
policy=$(mktemp /tmp/richt-account-policy.XXXXXXXXXX)
trap 'rm -f -- "$policy"' EXIT HUP INT TERM
awk '$1 != "USERGROUPS_ENAB" { print } END { print "USERGROUPS_ENAB no" }' /etc/login.defs > "$policy"
mount --bind "$policy" /etc/login.defs
userdel -- "$1"
`

const deleteProbeScript = `set -eu
for tool in userdel mount mktemp awk rm; do command -v "$tool" >/dev/null; done
test -f /etc/login.defs
umask 077
policy=$(mktemp /tmp/richt-account-probe.XXXXXXXXXX)
trap 'rm -f -- "$policy"' EXIT HUP INT TERM
mount --bind "$policy" /etc/login.defs
`

func (b Backend) checkDelete(ctx context.Context) error {
	_, err := b.run(ctx, "unshare", "--mount", "--propagation", "private", "sh", "-c", deleteProbeScript, "richt-account-probe")
	if err != nil {
		return fmt.Errorf("user deletion requires a private mount namespace and shadow-utils: %w", err)
	}
	return nil
}

func Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "account" || t.Account == nil || t.File != nil || t.RPM != nil || t.Artifact != nil || t.Process != nil || t.Oneshot != nil || t.Path != nil || t.Storage != nil || t.Mount != nil {
		return nil, errors.New("invalid account task")
	}
	a := t.Account
	if err := a.Validate(); err != nil {
		return nil, err
	}
	description := []string{fmt.Sprintf("%s local %s %s (%s)", a.Operation, a.Kind, a.Name, a.State)}
	if a.UID != nil {
		description = append(description, fmt.Sprintf("UID %d", *a.UID))
	}
	if a.GID != nil {
		description = append(description, fmt.Sprintf("GID %d", *a.GID))
	}
	if a.Operation == "create" {
		if (a.Kind == "user" && a.UID == nil) || (a.Kind == "group" && a.GID == nil) {
			description = append(description, "numeric ID allocated by shadow-utils at apply")
		}
		if a.System != nil {
			description = append(description, fmt.Sprintf("system allocation %t", *a.System))
		}
	}
	if a.PrimaryGroup != "" {
		description = append(description, "primary group "+a.PrimaryGroup)
	}
	if a.Home != "" {
		description = append(description, "home "+a.Home+" (no directory creation or move)")
	}
	if a.Shell != "" {
		description = append(description, "shell "+a.Shell)
	}
	if a.Comment != nil {
		description = append(description, "comment "+*a.Comment)
	}
	if a.SupplementaryGroups != nil {
		mode := "exact local supplementary groups: "
		if a.AppendGroups != nil && *a.AppendGroups {
			mode = "append local supplementary groups (preserve other memberships): "
		}
		description = append(description, mode+strings.Join(*a.SupplementaryGroups, ","))
	}
	if a.Operation == "delete" && a.Kind == "user" {
		description = append(description, "preserve home and groups; userdel in private mount namespace with private USERGROUPS_ENAB=no policy (requires unshare/mount privilege)")
	}
	if a.Operation == "update" && a.PrimaryGroup != "" {
		description = append(description, "shadow-utils primary-group changes may update group ownership within the user's home")
	}
	if a.Expected != nil {
		if a.Kind == "user" {
			description = append(description, fmt.Sprintf("require prepared user UID=%d; reject changed user identity", a.Expected.UID))
		} else {
			description = append(description, fmt.Sprintf("require prepared group GID=%d; reject changed group identity", a.Expected.GID))
		}
	}
	for _, g := range a.Groups {
		if g.GID != nil {
			description = append(description, fmt.Sprintf("require local group %s GID=%d", g.Name, *g.GID))
		} else {
			description = append(description, "resolve preceding automatically allocated local group "+g.Name)
		}
	}
	if a.Operation != "noop" {
		if a.Operation == "delete" && a.Kind == "user" {
			description = append(description, "execute unshare --mount --propagation private sh -c <fixed private-policy helper> richt-account-delete "+strconv.Quote(a.Name))
		} else {
			name, args := command(a)
			if a.Operation == "delete" {
				name = "groupdel"
				args = []string{"--", a.Name}
			}
			words := []string{name}
			for _, arg := range args {
				words = append(words, strconv.Quote(arg))
			}
			description = append(description, "execute "+strings.Join(words, " "))
		}
		description = append(description, "verify the prepared result; unfinished retries accept an already matching result")
	}
	return []plan.Action{{Kind: "account", Account: a, Description: description}}, nil
}
func (b Backend) Resolve(t plan.Task) ([]plan.Action, error) { return Resolve(t) }

func (b Backend) validateCurrent(ctx context.Context, a *plan.AccountChange, s snapshot) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Kind == "group" {
		id := a.GID
		if g, ok := s.groups[a.Name]; ok {
			if g.gid == 0 || g.gid == ^uint32(0) {
				return errors.New("refusing protected local group numeric identity")
			}
			id = ptr(g.gid)
		}
		if err := b.groupNSS(ctx, s, a.Name, id); err != nil {
			return err
		}
		if g, ok := s.groups[a.Name]; ok && a.Expected != nil && g.gid != a.Expected.GID {
			return errors.New("local group identity changed since preparation")
		}
	} else {
		id := a.UID
		if u, ok := s.users[a.Name]; ok {
			if u.uid == 0 || u.uid == ^uint32(0) {
				return errors.New("refusing protected local user numeric identity")
			}
			id = ptr(u.uid)
		}
		if err := b.userNSS(ctx, s, a.Name, id); err != nil {
			return err
		}
		if u, ok := s.users[a.Name]; ok && a.Expected != nil && u.uid != a.Expected.UID {
			return errors.New("local user identity changed since preparation")
		}
	}
	for _, ref := range a.Groups {
		g, ok := s.groups[ref.Name]
		if !ok {
			return fmt.Errorf("required local group %s is missing", ref.Name)
		}
		if ref.GID != nil && g.gid != *ref.GID {
			return fmt.Errorf("required group %s changed identity", ref.Name)
		}
		if err := b.groupNSS(ctx, s, ref.Name, ptr(g.gid)); err != nil {
			return err
		}
	}
	return nil
}

// Observe verifies fixed inputs and reports whether the desired account is missing
// or differs. It never produces a new operation or adopts another numeric identity.
func (b Backend) Observe(ctx context.Context, a *plan.AccountChange) (bool, error) {
	s, err := b.read(ctx)
	if err != nil {
		return false, err
	}
	if err = b.validateCurrent(ctx, a, s); err != nil {
		return false, err
	}
	if a.State == "absent" {
		if a.Kind == "user" {
			_, ok := s.users[a.Name]
			return ok, nil
		}
		_, ok := s.groups[a.Name]
		return ok, nil
	}
	return !matches(s, a), nil
}

func (b Backend) Execute(ctx context.Context, action plan.Action) error {
	if action.Kind != "account" {
		return errors.New("unsupported account action")
	}
	a := action.Account
	if err := a.Validate(); err != nil {
		return err
	}
	s, err := b.read(ctx)
	if err != nil {
		return err
	}
	if err = b.validateCurrent(ctx, a, s); err != nil {
		return err
	}
	exists := false
	if a.Kind == "user" {
		_, exists = s.users[a.Name]
	} else {
		_, exists = s.groups[a.Name]
	}
	if a.Operation == "noop" {
		if a.State == "absent" && !exists || a.State == "present" && matches(s, a) {
			return nil
		}
		return errors.New("prepared account noop no longer matches its result")
	}
	if a.State == "absent" {
		if !exists {
			return nil
		}
		if a.Kind == "group" {
			if err = protectPrimary(s, s.groups[a.Name], nil); err != nil {
				return err
			}
			_, err = b.run(ctx, "groupdel", "--", a.Name)
		} else {
			_, err = b.run(ctx, "unshare", "--mount", "--propagation", "private", "sh", "-c", deleteUserScript, "richt-account-delete", a.Name)
		}
	} else {
		if exists && matches(s, a) {
			return nil
		}
		if a.Operation == "create" && exists {
			return errors.New("prepared account creation conflicts with an existing account")
		}
		if a.Operation == "update" && !exists {
			return errors.New("prepared account update target is missing")
		}
		name, args := command(a)
		_, err = b.run(ctx, name, args...)
	}
	if err != nil {
		return fmt.Errorf("%s local %s %s: %w", a.Operation, a.Kind, a.Name, err)
	}
	after, err := b.read(ctx)
	if err != nil {
		return err
	}
	if err = b.validateCurrent(ctx, a, after); err != nil {
		return err
	}
	if a.State == "absent" {
		if a.Kind == "user" {
			_, exists = after.users[a.Name]
			if before, ok := s.groups[a.Name]; ok {
				current, present := after.groups[a.Name]
				if !present || current.gid != before.gid {
					return errors.New("user deletion did not preserve its same-name group")
				}
			}
		} else {
			_, exists = after.groups[a.Name]
		}
		if exists {
			return errors.New("account deletion did not remove the local account")
		}
		return nil
	}
	if !matches(after, a) {
		return errors.New("account mutation did not produce its prepared result")
	}
	return nil
}

func command(a *plan.AccountChange) (string, []string) {
	args := []string{}
	name := "groupadd"
	if a.Kind == "group" {
		if a.System != nil && *a.System {
			args = append(args, "--system")
		}
		if a.GID != nil {
			args = append(args, "--gid", strconv.FormatUint(uint64(*a.GID), 10))
		}
	} else {
		name = "usermod"
		if a.Operation == "create" {
			name = "useradd"
			args = append(args, "--no-user-group", "--no-create-home", "--password", "!")
			if a.System != nil && *a.System {
				args = append(args, "--system")
			}
			if a.UID != nil {
				args = append(args, "--uid", strconv.FormatUint(uint64(*a.UID), 10))
			}
		}
		if a.PrimaryGroup != "" {
			args = append(args, "--gid", a.PrimaryGroup)
		}
		if a.Home != "" {
			args = append(args, "--home", a.Home)
		}
		if a.Shell != "" {
			args = append(args, "--shell", a.Shell)
		}
		if a.Comment != nil {
			args = append(args, "--comment", *a.Comment)
		}
		if a.SupplementaryGroups != nil {
			additive := a.AppendGroups != nil && *a.AppendGroups
			if a.Operation == "create" || !additive || len(*a.SupplementaryGroups) > 0 {
				if a.Operation != "create" && additive {
					args = append(args, "--append")
				}
				args = append(args, "--groups", strings.Join(*a.SupplementaryGroups, ","))
			}
		}
	}
	args = append(args, "--", a.Name)
	return name, args
}
