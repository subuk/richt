// Package config prepares templates and resolves concrete local file operations.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/aymanbagabas/go-udiff"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"golang.org/x/sys/unix"
)

// Diff renders all templates and validates capabilities before observing resources.
func Diff(r *inventory.Resolved) (*plan.Plan, error) {
	p := &plan.Plan{Version: plan.Version}
	for _, h := range r.Hosts {
		if h.Transport.Type != "local" || h.Transport.Sudo {
			return nil, fmt.Errorf("host %s: transport %s/sudo is not supported yet", h.Name, h.Transport.Type)
		}
		ph := plan.Host{Name: h.Name, Transport: "local"}
		for _, s := range h.Services {
			if len(s.Definition.Binaries) > 0 || len(s.Definition.Processes) > 0 {
				return nil, fmt.Errorf("host %s service %s: binary/process backends are not supported yet", h.Name, s.Name)
			}
			names := make([]string, 0, len(s.Definition.Configs))
			for n := range s.Definition.Configs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				c := s.Definition.Configs[name]
				f, err := Prepare(c, s.State, h.Vars)
				if err != nil {
					return nil, fmt.Errorf("host %s service %s config %s: %w", h.Name, s.Name, name, err)
				}
				ph.Tasks = append(ph.Tasks, plan.Task{ID: s.Name + "/config/" + name, Service: s.Name, Component: "config/" + name, Operation: "file", File: f, Progress: plan.Progress{Status: "pending"}})
			}
		}
		p.Hosts = append(p.Hosts, ph)
	}
	// Only after every template/capability is valid, read current files and keep changes.
	for hi := range p.Hosts {
		h := &p.Hosts[hi]
		tasks := h.Tasks[:0]
		for _, t := range h.Tasks {
			changed, err := Observe(t.File)
			if err != nil {
				if t.File.Sensitive {
					err = errors.New("sensitive file observation failed")
				}
				return nil, fmt.Errorf("host %s config %s: %w", h.Name, t.Component, err)
			}
			if changed {
				tasks = append(tasks, t)
			}
		}
		h.Tasks = tasks
	}
	return p, nil
}

// Prepare freezes rendered content and requested ownership names for a file task.
func Prepare(c inventory.Config, state string, vars map[string]any) (*plan.FileChange, error) {
	if state == "" {
		state = "present"
	}
	if state != "present" && state != "absent" {
		return nil, fmt.Errorf("invalid file state %q", state)
	}
	if !filepath.IsAbs(c.Path) || filepath.Clean(c.Path) == "/" {
		return nil, errors.New("file path must be absolute and identify a file")
	}
	f := &plan.FileChange{Path: filepath.Clean(c.Path), State: state, Sensitive: c.Sensitive, SELinux: c.SELinux}
	f.Owner, f.Group = c.Owner, c.Group
	if c.Mode != "" {
		m, err := strconv.ParseUint(c.Mode, 8, 12)
		if err != nil {
			return nil, fmt.Errorf("invalid file mode %q", c.Mode)
		}
		mode := uint32(m)
		f.Mode = &mode
	}
	if c.SELinux != "" {
		if err := validateLabelSyntax(c.SELinux); err != nil {
			return nil, err
		}
	}
	if state == "absent" {
		return f, nil
	}
	var source string
	if c.Template.Inline != nil {
		source = *c.Template.Inline
	} else {
		b, err := os.ReadFile(c.Template.File)
		if err != nil {
			return nil, fmt.Errorf("read template: %w", err)
		}
		source = string(b)
	}
	secretCalled := false
	funcs := template.FuncMap{"secret": func(...any) (string, error) {
		secretCalled = true
		return "", errors.New("secret sources are not supported yet")
	}}
	tpl, err := template.New("config").Option("missingkey=error").Funcs(funcs).Parse(source)
	if err == nil {
		var b bytes.Buffer
		err = tpl.Execute(&b, vars)
		f.Content = b.String()
	}
	if secretCalled {
		return nil, errors.New("secret sources are not supported yet")
	}
	if err != nil {
		if c.Sensitive {
			return nil, errors.New("sensitive template rendering failed")
		}
		return nil, fmt.Errorf("render template: %w", err)
	}
	return f, nil
}

// Observe computes the final patch; apply never invokes it.
func Observe(f *plan.FileChange) (bool, error) {
	uid, gid, err := ownership(f)
	if err != nil {
		return false, err
	}
	if f.SELinux != "" {
		if err := validateLabel(f.SELinux); err != nil {
			return false, err
		}
	}
	if f.State == "absent" {
		info, err := os.Lstat(f.Path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return false, errors.New("target is not a regular file")
		}
		old := []byte(nil)
		if info.Mode().IsRegular() {
			old, err = os.ReadFile(f.Path)
		}
		if err != nil {
			return false, err
		}
		f.Patch = udiff.Unified(f.Path, "/dev/null", string(old), "")
		return true, nil
	}
	file, err := os.OpenFile(f.Path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		f.Write = true
		f.Patch = udiff.Unified("/dev/null", f.Path, "", f.Content)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("target is not a regular file")
	}
	old, err := io.ReadAll(file)
	if err != nil {
		return false, err
	}
	f.Write = !bytes.Equal(old, []byte(f.Content))
	f.Patch = ""
	if f.Write {
		f.Patch = udiff.Unified(f.Path, f.Path, string(old), f.Content)
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &st); err != nil {
		return false, err
	}
	changed := f.Write || f.Mode != nil && uint32(st.Mode)&07777 != *f.Mode || uid != -1 && int(st.Uid) != uid || gid != -1 && int(st.Gid) != gid
	if f.SELinux != "" {
		label, err := readLabel(file)
		if err != nil {
			return false, err
		}
		changed = changed || label != f.SELinux
	}
	return changed, nil
}

func validateLabelSyntax(label string) error {
	parts := strings.SplitN(label, ":", 4)
	if len(parts) < 3 || strings.ContainsAny(label, " \t\n\r\x00") {
		return errors.New("invalid SELinux context")
	}
	for _, part := range parts {
		if part == "" {
			return errors.New("invalid SELinux context")
		}
	}
	return nil
}

// Names are resolved on the managed host each time, rather than saved as IDs.
func ownership(f *plan.FileChange) (int, int, error) {
	uid, gid := -1, -1
	if f.UID != nil {
		uid = *f.UID
	}
	if f.GID != nil {
		gid = *f.GID
	}
	if f.Owner != "" {
		u, err := user.Lookup(f.Owner)
		if err != nil {
			return 0, 0, fmt.Errorf("owner %q does not exist", f.Owner)
		}
		uid, err = strconv.Atoi(u.Uid)
		if err != nil {
			return 0, 0, err
		}
	}
	if f.Group != "" {
		g, err := user.LookupGroup(f.Group)
		if err != nil {
			return 0, 0, fmt.Errorf("group %q does not exist", f.Group)
		}
		gid, err = strconv.Atoi(g.Gid)
		if err != nil {
			return 0, 0, err
		}
	}
	return uid, gid, nil
}

type Local struct{}

func (Local) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "file" || t.File == nil || t.RPM != nil || t.Process != nil {
		return nil, fmt.Errorf("unsupported operation %q", t.Operation)
	}
	f := t.File
	if !filepath.IsAbs(f.Path) || filepath.Clean(f.Path) != f.Path || f.Path == "/" {
		return nil, errors.New("invalid file path in plan")
	}
	if f.UID != nil && *f.UID < 0 || f.GID != nil && *f.GID < 0 || f.Mode != nil && *f.Mode > 07777 {
		return nil, errors.New("invalid file attributes in plan")
	}
	if f.State == "absent" {
		return []plan.Action{{Kind: "remove-file", File: f}}, nil
	}
	if f.State != "present" {
		return nil, errors.New("invalid file state in plan")
	}
	if f.SELinux != "" {
		if err := validateLabelSyntax(f.SELinux); err != nil {
			return nil, err
		}
	}
	actions := []plan.Action{{Kind: "create-parent-directories", File: f}}
	if f.Write {
		actions = append(actions, plan.Action{Kind: "overwrite-file", File: f})
	}
	if f.Owner != "" || f.Group != "" || f.UID != nil || f.GID != nil || f.Mode != nil || f.SELinux != "" {
		actions = append(actions, plan.Action{Kind: "set-file-attributes", File: f})
	}
	return actions, nil
}

func (Local) Execute(ctx context.Context, a plan.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f := a.File
	if f == nil {
		return errors.New("missing file action inputs")
	}
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return err
	}
	if a.Kind == "create-parent-directories" {
		return os.MkdirAll(filepath.Dir(f.Path), 0755)
	}
	if a.Kind == "remove-file" {
		info, err := os.Lstat(f.Path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("target is not a regular file")
		}
		return os.Remove(f.Path)
	}
	flags := os.O_WRONLY | unix.O_NONBLOCK
	if a.Kind == "overwrite-file" {
		flags |= os.O_CREATE
	} else if a.Kind != "set-file-attributes" {
		return fmt.Errorf("unsupported action %q", a.Kind)
	}
	file, err := os.OpenFile(f.Path, flags, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("target is not a regular file")
	}
	if a.Kind == "overwrite-file" {
		if err = file.Truncate(0); err != nil {
			return err
		}
		if _, err = file.WriteString(f.Content); err != nil {
			return err
		}
		return file.Sync()
	}
	uid, gid, err := ownership(f)
	if err != nil {
		return err
	}
	if uid != -1 || gid != -1 {
		if err = file.Chown(uid, gid); err != nil {
			return err
		}
	}
	if f.Mode != nil {
		if err = unix.Fchmod(int(file.Fd()), *f.Mode); err != nil {
			return err
		}
	}
	if f.SELinux != "" {
		if err = validateLabel(f.SELinux); err != nil {
			return err
		}
		if err = writeLabel(file, f.SELinux); err != nil {
			return err
		}
	}
	return file.Sync()
}
