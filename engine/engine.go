// Package engine prepares complete services and dispatches their semantic operations.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/secrets"
	"github.com/subuk/richt/systemd"
	"github.com/subuk/richt/transport"
)

type Local struct {
	RPM     rpm.Backend
	Systemd systemd.Backend
	Files   *config.Target
	// Connect optionally supplies a target command boundary for SSH and sudo.
	Connect func(inventory.Transport) (config.Target, error)
}

// ForHost binds the existing resource executors without contacting the host.
func (e Local) ForHost(h plan.Host) (plan.Executor, error) {
	c := inventory.Transport{Type: h.Transport}
	if h.Connection != nil {
		c.Host = h.Connection.Host
		c.User = h.Connection.User
		c.Port = h.Connection.Port
		c.Sudo = h.Connection.Sudo
	}
	if c.Type == "local" && !c.Sudo {
		return e, nil
	}
	var target config.Target
	if e.Connect != nil {
		var err error
		target, err = e.Connect(c)
		if err != nil {
			return nil, err
		}
	} else {
		client, err := transport.New(c)
		if err != nil {
			return nil, err
		}
		target = config.Target{Run: client.Run, RunInput: client.RunInput}
	}
	e.Files = &target
	e.RPM = rpm.Backend{Run: target.Run}
	e.Systemd = systemd.Backend{Run: target.Run, ReadFile: target.ReadFile, Glob: target.Glob}
	return e, nil
}

func host(h inventory.ResolvedHost) plan.Host {
	p := plan.Host{Name: h.Name, Transport: h.Transport.Type, Tasks: []plan.Task{}}
	c := h.Transport
	if c.Type == "ssh" || c.Sudo {
		p.Connection = &plan.Connection{Host: c.Host, User: c.User, Port: c.Port, Sudo: c.Sudo}
	}
	return p
}

func (e Local) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation == "daemon-reload" && (t.File != nil || t.RPM != nil || t.Process != nil) {
		return nil, errors.New("daemon-reload task must not contain a resource payload")
	}
	switch t.Operation {
	case "file":
		return (config.Local{}).Resolve(t)
	case "rpm":
		return e.RPM.Resolve(t)
	case "systemd", "systemd-enable", "daemon-reload":
		return e.Systemd.Resolve(t)
	default:
		return nil, fmt.Errorf("unsupported operation %q", t.Operation)
	}
}
func (e Local) Execute(ctx context.Context, a plan.Action) error {
	switch a.Kind {
	case "rpm":
		return e.RPM.Execute(ctx, a)
	case "systemd", "systemd-enable", "daemon-reload":
		return e.Systemd.Execute(ctx, a)
	default:
		if e.Files != nil {
			return e.Files.Execute(ctx, a)
		}
		return (config.Local{}).Execute(ctx, a)
	}
}
func Diff(r *inventory.Resolved) (*plan.Plan, error) { return (Local{}).Diff(context.Background(), r) }

type preparedFile struct {
	name, ref, process string
	file               *plan.FileChange
	changed            bool
}
type preparedService struct {
	service  inventory.ResolvedService
	files    []preparedFile
	binaries map[string]*plan.RPMChange
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func task(service, component, operation string) plan.Task {
	return plan.Task{ID: service + "/" + component, Service: service, Component: component, Operation: operation, Progress: plan.Progress{Status: "pending"}}
}

// Diff completes rendering before host observation. No repository is contacted.
func (e Local) Diff(ctx context.Context, r *inventory.Resolved) (*plan.Plan, error) {
	if r == nil {
		return nil, errors.New("resolved inventory is nil")
	}
	prepared := make([][]preparedService, len(r.Hosts))
	lookup := secrets.New(r.SecretSources)
	for hi, h := range r.Hosts {
		for _, s := range h.Services {
			ps := preparedService{service: s, binaries: map[string]*plan.RPMChange{}}
			add := func(name, ref, process string, c inventory.Config) error {
				f, err := config.Prepare(c, s.State, h.Vars, lookup)
				if err != nil {
					return fmt.Errorf("host %s service %s %s: %w", h.Name, s.Name, name, err)
				}
				if process == "" {
					for key, value := range s.Definition.Processes {
						unitPath := "/etc/systemd/system/" + value.Unit
						if f.Path == unitPath || strings.HasPrefix(f.Path, unitPath+".d/") {
							process = key
						}
					}
				}
				if process != "" && f.State != "absent" {
					if _, err := systemd.ReloadSetting(f.Content, false); err != nil {
						if f.Sensitive {
							err = errors.New("sensitive unit parsing failed")
						}
						return fmt.Errorf("%s: %w", name, err)
					}
				}
				ps.files = append(ps.files, preparedFile{name: name, ref: ref, process: process, file: f})
				return nil
			}
			for _, name := range keys(s.Definition.Binaries) {
				c, err := rpm.Prepare(s.Definition.Binaries[name], s.State)
				if err != nil {
					return nil, fmt.Errorf("service %s binary %s: %w", s.Name, name, err)
				}
				ps.binaries[name] = c
			}
			for _, name := range keys(s.Definition.Configs) {
				if err := add("config/"+name, "config."+name, "", s.Definition.Configs[name]); err != nil {
					return nil, err
				}
			}
			for _, name := range keys(s.Definition.Processes) {
				p := s.Definition.Processes[name]
				if p.UnitFile != nil {
					if err := add("process/"+name+"/unit", "", name, *p.UnitFile); err != nil {
						return nil, err
					}
				}
				for _, key := range keys(p.Overrides) {
					if err := add("process/"+name+"/override/"+key, "", name, p.Overrides[key]); err != nil {
						return nil, err
					}
				}
			}
			prepared[hi] = append(prepared[hi], ps)
		}
	}
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{}}
	// Capability checks happen before observing the first service or writing anything.
	bound := make([]Local, len(r.Hosts))
	for hi, h := range r.Hosts {
		executor, err := e.ForHost(host(h))
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Name, err)
		}
		bound[hi] = executor.(Local)
		checkedManagers := map[string]bool{}
		for _, service := range prepared[hi] {
			for _, binary := range service.binaries {
				if !checkedManagers[binary.Manager] {
					if err := bound[hi].RPM.CheckManager(ctx, binary.Manager); err != nil {
						return nil, fmt.Errorf("host %s: %w", h.Name, err)
					}
					checkedManagers[binary.Manager] = true
				}
			}
		}
	}
	for hi, h := range r.Hosts {
		ph := host(h)
		for _, ps := range prepared[hi] {
			tasks, err := bound[hi].observe(ctx, ps)
			if err != nil {
				return nil, fmt.Errorf("host %s service %s: %w", h.Name, ps.service.Name, err)
			}
			ph.Tasks = append(ph.Tasks, tasks...)
		}
		p.Hosts = append(p.Hosts, ph)
	}
	return p, p.Validate()
}
func processOrder(processes map[string]inventory.Process) ([]string, error) {
	var out []string
	seen := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		if seen[n] == 1 {
			return errors.New("cycle in process after references")
		}
		if seen[n] == 2 {
			return nil
		}
		p, ok := processes[n]
		if !ok {
			return fmt.Errorf("unknown process %s in after", n)
		}
		seen[n] = 1
		deps := append([]string(nil), p.After...)
		sort.Strings(deps)
		for _, ref := range deps {
			if err := visit(ref); err != nil {
				return err
			}
		}
		seen[n] = 2
		out = append(out, n)
		return nil
	}
	for _, n := range keys(processes) {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (e Local) observe(ctx context.Context, ps preparedService) ([]plan.Task, error) {
	s := ps.service
	absent := s.State == "absent"
	changed := map[string]bool{}
	unitChanged := map[string]bool{}
	daemonReload := false
	var binaries, files, processes []plan.Task
	for _, name := range keys(ps.binaries) {
		c := ps.binaries[name]
		yes, err := e.RPM.Observe(ctx, c)
		if err != nil {
			return nil, err
		}
		if yes {
			changed["binary."+name] = true
			t := task(s.Name, "binary/"+name, "rpm")
			t.RPM = c
			binaries = append(binaries, t)
		}
	}
	for i := range ps.files {
		f := &ps.files[i]
		var yes bool
		var err error
		if e.Files != nil {
			yes, err = e.Files.Observe(ctx, f.file)
		} else {
			yes, err = config.Observe(f.file)
		}
		if err != nil {
			if f.file.Sensitive {
				err = errors.New("sensitive file observation failed")
			}
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		f.changed = yes
		if yes {
			if strings.HasPrefix(f.file.Path, "/etc/systemd/system/") || strings.HasPrefix(f.file.Path, "/usr/lib/systemd/system/") || strings.HasPrefix(f.file.Path, "/run/systemd/system/") {
				daemonReload = true
			}
			if f.ref != "" {
				changed[f.ref] = true
			}
			if f.process != "" {
				unitChanged[f.process] = true
			}
			t := task(s.Name, f.name, "file")
			t.File = f.file
			files = append(files, t)
		}
	}
	order, err := processOrder(s.Definition.Processes)
	if err != nil {
		return nil, err
	}
	if absent {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	for _, name := range order {
		v := s.Definition.Processes[name]
		var unitFiles []*plan.FileChange
		sensitiveUnit := false
		for _, f := range ps.files {
			if f.process == name {
				unitFiles = append(unitFiles, f.file)
				sensitiveUnit = sensitiveUnit || f.file.Sensitive
			}
		}
		state := v.State
		if state == "" {
			state = "running"
		}
		timeout := v.Timeout
		if timeout == "" {
			timeout = "5s"
		}
		c := &plan.ProcessChange{Unit: v.Unit, State: state, Enabled: v.Enabled, Timeout: timeout, MissingOK: absent}
		if absent {
			no := false
			c.State = "stopped"
			c.Enabled = &no
		} else {
			for _, ref := range v.ReloadOn {
				if changed[ref] {
					c.Reaction = "reload"
				}
			}
			for _, ref := range v.RestartOn {
				if changed[ref] {
					c.Reaction = "restart"
				}
			}
			if unitChanged[name] {
				c.Reaction = "restart"
			}
		}
		if err := systemd.Validate(c); err != nil {
			return nil, err
		}
		_, status, err := e.Systemd.NeedsChange(ctx, c)
		if err != nil {
			if sensitiveUnit {
				err = errors.New("sensitive unit observation failed")
			}
			return nil, err
		}
		// Reload declarations are capabilities, even when no input currently changes.
		if !absent && len(v.ReloadOn) > 0 {
			canReload, err := e.Systemd.ReloadSupported(ctx, v.Unit, status, unitFiles)
			if err != nil {
				return nil, fmt.Errorf("process %s: %w", name, err)
			}
			if !canReload {
				return nil, fmt.Errorf("process %s: reload is unsupported or cannot be verified before installation; provide a unit_file or override with ExecReload", name)
			}
		}
		if !absent && status.Load == "not-found" && v.UnitFile == nil && len(binaries) == 0 {
			return nil, fmt.Errorf("process %s: unit %s does not exist and no package installation supplies it", name, v.Unit)
		}
		var enableTask *plan.Task
		if c.Enabled != nil && ((status.Load != "not-found" && systemd.Enabled(status) != *c.Enabled) || (!absent && status.Load == "not-found")) {
			ec := *c
			t := task(s.Name, "process/"+name+"/enabled", "systemd-enable")
			t.Process = &ec
			enableTask = &t
		}
		c.Enabled = nil
		// Enablement and lifecycle each have independent persisted progress.
		if !absent && enableTask != nil {
			processes = append(processes, *enableTask)
		}
		lifecycleNeeded := status.Load == "not-found" && c.State == "running" || status.Active == "active" && c.State == "stopped" || (status.Active == "inactive" || status.Active == "failed") && c.State == "running"
		if lifecycleNeeded || (!absent && (c.Reaction != "" || unitChanged[name])) {
			t := task(s.Name, "process/"+name, "systemd")
			t.Process = c
			processes = append(processes, t)
		}
		if absent && enableTask != nil {
			processes = append(processes, *enableTask)
		}
	}
	var out []plan.Task
	if absent {
		out = append(out, processes...)
		out = append(out, files...)
		out = append(out, binaries...)
	} else {
		out = append(out, binaries...)
		out = append(out, files...)
	}
	if daemonReload || len(unitChanged) > 0 || (len(binaries) > 0 && len(s.Definition.Processes) > 0) {
		out = append(out, task(s.Name, "systemd/daemon-reload", "daemon-reload"))
	}
	if !absent {
		out = append(out, processes...)
	}
	return out, nil
}
