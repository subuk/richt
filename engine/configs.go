package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/storage"
)

// configSequence contains only the declared effects preceding the current
// config on this host. It does not select prerequisites or reorder components.
type configSequence struct {
	storage storage.Preparation
	mounts  []*plan.MountChange
}

func newConfigSequence() *configSequence {
	return &configSequence{storage: storage.Preparation{Unmounted: map[string]bool{}}}
}

func (e Local) hiddenConfigPath(ctx context.Context, s *configSequence, path, state string) (bool, error) {
	if len(s.mounts) == 0 {
		return false, nil
	}
	canonical, err := e.Mount.ProjectPath(ctx, path, s.mounts)
	if err != nil {
		return false, err
	}
	return s.hidden(canonical, state)
}

// Device and mount configs require Linux. Resolve existing path aliases at
// preparation, while inventory inspection remains entirely structural.
func (e Local) checkConfigPaths(ctx context.Context, services []preparedService) error {
	required := false
	for _, s := range services {
		for _, f := range s.files {
			if f.definition != nil {
				switch f.definition.Type {
				case "mount", "mdadm", "lvm-vg", "lvm-lv":
					required = true
				}
			}
		}
	}
	if !required {
		return nil
	}
	owners := map[string]string{}
	hasMount := false
	type claim struct {
		path, state, kind, owner string
		position                 int
	}
	var claims []claim
	var declared []*plan.MountChange
	for _, s := range services {
		for _, f := range s.files {
			if c := f.definition; c != nil && c.Type == "mount" {
				path, err := e.Mount.ProjectPath(ctx, c.Target, declared)
				if err != nil {
					return err
				}
				declared = append(declared, &plan.MountChange{Target: path, TargetPending: true, State: "present"})
			}
		}
	}
	for _, s := range services {
		for _, f := range s.files {
			path := ""
			if f.file != nil {
				path = f.file.Path
			} else if c := f.definition; c != nil {
				switch c.Type {
				case "file", "directory":
					path = c.Path
				case "mdadm":
					path = c.Array
				case "lvm-lv":
					path = "/dev/" + c.VG + "/" + c.LV
				case "mount":
					path = c.Target
					hasMount = true
				}
			}
			if path == "" {
				continue
			}
			canonical, err := e.Mount.ProjectPath(ctx, path, declared)
			if err != nil {
				return err
			}
			owner := s.service.Name + "/" + f.component
			if old, ok := owners[canonical]; ok {
				return fmt.Errorf("resource aliases %s and %s resolve to the same path %s", old, owner, canonical)
			}
			owners[canonical] = owner
			kind := "template"
			if f.definition != nil {
				kind = f.definition.Type
			}
			claims = append(claims, claim{canonical, s.service.State, kind, owner, len(claims)})
		}
	}
	for _, c := range claims {
		if c.state == "absent" || c.kind != "template" && c.kind != "file" && c.kind != "directory" {
			continue
		}
		for _, m := range claims {
			if m.kind == "mount" && m.state != "absent" && m.position > c.position && strings.HasPrefix(c.path, m.path+"/") {
				return fmt.Errorf("%s precedes mount %s; declare the mount before its filesystem contents", c.owner, m.path)
			}
		}
	}
	if owner, ok := owners["/etc/fstab"]; ok && hasMount {
		return fmt.Errorf("%s owns /etc/fstab, conflicting with mount entry management", owner)
	}
	return nil
}

// hidden prevents observation of the covered directory when a preceding mount
// will expose another filesystem. A newly formatted filesystem is known empty;
// an existing unmounted filesystem must be mounted before inspecting its files.
func (s *configSequence) hidden(path, state string) (bool, error) {
	var enclosing *plan.MountChange
	for _, m := range s.mounts {
		if path == m.Target || strings.HasPrefix(path, m.Target+"/") {
			if enclosing == nil || len(m.Target) >= len(enclosing.Target) {
				enclosing = m
			}
		}
	}
	if enclosing == nil {
		return false, nil
	}
	if enclosing.State == "absent" {
		return false, fmt.Errorf("path %s follows removal of mount %s; place filesystem contents after the mount in the present config list", path, enclosing.Target)
	}
	if !enclosing.TargetPending {
		return false, nil
	}
	if state == "present" && enclosing.Identity.Fresh {
		return true, nil
	}
	return false, fmt.Errorf("cannot observe %s inside unmounted existing filesystem %s; mount it before preparing its contents", path, enclosing.Target)
}

func (e Local) observeTyped(ctx context.Context, service inventory.ResolvedService, f preparedFile, sequence *configSequence) (plan.Task, bool, error) {
	c := *f.definition
	t := task(service.Name, f.id, f.component, "")
	switch c.Type {
	case "file", "directory":
		t.Operation = "path"
		p, err := config.PreparePath(c, service.State)
		if err != nil {
			return t, false, err
		}
		t.Path = p
		hidden, err := e.hiddenConfigPath(ctx, sequence, filepath.Clean(c.Path), service.State)
		if err != nil {
			return t, false, err
		}
		var changed bool
		if e.Files != nil {
			if hidden {
				changed, err = e.Files.ObservePathMissing(ctx, p)
			} else {
				changed, err = e.Files.ObservePath(ctx, p)
			}
		} else {
			if hidden {
				changed, err = (config.Local{}).ObservePathMissing(ctx, p)
			} else {
				changed, err = (config.Local{}).ObservePath(ctx, p)
			}
		}
		return t, changed, err
	case "mdadm", "lvm-vg", "lvm-lv":
		t.Operation = "storage"
		change, changed, err := e.Storage.Prepare(ctx, c, service.State, &sequence.storage)
		t.Storage = change
		return t, changed, err
	case "mount":
		t.Operation = "mount"
		var anticipated *plan.DeviceIdentity
		if d, ok := sequence.storage.Lookup(c.Source); ok {
			anticipated = &d
		}
		change, changed, err := e.Mount.Prepare(ctx, c, service.State, anticipated, sequence.mounts)
		if err != nil {
			return t, false, err
		}
		t.Mount = change
		sequence.mounts = append(sequence.mounts, change)
		if service.State == "absent" {
			sequence.storage.Unmounted[change.Target] = true
		}
		return t, changed, nil
	default:
		return t, false, fmt.Errorf("unsupported config type %q", c.Type)
	}
}
