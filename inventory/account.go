package inventory

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

var accountNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*\$?$`)

// ValidAccountName reports whether name is a supported Linux local account name.
func ValidAccountName(name string) bool {
	return len(name) <= 32 && accountNamePattern.MatchString(name)
}

// ValidateAccountConfig checks local account declarations without observing a
// target. Optional values retain omission so existing account attributes remain
// unmanaged unless explicitly requested.
func ValidateAccountConfig(c Config) error {
	if c.Type != "user" && c.Type != "group" {
		return fmt.Errorf("type: expected user or group")
	}
	if c.Path != "" || c.Owner != "" || c.Mode != "" || c.Template.File != "" || c.Template.Inline != nil || c.Sensitive || c.AtomicWrites != nil || c.Backup || c.SELinux != "" || c.Validate != nil || c.Array != "" || c.Level != "" || c.Metadata != "" || c.Members != nil || c.VG != "" || c.Devices != nil || c.LV != "" || c.Size != "" || c.Filesystem != "" || c.Source != "" || c.Target != "" || c.Options != nil {
		return fmt.Errorf("%s config cannot set file, storage or mount fields", c.Type)
	}
	validName := func(field, value string) error {
		if !ValidAccountName(value) {
			return fmt.Errorf("%s: expected a Linux account name of at most 32 bytes", field)
		}
		return nil
	}
	if err := validName("group", c.Group); err != nil {
		return err
	}
	if c.Type == "group" {
		if c.User != "" || c.UID != nil || c.Home != "" || c.Shell != "" || c.Comment != nil || c.SupplementaryGroups != nil {
			return fmt.Errorf("group config cannot set user fields")
		}
		if c.Group == "root" {
			return fmt.Errorf("group: managing root is prohibited")
		}
		if c.GID != nil && (*c.GID == 0 || *c.GID == ^uint32(0)) {
			return fmt.Errorf("gid: expected an integer from 1 to 4294967294")
		}
		return nil
	}
	if err := validName("user", c.User); err != nil {
		return err
	}
	if c.User == "root" {
		return fmt.Errorf("user: managing root is prohibited")
	}
	if c.GID != nil {
		return fmt.Errorf("user config cannot set gid; select its primary group by name")
	}
	if c.UID != nil && (*c.UID == 0 || *c.UID == ^uint32(0)) {
		return fmt.Errorf("uid: expected an integer from 1 to 4294967294")
	}
	for _, field := range []struct{ name, value string }{{"home", c.Home}, {"shell", c.Shell}} {
		if field.value != "" && (!path.IsAbs(field.value) || path.Clean(field.value) != field.value || field.value == "/" || strings.ContainsRune(field.value, ':') || strings.IndexFunc(field.value, unicode.IsControl) >= 0) {
			return fmt.Errorf("%s: expected a clean absolute non-root path without colons or control characters", field.name)
		}
	}
	if c.Comment != nil && (strings.ContainsRune(*c.Comment, ':') || strings.IndexFunc(*c.Comment, unicode.IsControl) >= 0) {
		return fmt.Errorf("comment: colons and control characters are prohibited")
	}
	if c.SupplementaryGroups != nil {
		seen := map[string]bool{}
		for _, group := range *c.SupplementaryGroups {
			if err := validName("supplementary_groups", group); err != nil {
				return err
			}
			if seen[group] {
				return fmt.Errorf("supplementary_groups: duplicate group %q", group)
			}
			seen[group] = true
		}
	}
	return nil
}

func hasAccountFields(c Config) bool {
	return c.User != "" || c.UID != nil || c.GID != nil || c.System != nil || c.Home != "" || c.Shell != "" || c.Comment != nil || c.SupplementaryGroups != nil
}
