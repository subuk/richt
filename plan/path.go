package plan

import (
	"errors"
	"path/filepath"
	"strings"
)

// PathChange manages existence and explicit attributes without managing contents.
type PathChange struct {
	Path     string        `json:"path"`
	Type     string        `json:"type"`
	State    string        `json:"state"`
	Owner    string        `json:"owner,omitempty"`
	Group    string        `json:"group,omitempty"`
	Mode     *uint32       `json:"mode,omitempty"`
	Identity *PathIdentity `json:"identity,omitempty"`
}

// PathIdentity protects an observed named entry from replacement on continuation.
type PathIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func (p *PathChange) Validate() error {
	if p == nil || !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || p.Path == "/" || strings.ContainsAny(p.Path, "\x00\r\n") {
		return errors.New("invalid path in plan")
	}
	if p.Type != "file" && p.Type != "directory" {
		return errors.New("invalid path type in plan")
	}
	if p.State != "present" && p.State != "absent" {
		return errors.New("invalid path state in plan")
	}
	if p.Mode != nil && *p.Mode > 07777 {
		return errors.New("invalid path mode in plan")
	}
	if p.Identity != nil && p.Identity.Inode == 0 {
		return errors.New("invalid path identity in plan")
	}
	return nil
}
