package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

// ownershipPaths retains only declarations needed for alias checks. Excluded
// services are never prepared, rendered, downloaded, or scheduled by this pass.
func ownershipPaths(services []inventory.ResolvedService) []preparedService {
	out := make([]preparedService, 0, len(services))
	for _, s := range services {
		ps := preparedService{service: s}
		for i, b := range s.Definition.Binaries {
			if b.Type == "download" || b.Type == "archive" {
				ps.binaries = append(ps.binaries, preparedBinary{index: i, name: b.Name, artifact: &plan.ArtifactChange{Type: b.Type, Path: b.Path}})
			}
		}
		add := func(c inventory.Config, component string) {
			f := preparedFile{component: component}
			if c.Type == "" || c.Type == "template" {
				f.file = &plan.FileChange{Path: c.Path}
			} else {
				f.definition = &c
			}
			ps.files = append(ps.files, f)
		}
		for i, c := range s.Definition.Configs {
			add(c, "config/"+label(c.Name, i))
		}
		for i, p := range s.Definition.Processes {
			if p.Type != "systemd" {
				continue
			}
			if p.UnitFile != nil {
				c := *p.UnitFile
				if c.Path == "" {
					c.Path = "/etc/systemd/system/" + p.Unit
				}
				add(c, "process/"+label(p.Name, i)+"/unit")
			}
			for _, key := range keys(p.Overrides) {
				c := p.Overrides[key]
				if c.Path == "" {
					c.Path = "/etc/systemd/system/" + p.Unit + ".d/" + key
				}
				add(c, "process/"+label(p.Name, i)+"/override/"+key)
			}
		}
		out = append(out, ps)
	}
	return out
}

// checkArtifactPaths checks target aliases before observation. Artifact roots
// own their complete trees; ordinary config writes may follow aliases into them.
func (e Local) checkArtifactPaths(ctx context.Context, services []preparedService) error {
	type claim struct {
		path, owner string
		tree        bool
	}
	var artifacts []claim
	for _, service := range services {
		for _, b := range service.binaries {
			if c := b.artifact; c != nil {
				owner := service.service.Name + "/binary/" + label(b.name, b.index)
				artifacts = append(artifacts, claim{c.Path, owner, c.Type == "archive"}, claim{inventory.ArtifactMetadataPath(c.Path), owner, true})
			}
		}
	}
	if len(artifacts) == 0 {
		return nil
	}
	// Project paths at mount boundaries without inspecting hidden children.
	var mounts []*plan.MountChange
	for _, service := range services {
		for _, f := range service.files {
			if c := f.definition; c != nil && c.Type == "mount" {
				p, err := e.Mount.CanonicalPath(ctx, c.Target)
				if err != nil {
					return err
				}
				mounts = append(mounts, &plan.MountChange{Target: p, State: service.service.State, TargetPending: true})
			}
		}
	}
	active := map[string]bool{}
	type observedMount struct {
		Target string `json:"target"`
		Device string `json:"maj:min"`
		Root   string `json:"fsroot"`
	}
	var observed struct {
		Filesystems []observedMount `json:"filesystems"`
	}
	{
		run := e.Mount.Run
		if run == nil {
			run = rpm.Run
		}
		out, err := run(ctx, "findmnt", "--json", "--list", "--output", "TARGET,MAJ:MIN,FSROOT")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(out, &observed); err != nil {
			return fmt.Errorf("invalid mount observation for artifact paths")
		}
		for _, fs := range observed.Filesystems {
			active[fs.Target] = true
		}
		for _, m := range mounts {
			m.TargetPending = !active[m.Target]
		}
	}
	for _, a := range artifacts {
		for _, m := range mounts {
			if !within(a.path, m.Target) {
				continue
			}
			if m.State == "absent" {
				return fmt.Errorf("%s shares a filesystem scheduled for unmount; remove artifacts in a separate apply before the mount", a.owner)
			}
			if !active[m.Target] {
				return fmt.Errorf("%s is inside an unmounted filesystem; apply the mount separately before preparing artifacts", a.owner)
			}
		}
	}
	// Filesystem-relative paths also identify aliases exposed by bind mounts.
	physical := func(p string) string {
		for _, m := range mounts {
			if m.TargetPending && within(p, m.Target) {
				return "pending:" + p
			}
		}
		var enclosing *observedMount
		for i := range observed.Filesystems {
			m := &observed.Filesystems[i]
			if m.Device != "" && m.Root != "" && within(p, m.Target) && (enclosing == nil || len(m.Target) > len(enclosing.Target)) {
				enclosing = m
			}
		}
		if enclosing == nil {
			return p
		}
		return enclosing.Device + ":" + path.Join(enclosing.Root, strings.TrimPrefix(p, enclosing.Target))
	}
	for i := range artifacts {
		canonical, err := e.Mount.ProjectPath(ctx, artifacts[i].path, mounts)
		if err != nil {
			return err
		}
		artifacts[i].path = physical(canonical)
	}
	for i, a := range artifacts {
		for _, b := range artifacts[i+1:] {
			if within(a.path, b.path) || within(b.path, a.path) {
				return fmt.Errorf("artifact resources owned by %s and %s overlap through target aliases", a.owner, b.owner)
			}
		}
	}
	for _, service := range services {
		for _, process := range service.service.Definition.Processes {
			if process.Type != "systemd" {
				continue
			}
			for _, prefix := range []string{"/etc/systemd/system/", "/usr/lib/systemd/system/", "/lib/systemd/system/", "/run/systemd/system/"} {
				for _, suffix := range []string{"", ".d"} {
					canonical, err := e.Mount.ProjectPath(ctx, prefix+process.Unit+suffix, mounts)
					if err != nil {
						return err
					}
					canonical = physical(canonical)
					for _, a := range artifacts {
						if within(canonical, a.path) || suffix == ".d" && within(a.path, canonical) {
							return fmt.Errorf("systemd unit %s overlaps a resource owned by %s", process.Unit, a.owner)
						}
					}
				}
			}
		}
		for _, f := range service.files {
			p, kind := "", "template"
			if f.file != nil {
				p = f.file.Path
			} else if c := f.definition; c != nil {
				kind = c.Type
				inputs := append(append([]string{}, c.Members...), c.Devices...)
				if c.Type == "mount" {
					inputs = append(inputs, c.Source)
				}
				for _, input := range inputs {
					canonical, err := e.Mount.ProjectPath(ctx, input, mounts)
					if err != nil {
						return err
					}
					canonical = physical(canonical)
					for _, a := range artifacts {
						if canonical == a.path || a.tree && within(canonical, a.path) {
							return fmt.Errorf("%s/%s consumes a resource owned by %s", service.service.Name, f.component, a.owner)
						}
					}
				}
				switch c.Type {
				case "file", "directory":
					p = c.Path
				case "mount":
					p = c.Target
				case "mdadm":
					p = c.Array
				case "lvm-lv":
					p = "/dev/" + c.VG + "/" + c.LV
				}
			}
			if p == "" {
				continue
			}
			if kind == "mount" {
				fstab, err := e.Mount.CanonicalPath(ctx, "/etc/fstab")
				if err != nil {
					return err
				}
				fstab = physical(fstab)
				for _, a := range artifacts {
					if fstab == a.path || a.tree && within(fstab, a.path) {
						return fmt.Errorf("mount fstab writes resolve into a resource owned by %s", a.owner)
					}
				}
			}
			canonical, err := e.Mount.ProjectPath(ctx, p, mounts)
			if err != nil {
				return err
			}
			canonical = physical(canonical)
			for _, a := range artifacts {
				conflict := canonical == a.path || a.tree && within(canonical, a.path)
				if within(a.path, canonical) && (kind != "directory" && kind != "mount" || service.service.State == "absent") {
					conflict = true
				}
				if conflict {
					return fmt.Errorf("%s/%s resolves into a resource owned by %s", service.service.Name, f.component, a.owner)
				}
			}
		}
	}
	return nil
}

func within(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}
