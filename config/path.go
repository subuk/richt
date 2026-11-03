package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"golang.org/x/sys/unix"
)

func PreparePath(c inventory.Config, state string) (*plan.PathChange, error) {
	if state == "" {
		state = "present"
	}
	p := &plan.PathChange{Path: filepath.Clean(c.Path), Type: c.Type, State: state, Owner: c.Owner, Group: c.Group}
	if c.Mode != "" {
		m, err := strconv.ParseUint(c.Mode, 8, 12)
		if err != nil {
			return nil, errors.New("invalid path mode")
		}
		mode := uint32(m)
		p.Mode = &mode
	}
	return p, p.Validate()
}

func (Local) ObservePath(ctx context.Context, p *plan.PathChange) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return ObservePath(p)
}

func pathStat(path string, follow bool) (*unix.Stat_t, error) {
	var st unix.Stat_t
	var err error
	if follow {
		err = unix.Stat(path, &st)
	} else {
		err = unix.Lstat(path, &st)
	}
	return &st, err
}
func pathIdentity(st *unix.Stat_t) *plan.PathIdentity {
	return &plan.PathIdentity{Device: uint64(st.Dev), Inode: uint64(st.Ino)}
}
func samePathIdentity(st *unix.Stat_t, id *plan.PathIdentity) bool {
	return id == nil || uint64(st.Dev) == id.Device && uint64(st.Ino) == id.Inode
}
func pathCompatible(st *unix.Stat_t, p *plan.PathChange) bool {
	typ := st.Mode & unix.S_IFMT
	return p.State == "absent" && typ == unix.S_IFLNK || p.Type == "file" && typ == unix.S_IFREG || p.Type == "directory" && typ == unix.S_IFDIR
}
func pathOwnership(p *plan.PathChange) (int, int, error) {
	return ownership(&plan.FileChange{Owner: p.Owner, Group: p.Group})
}

func ObservePath(p *plan.PathChange) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	uid, gid, err := pathOwnership(p)
	if err != nil {
		return false, err
	}
	st, err := pathStat(p.Path, p.State == "present")
	if os.IsNotExist(err) {
		return p.State == "present", nil
	}
	if err != nil {
		return false, err
	}
	if !pathCompatible(st, p) {
		return false, errors.New("target has an incompatible path type")
	}
	p.Identity = pathIdentity(st)
	if p.State == "absent" {
		return true, nil
	}
	return p.Mode != nil && uint32(st.Mode)&07777 != *p.Mode || uid != -1 && int(st.Uid) != uid || gid != -1 && int(st.Gid) != gid, nil
}

func (Local) ResolvePath(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "path" || t.Artifact != nil || t.Path == nil || t.File != nil || t.RPM != nil || t.Process != nil || t.Oneshot != nil || t.Storage != nil || t.Mount != nil {
		return nil, errors.New("invalid path task")
	}
	if err := t.Path.Validate(); err != nil {
		return nil, err
	}
	kind := "ensure-path"
	description := []string{fmt.Sprintf("ensure %s %s; preserve existing contents; create necessary parents with mode 0755 subject to target umask", t.Path.Type, t.Path.Path)}
	if t.Path.Type == "file" {
		description = append(description, "create an empty file only when missing, default mode 0644; never truncate existing contents")
	} else {
		description = append(description, "create a missing directory with default mode 0755; attributes affect this directory only")
	}
	if t.Path.Owner != "" {
		description = append(description, "set owner "+t.Path.Owner+" (resolve name on target)")
	}
	if t.Path.Group != "" {
		description = append(description, "set group "+t.Path.Group+" (resolve name on target)")
	}
	if t.Path.Mode != nil {
		description = append(description, fmt.Sprintf("set mode %04o", *t.Path.Mode))
	}
	if t.Path.State == "absent" {
		kind = "remove-path"
		description = []string{fmt.Sprintf("delete %s %s and its data; unlink a named symlink without following it", t.Path.Type, t.Path.Path)}
		if t.Path.Type == "directory" {
			description = append(description, "recursively delete all children, including undeclared data; refuse active mount and device boundaries")
		}
	}
	if t.Path.Identity != nil {
		description = append(description, fmt.Sprintf("require prepared identity device=%d inode=%d; fail if replaced", t.Path.Identity.Device, t.Path.Identity.Inode))
	}
	return []plan.Action{{Kind: kind, Path: t.Path, Description: description}}, nil
}

func (Local) ExecutePath(ctx context.Context, a plan.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := a.Path
	if p == nil {
		return errors.New("missing path inputs")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.State == "absent" {
		if a.Kind != "remove-path" {
			return errors.New("invalid path removal action")
		}
		st, err := pathStat(p.Path, false)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.Identity == nil || !samePathIdentity(st, p.Identity) {
			return errors.New("path identity changed since preparation")
		}
		if !pathCompatible(st, p) {
			return errors.New("target has an incompatible path type")
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return os.Remove(p.Path)
		}
		if err := checkPathMounts(p.Path); err != nil {
			return err
		}
		return removePathTree(p.Path, p.Identity)
	}
	if a.Kind != "ensure-path" {
		return errors.New("invalid path creation action")
	}
	if p.Identity != nil {
		st, err := pathStat(p.Path, true)
		if err != nil || !samePathIdentity(st, p.Identity) {
			return errors.New("path identity changed since preparation")
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0755); err != nil {
		return err
	}
	var file *os.File
	var err error
	created := false
	if p.Type == "file" {
		file, err = os.OpenFile(p.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NONBLOCK, 0600)
		if err == nil {
			created = true
		} else if os.IsExist(err) {
			file, err = os.OpenFile(p.Path, os.O_RDONLY|unix.O_NONBLOCK, 0)
			if os.IsPermission(err) {
				file, err = os.OpenFile(p.Path, os.O_WRONLY|unix.O_NONBLOCK, 0)
			}
		}
	} else {
		err = os.Mkdir(p.Path, 0700)
		created = err == nil
		if err != nil && !os.IsExist(err) {
			return err
		}
		file, err = os.Open(p.Path)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	if !pathCompatible(&st, p) {
		return errors.New("target has an incompatible path type")
	}
	if !samePathIdentity(&st, p.Identity) {
		return errors.New("path identity changed since preparation")
	}
	if created {
		mode := uint32(0644)
		if p.Type == "directory" {
			mode = 0755
		}
		if err := unix.Fchmod(int(file.Fd()), mode); err != nil {
			return err
		}
	}
	uid, gid, err := pathOwnership(p)
	if err != nil {
		return err
	}
	if uid != -1 || gid != -1 {
		if err := file.Chown(uid, gid); err != nil {
			return err
		}
	}
	if p.Mode != nil {
		if err := unix.Fchmod(int(file.Fd()), *p.Mode); err != nil {
			return err
		}
	}
	return nil
}

// Check all mount points, including same-device bind mounts, before deletion.
func checkPathMounts(path string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		target := decode.Replace(fields[4])
		if target == path || strings.HasPrefix(target, path+"/") {
			return fmt.Errorf("recursive directory deletion crosses active mount %s", target)
		}
	}
	return nil
}

// Descriptor-relative traversal does not follow child symlinks or cross devices.
func removePathTree(path string, id *plan.PathIdentity) error {
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return removePathAt(int(parent.Fd()), filepath.Base(path), id.Device, id)
}
func removePathAt(parent int, name string, device uint64, identity *plan.PathIdentity) error {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if !samePathIdentity(&st, identity) {
		return errors.New("path identity changed during deletion")
	}
	if uint64(st.Dev) != device {
		return errors.New("recursive directory deletion crosses a device boundary")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(parent, name, 0)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return err
	}
	if !samePathIdentity(&opened, pathIdentity(&st)) {
		return errors.New("directory changed during deletion")
	}
	children, err := file.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := removePathAt(fd, child.Name(), device, nil); err != nil {
			return err
		}
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !samePathIdentity(&current, pathIdentity(&st)) {
		return errors.New("directory changed during deletion")
	}
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}
