package inventory

import (
	"fmt"
	"path"
	"strings"
)

func (in *Inventory) validateOwnership(h Host) error {
	type owner struct{ service, component string }
	owners := map[string]owner{}
	claim := func(resource, service, component string) error {
		if old, ok := owners[resource]; ok {
			if old.service != service || (!strings.HasPrefix(resource, "systemd unit ") && old.component != component) {
				return fmt.Errorf("host %q: resource %s is owned by both %s/%s and %s/%s", h.Name, resource, old.service, old.component, service, component)
			}
		}
		owners[resource] = owner{service, component}
		return nil
	}
	claimFile := func(c Config, service, component string) error {
		p := path.Clean(c.Path)
		if err := claim("file "+p, service, component); err != nil {
			return err
		}
		if unit := unitFromPath(p); unit != "" {
			return claim("systemd unit "+unit, service, component)
		}
		return nil
	}
	for _, a := range h.Services {
		s := in.Services[a.Name]
		processUnits := map[string]string{}
		for _, name := range sortedKeys(s.Binaries) {
			// DNF and YUM both own packages in the RPM database.
			if err := claim("package rpm:"+s.Binaries[name].Package, a.Name, "binary."+name); err != nil {
				return err
			}
		}
		for _, name := range sortedKeys(s.Configs) {
			if err := claimFile(s.Configs[name], a.Name, "config."+name); err != nil {
				return err
			}
		}
		for _, name := range sortedKeys(s.Processes) {
			p := s.Processes[name]
			component := "process." + name
			if previous, ok := processUnits[p.Unit]; ok {
				return fmt.Errorf("host %q: systemd unit %s is managed by both %s/process.%s and %s/%s", h.Name, p.Unit, a.Name, previous, a.Name, component)
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
