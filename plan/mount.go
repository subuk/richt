package plan

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

// MountChange freezes one fstab edit and the mount operation using a block identity.
// Before/after snapshots compose in config order and permit interrupted continuation.
type MountChange struct {
	Source        string         `json:"source"`
	Target        string         `json:"target"`
	Filesystem    string         `json:"filesystem"`
	Options       []string       `json:"options"`
	State         string         `json:"state"`
	Identity      DeviceIdentity `json:"identity"`
	FstabBefore   string         `json:"fstab_before"`
	FstabAfter    string         `json:"fstab_after"`
	Active        bool           `json:"active"`
	TargetPending bool           `json:"target_pending"`
}

func (c *MountChange) Validate() error {
	if c == nil {
		return errors.New("missing mount payload")
	}
	for _, p := range []string{c.Source, c.Target} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return errors.New("mount source and target must be clean absolute non-root paths")
		}
	}
	if c.Filesystem != "xfs" || c.State != "present" && c.State != "absent" {
		return errors.New("invalid mount filesystem or target state")
	}
	seen := map[string]bool{}
	for _, o := range c.Options {
		if o == "" || strings.ContainsAny(o, ",\\#") || strings.IndexFunc(o, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || seen[o] {
			return errors.New("invalid or duplicate mount option")
		}
		seen[o] = true
	}
	if c.State == "present" && c.Identity.Path != "" && (c.Identity.Filesystem != "xfs" || c.Identity.UUID == "") {
		return errors.New("mount requires a prepared XFS filesystem identity")
	}
	if c.State == "present" && c.Identity.Path == "" {
		return errors.New("mount requires a prepared source identity")
	}
	return nil
}
