package inventory

import (
	"fmt"
	"sort"
)

// Resolve applies the union of host names and tags, intersected with services.
// Ownership and tag conflicts are checked for all assignments on selected hosts
// before service filtering. No dependency services are added automatically.
func (in *Inventory) Resolve(s Selectors) (*Resolved, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	hostNames := map[string]bool{}
	for _, h := range in.Hosts {
		hostNames[h.Name] = true
	}
	for _, name := range s.Hosts {
		if !hostNames[name] {
			return nil, fmt.Errorf("host selector %q: unknown host", name)
		}
	}
	for _, name := range s.Tags {
		if _, ok := in.Tags[name]; !ok {
			return nil, fmt.Errorf("tag selector %q: unknown tag", name)
		}
	}
	for _, name := range s.Services {
		if _, ok := in.Services[name]; !ok {
			return nil, fmt.Errorf("service selector %q: unknown service", name)
		}
	}
	hostSet, tagSet, serviceSet := stringSet(s.Hosts), stringSet(s.Tags), stringSet(s.Services)
	out := &Resolved{Hosts: []ResolvedHost{}, SecretSources: in.SecretSources}
	hosts := append([]Host(nil), in.Hosts...)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	for _, h := range hosts {
		selected := len(hostSet)+len(tagSet) == 0 || hostSet[h.Name]
		for _, tag := range h.Tags {
			selected = selected || tagSet[tag]
		}
		if !selected {
			continue
		}
		vars, err := MergeTags(h.Name, h.Tags, in.Tags)
		if err != nil {
			return nil, err
		}
		if err := in.validateOwnership(h); err != nil {
			return nil, err
		}
		rh := ResolvedHost{Name: h.Name, Tags: append([]string{}, h.Tags...), Transport: h.Transport, Vars: vars, Services: []ResolvedService{}}
		sort.Strings(rh.Tags)
		assignments := append([]Assignment(nil), h.Services...)
		sort.Slice(assignments, func(i, j int) bool { return assignments[i].Order < assignments[j].Order })
		for _, a := range assignments {
			if len(serviceSet) > 0 && !serviceSet[a.Name] {
				continue
			}
			if a.State == "" {
				a.State = "present"
			}
			rh.Services = append(rh.Services, ResolvedService{Assignment: a, Definition: normalizedService(in.Services[a.Name])})
		}
		out.Hosts = append(out.Hosts, rh)
	}
	return out, nil
}

func stringSet(values []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return m
}

func normalizedService(s Service) Service {
	// Copy maps before applying defaults so repeated resolutions are independent.
	binaries := make(map[string]Binary, len(s.Binaries))
	for k, b := range s.Binaries {
		if b.Manager == "" {
			b.Manager = "dnf"
		}
		binaries[k] = b
	}
	s.Binaries = binaries
	processes := make(map[string]Process, len(s.Processes))
	for k, p := range s.Processes {
		if p.Timeout == "" {
			p.Timeout = "5s"
		}
		if p.State == "" {
			p.State = "running"
		}
		processes[k] = p
	}
	s.Processes = processes
	return s
}
