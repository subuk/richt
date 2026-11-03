package inventory

import (
	"fmt"
	"path"
	"strings"
)

func (in *Inventory) validateOwnership(host string, assignments []Assignment) error {
	type owner struct{ service, component string }
	owners := map[string]owner{}
	// Artifact trees and metadata are exclusively owned. Other paths are
	// recorded to check containment, including same-service nested resources.
	type pathOwner struct {
		path, service, component          string
		tree, artifact, directory, absent bool
	}
	paths := []pathOwner{}
	unitPaths := []pathOwner{}
	fstabEntries := false
	fstabFile := false
	type removal struct{ path, service string }
	removals := []removal{}
	claim := func(resource, service, component string) error {
		if old, ok := owners[resource]; ok {
			if old.service != service || (!strings.HasPrefix(resource, "systemd unit ") && old.component != component) {
				return fmt.Errorf("host %q: resource %s is owned by both %s/%s and %s/%s", host, resource, old.service, old.component, service, component)
			}
		}
		owners[resource] = owner{service, component}
		return nil
	}
	claimFile := func(c Config, service, component string) error {
		p := path.Clean(c.Path)
		paths = append(paths, pathOwner{path: p, service: service, component: component, directory: c.Type == "directory"})
		if err := claim("file "+p, service, component); err != nil {
			return err
		}
		if unit := unitFromPath(p); unit != "" {
			return claim("systemd unit "+unit, service, component)
		}
		return nil
	}
	for _, a := range assignments {
		s := in.Services[a.Name]
		processUnits := map[string]string{}
		for i, b := range s.Binaries {
			name := componentLabel(b.Name, i)
			if b.Type == "download" || b.Type == "archive" {
				component := "binary." + name
				if err := claimFile(Config{Path: b.Path}, a.Name, component); err != nil {
					return err
				}
				paths[len(paths)-1].artifact = true
				paths[len(paths)-1].tree = b.Type == "archive"
				paths = append(paths, pathOwner{path: ArtifactMetadataPath(b.Path), service: a.Name, component: component + ".metadata", tree: true, artifact: true})
				continue
			}
			// DNF and YUM both own packages in the RPM database.
			if err := claim("package rpm:"+b.Package, a.Name, "binary."+name); err != nil {
				return err
			}
		}
		for i, c := range s.Configs {
			component := "config." + componentLabel(c.Name, i)
			resource := ""
			switch c.Type {
			case "mdadm":
				resource = "block device " + path.Clean(c.Array)
				for _, device := range c.Members {
					paths = append(paths, pathOwner{path: path.Clean(device), service: a.Name, component: component + ".members"})
					if err := claim("device consumer "+path.Clean(device), a.Name, component); err != nil {
						return err
					}
				}
			case "lvm-vg":
				resource = "volume group " + c.VG
				for _, device := range c.Devices {
					paths = append(paths, pathOwner{path: path.Clean(device), service: a.Name, component: component + ".devices"})
					if err := claim("device consumer "+path.Clean(device), a.Name, component); err != nil {
						return err
					}
				}
			case "lvm-lv":
				resource = "block device /dev/" + c.VG + "/" + c.LV
			case "mount":
				resource = "mount " + path.Clean(c.Target)
				paths = append(paths, pathOwner{path: path.Clean(c.Source), service: a.Name, component: component + ".source"})
				paths = append(paths, pathOwner{path: path.Clean(c.Target), service: a.Name, component: component, directory: true})
				paths = append(paths, pathOwner{path: "/etc/fstab", service: a.Name, component: component + ".fstab"})
				fstabEntries = true
				if err := claim("file "+path.Clean(c.Target), a.Name, component); err != nil {
					return err
				}
			default:
				if path.Clean(c.Path) == "/etc/fstab" {
					fstabFile = true
				}
				if err := claimFile(c, a.Name, component); err != nil {
					return err
				}
			}
			if c.Type == "directory" && a.State == "absent" {
				removals = append(removals, removal{path.Clean(c.Path), a.Name})
				paths[len(paths)-1].absent = true
			}
			if strings.HasPrefix(resource, "block device ") {
				paths = append(paths, pathOwner{path: strings.TrimPrefix(resource, "block device "), service: a.Name, component: component})
				if err := claim("file "+strings.TrimPrefix(resource, "block device "), a.Name, component); err != nil {
					return err
				}
			}
			if resource != "" {
				if err := claim(resource, a.Name, component); err != nil {
					return err
				}
			}
		}

		for i, p := range s.Processes {
			if p.Type != "systemd" {
				continue
			}
			name := componentLabel(p.Name, i)
			component := "process." + name
			for _, prefix := range []string{"/etc/systemd/system/", "/usr/lib/systemd/system/", "/lib/systemd/system/", "/run/systemd/system/"} {
				unitPaths = append(unitPaths, pathOwner{path: prefix + p.Unit, service: a.Name, component: component})
				unitPaths = append(unitPaths, pathOwner{path: prefix + p.Unit + ".d", service: a.Name, component: component, tree: true})
			}
			if previous, ok := processUnits[p.Unit]; ok {
				return fmt.Errorf("host %q: systemd unit %s is managed by both %s/process.%s and %s/%s", host, p.Unit, a.Name, previous, a.Name, component)
			}
			processUnits[p.Unit] = name
			if err := claim("systemd unit "+p.Unit, a.Name, component); err != nil {
				return err
			}
			if p.UnitFile != nil {
				if err := claimFile(*p.UnitFile, a.Name, component+".unit_file"); err != nil {
					return err
				}
			}
			for _, key := range sortedKeys(p.Overrides) {
				if err := claimFile(p.Overrides[key], a.Name, component+".overrides."+key); err != nil {
					return err
				}
			}
		}
	}
	conflict := func(a, b pathOwner) error {
		return fmt.Errorf("host %q: artifact resource scope conflicts between %s/%s (%s) and %s/%s (%s)", host, a.service, a.component, a.path, b.service, b.component, b.path)
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if !a.artifact && !b.artifact {
				continue
			}
			if a.path == b.path || strings.HasPrefix(b.path, a.path+"/") && (a.tree || !a.directory || a.absent) || strings.HasPrefix(a.path, b.path+"/") && (b.tree || !b.directory || b.absent) {
				return conflict(a, b)
			}
		}
		if a.artifact {
			for _, b := range unitPaths {
				if b.path == a.path || strings.HasPrefix(b.path, a.path+"/") || b.tree && strings.HasPrefix(a.path, b.path+"/") {
					return conflict(a, b)
				}
			}
		}
	}
	for _, r := range removals {
		for _, resource := range sortedKeys(owners) {
			owner := owners[resource]
			if strings.HasPrefix(resource, "file "+r.path+"/") && owner.service != r.service {
				return fmt.Errorf("host %q: directory %s deletion by %s contains resource %s owned by %s/%s", host, r.path, r.service, resource, owner.service, owner.component)
			}
		}
	}
	if fstabEntries && fstabFile {
		return fmt.Errorf("host %q: /etc/fstab whole-file ownership conflicts with mount entry management", host)
	}
	return nil
}

func unitFromPath(p string) string {
	for _, prefix := range []string{"/etc/systemd/system/", "/usr/lib/systemd/system/", "/lib/systemd/system/", "/run/systemd/system/"} {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.Split(strings.TrimPrefix(p, prefix), "/")[0]
		name = strings.TrimSuffix(name, ".d")
		if unitPattern.MatchString(name) {
			return name
		}
	}
	return ""
}
