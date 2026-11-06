package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/subuk/richt/inventory"
)

// AccountChange fixes one local account operation; it never contains raw commands.
type AccountChange struct {
	Kind                string           `json:"kind"`
	Name                string           `json:"name"`
	State               string           `json:"state"`
	Operation           string           `json:"operation"`
	Expected            *AccountIdentity `json:"expected,omitempty"`
	UID                 *uint32          `json:"uid,omitempty"`
	GID                 *uint32          `json:"gid,omitempty"`
	System              *bool            `json:"system,omitempty"`
	PrimaryGroup        string           `json:"primary_group,omitempty"`
	Home                string           `json:"home,omitempty"`
	Shell               string           `json:"shell,omitempty"`
	Comment             *string          `json:"comment,omitempty"`
	SupplementaryGroups *[]string        `json:"supplementary_groups,omitempty"`
	AppendGroups        *bool            `json:"append_groups,omitempty"`
	Groups              []AccountGroup   `json:"groups,omitempty"`
}

// UnmarshalJSON retains explicit membership mode and rejects null mode values.
func (c *AccountChange) UnmarshalJSON(data []byte) error {
	type plain AccountChange
	var decoded plain
	wire := struct {
		*plain
		AppendGroups json.RawMessage `json:"append_groups"`
	}{plain: &decoded}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return errors.New("invalid account JSON")
	}
	if wire.AppendGroups != nil {
		var value *bool
		if err := json.Unmarshal(wire.AppendGroups, &value); err != nil || value == nil {
			return errors.New("account append_groups must be a boolean when supplied")
		}
		decoded.AppendGroups = value
	}
	*c = AccountChange(decoded)
	return nil
}

type AccountIdentity struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

// A nil GID identifies a preceding group creation with automatic allocation.
type AccountGroup struct {
	Name string  `json:"name"`
	GID  *uint32 `json:"gid,omitempty"`
}

func ValidAccountName(name string) bool { return inventory.ValidAccountName(name) }

func (c *AccountChange) Validate() error {
	if c == nil || !ValidAccountName(c.Name) || (c.Kind != "user" && c.Kind != "group") {
		return errors.New("invalid account identity in plan")
	}
	if c.State != "present" && c.State != "absent" {
		return errors.New("invalid account state in plan")
	}
	if c.Name == "root" {
		return errors.New("managing root accounts is prohibited")
	}
	for _, id := range []*uint32{c.UID, c.GID} {
		if id != nil && (*id == 0 || *id == ^uint32(0)) {
			return errors.New("invalid account numeric ID in plan")
		}
	}
	if c.Expected != nil {
		if c.Kind == "user" && (c.Expected.UID == 0 || c.Expected.UID == ^uint32(0) || c.Expected.GID == ^uint32(0)) || c.Kind == "group" && (c.Expected.UID != 0 || c.Expected.GID == 0 || c.Expected.GID == ^uint32(0)) {
			return errors.New("invalid expected account identity")
		}
	}
	switch c.Operation {
	case "create":
		if c.State != "present" || c.Expected != nil {
			return errors.New("invalid account creation precondition")
		}
	case "update":
		if c.State != "present" || c.Expected == nil || c.Kind != "user" {
			return errors.New("invalid account update precondition")
		}
	case "delete":
		if c.State != "absent" || c.Expected == nil {
			return errors.New("invalid account deletion precondition")
		}
	case "noop":
		if (c.State == "present") != (c.Expected != nil) {
			return errors.New("invalid account noop precondition")
		}
	default:
		return errors.New("invalid account operation in plan")
	}
	if c.System != nil && c.Operation != "create" {
		return errors.New("system allocation is valid only for account creation")
	}
	if c.State == "absent" && (c.UID != nil || c.GID != nil || c.System != nil || c.PrimaryGroup != "" || c.Home != "" || c.Shell != "" || c.Comment != nil || c.SupplementaryGroups != nil || c.AppendGroups != nil || len(c.Groups) != 0) {
		return errors.New("creation fields on absent account plan")
	}
	if c.Kind == "group" {
		if c.UID != nil || c.PrimaryGroup != "" || c.Home != "" || c.Shell != "" || c.Comment != nil || c.SupplementaryGroups != nil || c.AppendGroups != nil || len(c.Groups) != 0 {
			return errors.New("user fields on group plan")
		}
		if c.Expected != nil && c.GID != nil && *c.GID != c.Expected.GID {
			return errors.New("group GID changes are unsupported")
		}
		return nil
	}
	if c.GID != nil {
		return errors.New("user plan requires a named primary group")
	}
	if c.Expected != nil && c.UID != nil && *c.UID != c.Expected.UID {
		return errors.New("user UID changes are unsupported")
	}
	for _, p := range []string{c.Home, c.Shell} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsRune(p, ':') || strings.IndexFunc(p, unicode.IsControl) >= 0) {
			return errors.New("invalid account path in plan")
		}
	}
	if c.Comment != nil && (strings.ContainsRune(*c.Comment, ':') || strings.IndexFunc(*c.Comment, unicode.IsControl) >= 0) {
		return errors.New("invalid account comment in plan")
	}
	if c.Operation == "create" && (c.PrimaryGroup == "" || c.Home == "" || c.Shell == "" || c.Comment == nil) {
		return errors.New("incomplete account creation inputs")
	}
	if c.State == "present" && c.PrimaryGroup == "" {
		return errors.New("present user requires a named primary group")
	}
	if c.Operation == "create" && c.SupplementaryGroups == nil {
		return errors.New("account creation requires frozen supplementary groups")
	}
	if c.AppendGroups != nil && c.SupplementaryGroups == nil {
		return errors.New("append_groups requires supplementary_groups in account plan")
	}
	refs := map[string]bool{}
	if c.PrimaryGroup != "" {
		if !ValidAccountName(c.PrimaryGroup) {
			return errors.New("invalid primary group in plan")
		}
		refs[c.PrimaryGroup] = true
	}
	if c.SupplementaryGroups != nil {
		seen := map[string]bool{}
		for _, name := range *c.SupplementaryGroups {
			if !ValidAccountName(name) || seen[name] {
				return errors.New("invalid supplementary groups in plan")
			}
			seen[name] = true
			refs[name] = true
		}
	}
	seen := map[string]bool{}
	for _, g := range c.Groups {
		if !refs[g.Name] || seen[g.Name] || g.GID != nil && *g.GID == ^uint32(0) {
			return errors.New("invalid account group precondition")
		}
		seen[g.Name] = true
	}
	if len(seen) != len(refs) {
		return errors.New("missing account group precondition")
	}
	return nil
}
