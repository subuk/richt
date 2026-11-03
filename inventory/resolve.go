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
		assignments, err := in.assignments(h)
		if err != nil {
			return nil, err
		}
		if err := in.validateOwnership(h.Name, assignments); err != nil {
			return nil, err
		}
		rh := ResolvedHost{Name: h.Name, Tags: append([]string{}, h.Tags...), Transport: h.Transport, Vars: vars, Services: []ResolvedService{}}
		sort.Strings(rh.Tags)
		for _, a := range assignments {
			service := ResolvedService{Assignment: a, Definition: normalizedService(in.Services[a.Name].Service)}
			rh.OwnershipServices = append(rh.OwnershipServices, service)
			if len(serviceSet) > 0 && !serviceSet[a.Name] {
				continue
			}
			rh.Services = append(rh.Services, service)
		}
		out.Hosts = append(out.Hosts, rh)
	}
	return out, nil
}

// assignments expands a selected host's tags before service filtering. A tag's
// variable priority never overrides an assignment contributed by another tag.
func (in *Inventory) assignments(h Host) ([]Assignment, error) {
	tags := append([]string(nil), h.Tags...)
	sort.Strings(tags)
	sources := map[string]string{}
	assignments := []Assignment{}
	for _, tag := range tags {
		for _, name := range sortedKeys(in.Tags[tag].Services) {
			if previous, ok := sources[name]; ok {
				return nil, fmt.Errorf("host %q: service %q is assigned by both tags %q and %q; assign it through only one tag", h.Name, name, previous, tag)
			}
			sources[name] = tag
			state := in.Tags[tag].Services[name].State
			if state == "" {
				state = "present"
			}
			assignments = append(assignments, Assignment{Name: name, Order: *in.Services[name].Order, State: state})
		}
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].Order != assignments[j].Order {
			return assignments[i].Order < assignments[j].Order
		}
		return assignments[i].Name < assignments[j].Name
	})
	for i := 1; i < len(assignments); i++ {
		a, b := assignments[i-1], assignments[i]
		if a.Order == b.Order {
			return nil, fmt.Errorf("host %q services: order %d is shared by services %q (tag %q) and %q (tag %q)", h.Name, a.Order, a.Name, sources[a.Name], b.Name, sources[b.Name])
		}
	}
	return assignments, nil
}

func stringSet(values []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return m
}

func normalizedService(s Service) Service {
	// Copy slices before applying defaults so repeated resolutions are independent.
	binaries := make([]Binary, len(s.Binaries))
	for k, b := range s.Binaries {
		if b.Type == "rpm" && b.Manager == "" {
			b.Manager = "dnf"
		}
		binaries[k] = b
	}
	s.Binaries = binaries
	processes := make([]Process, len(s.Processes))
	for k, p := range s.Processes {
		if p.Timeout == "" {
			p.Timeout = "5s"
		}
		if p.Type == "systemd" && p.State == "" {
			p.State = "running"
		}
		processes[k] = p
	}
	s.Processes = processes
	return s
}
