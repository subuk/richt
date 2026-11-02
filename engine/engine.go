// Package engine prepares complete services and dispatches their semantic operations.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	case "systemd", "systemd-enable", "systemd-mask", "daemon-reload":
		return e.Systemd.Resolve(t)
	default:
		return nil, fmt.Errorf("unsupported operation %q", t.Operation)
	}
}
func (e Local) Execute(ctx context.Context, a plan.Action) error {
	switch a.Kind {
	case "rpm":
		return e.RPM.Execute(ctx, a)
	case "systemd", "systemd-enable", "systemd-mask", "daemon-reload":
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
	id, component, ref, process string
	file                        *plan.FileChange
	changed                     bool
}
type preparedService struct {
	service  inventory.ResolvedService
	files    []preparedFile
	binaries []preparedBinary
}

type preparedBinary struct {
	index  int
	name   string
	change *plan.RPMChange
}

func label(name string, index int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("[%d]", index+1)
}

func positions(length int, reverse bool) []int {
	out := make([]int, length)
	for i := range out {
		out[i] = i
		if reverse {
			out[i] = length - 1 - i
		}
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func task(service, id, component, operation string) plan.Task {
	return plan.Task{ID: service + "/" + id, Service: service, Component: component, Operation: operation, Progress: plan.Progress{Status: "pending"}}
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
			ps := preparedService{service: s}
			add := func(id, component, ref, process string, c inventory.Config) error {
				f, err := config.Prepare(c, s.State, h.Vars, lookup)
				if err != nil {
					return fmt.Errorf("host %s service %s %s: %w", h.Name, s.Name, component, err)
				}
				if process == "" {
					for key, value := range s.Definition.Processes {
						unitPath := "/etc/systemd/system/" + value.Unit
						if f.Path == unitPath || strings.HasPrefix(f.Path, unitPath+".d/") {
							process = strconv.Itoa(key)
						}
					}
				}
				if process != "" && f.State != "absent" {
					if _, err := systemd.ReloadSetting(f.Content, false); err != nil {
						if f.Sensitive {
							err = errors.New("sensitive unit parsing failed")
						}
						return fmt.Errorf("%s: %w", component, err)
					}
				}
				ps.files = append(ps.files, preparedFile{id: id, component: component, ref: ref, process: process, file: f})
				return nil
			}
			for index, binary := range s.Definition.Binaries {
				c, err := rpm.Prepare(binary, s.State)
				if err != nil {
					return nil, fmt.Errorf("service %s binary %s: %w", s.Name, label(binary.Name, index), err)
				}
				ps.binaries = append(ps.binaries, preparedBinary{index: index, name: binary.Name, change: c})
			}
			for _, index := range positions(len(s.Definition.Configs), s.State == "absent") {
				c := s.Definition.Configs[index]
				ref := ""
				if c.Name != "" {
					ref = "config." + c.Name
				}
				if err := add("config/"+strconv.Itoa(index), "config/"+label(c.Name, index), ref, "", c); err != nil {
					return nil, err
				}
			}
			for _, index := range positions(len(s.Definition.Processes), s.State == "absent") {
				p := s.Definition.Processes[index]
				id, component := "process/"+strconv.Itoa(index), "process/"+label(p.Name, index)
				if p.UnitFile != nil {
					if err := add(id+"/unit", component+"/unit", "", strconv.Itoa(index), *p.UnitFile); err != nil {
						return nil, err
					}
				}
				for _, key := range keys(p.Overrides) {
					if err := add(id+"/override/"+key, component+"/override/"+key, "", strconv.Itoa(index), p.Overrides[key]); err != nil {
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
			for _, prepared := range service.binaries {
				binary := prepared.change
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
func (e Local) observe(ctx context.Context, ps preparedService) ([]plan.Task, error) {
	s := ps.service
	absent := s.State == "absent"
	changed := map[string]bool{}
	unitChanged := map[string]bool{}
	daemonReload := false
	var binaries, files, processes, unmasks []plan.Task
	for _, position := range positions(len(ps.binaries), absent) {
		binary := ps.binaries[position]
		c := binary.change
		yes, err := e.RPM.Observe(ctx, c)
		if err != nil {
			return nil, err
		}
		if yes {
			if binary.name != "" {
				changed["binary."+binary.name] = true
			}
			t := task(s.Name, "binary/"+strconv.Itoa(binary.index), "binary/"+label(binary.name, binary.index), "rpm")
			t.RPM = c
			binaries = append(binaries, t)
		}
	}
	order := positions(len(s.Definition.Processes), absent)
	processLabels := map[string]string{}
	for index, process := range s.Definition.Processes {
		processLabels[strconv.Itoa(index)] = label(process.Name, index)
	}
	statuses := map[string]systemd.Status{}
	processChanges := map[string]*plan.ProcessChange{}
	for _, index := range order {
		name := strconv.Itoa(index)
		v := s.Definition.Processes[index]
		state, timeout := v.State, v.Timeout
		if state == "" {
			state = "running"
		}
		if timeout == "" {
			timeout = "5s"
		}
		c := &plan.ProcessChange{Unit: v.Unit, State: state, Enabled: v.Enabled, Masked: v.Masked, Timeout: timeout, MissingOK: absent}
		if absent {
			no := false
			c.State, c.Enabled, c.Masked = "stopped", &no, nil
		}
		if err := systemd.Validate(c); err != nil {
			return nil, err
		}
		_, status, err := e.Systemd.NeedsChange(ctx, c)
		if err != nil {
			for _, f := range ps.files {
				if f.process == name && f.file.Sensitive {
					err = errors.New("sensitive unit observation failed")
				}
			}
			return nil, err
		}
		statuses[name], processChanges[name] = status, c
	}
	for i := range ps.files {
		f := &ps.files[i]
		unmaskFullUnit := false
		if c := processChanges[f.process]; c != nil && f.file.Path == "/etc/systemd/system/"+c.Unit {
			if c.Masked != nil && *c.Masked {
				return nil, fmt.Errorf("process %s: a persistent mask cannot coexist with an owned full unit", processLabels[f.process])
			}
			if systemd.Masked(statuses[f.process]) {
				if absent || c.Masked == nil {
					return nil, fmt.Errorf("process %s: owned full unit is masked; explicitly unmask it with masked: false in a present assignment before writing or removing it", processLabels[f.process])
				}
				unmaskFullUnit = true
			}
		}
		var yes bool
		var err error
		if unmaskFullUnit && e.Files != nil {
			yes, err = e.Files.ObserveMissing(ctx, f.file)
		} else if unmaskFullUnit {
			yes, err = config.ObserveMissing(f.file)
		} else if e.Files != nil {
			yes, err = e.Files.Observe(ctx, f.file)
		} else {
			yes, err = config.Observe(f.file)
		}
		if err != nil {
			if f.file.Sensitive {
				err = errors.New("sensitive file observation failed")
			}
			return nil, fmt.Errorf("%s: %w", f.component, err)
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
			t := task(s.Name, f.id, f.component, "file")
			t.File = f.file
			files = append(files, t)
		}
	}
	for _, index := range order {
		name := strconv.Itoa(index)
		v := s.Definition.Processes[index]
		var unitFiles []*plan.FileChange
		hasFullUnit := false
		for _, f := range ps.files {
			if f.process == name {
				unitFiles = append(unitFiles, f.file)
				hasFullUnit = hasFullUnit || f.file.Path == "/etc/systemd/system/"+v.Unit
			}
		}
		c, status := processChanges[name], statuses[name]
		unmaskRequested := c.Masked != nil && !*c.Masked && systemd.Masked(status)
		if !absent {
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
		// Reload declarations are capabilities, even when no input currently changes.
		if !absent && len(v.ReloadOn) > 0 {
			canReload, err := e.Systemd.ReloadSupported(ctx, v.Unit, status, unitFiles)
			if err != nil {
				return nil, fmt.Errorf("process %s: %w", processLabels[name], err)
			}
			if !canReload {
				return nil, fmt.Errorf("process %s: reload is unsupported or cannot be verified before installation; provide a unit_file or override with ExecReload", processLabels[name])
			}
		}
		if !absent && status.Load == "not-found" && !hasFullUnit && len(binaries) == 0 && !(c.Masked != nil && (*c.Masked || c.State == "stopped")) {
			return nil, fmt.Errorf("process %s: unit %s does not exist and no package installation supplies it", processLabels[name], v.Unit)
		}
		var maskTask *plan.Task
		if c.Masked != nil && systemd.MaskNeedsChange(status, *c.Masked) {
			mc := *c
			mc.Enabled = nil
			mc.Reaction = ""
			t := task(s.Name, "process/"+name+"/masked", "process/"+label(v.Name, index)+"/masked", "systemd-mask")
			t.Process = &mc
			maskTask = &t
			if !*c.Masked {
				// Removing a full-unit mask must precede file writes, which follow symlinks.
				unmasks = append(unmasks, t)
			}
		}
		c.Masked = nil
		var enableTask *plan.Task
		if c.Enabled != nil && (unmaskRequested || (status.Load != "not-found" && systemd.Enabled(status) != *c.Enabled) || (!absent && status.Load == "not-found" && (*c.Enabled || hasFullUnit || len(binaries) > 0))) {
			ec := *c
			if unmaskRequested && !*c.Enabled && !hasFullUnit && len(binaries) == 0 {
				ec.MissingOK = true
			}
			t := task(s.Name, "process/"+name+"/enabled", "process/"+label(v.Name, index)+"/enabled", "systemd-enable")
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
			t := task(s.Name, "process/"+name, "process/"+label(v.Name, index), "systemd")
			t.Process = c
			processes = append(processes, t)
		}
		if absent && enableTask != nil {
			processes = append(processes, *enableTask)
		}
		if maskTask != nil && *maskTask.Process.Masked {
			processes = append(processes, *maskTask)
		}
	}
	var out []plan.Task
	if absent {
		out = append(out, processes...)
		out = append(out, files...)
		out = append(out, binaries...)
	} else {
		out = append(out, binaries...)
		out = append(out, unmasks...)
		out = append(out, files...)
	}
	if daemonReload || len(unitChanged) > 0 || (len(binaries) > 0 && len(s.Definition.Processes) > 0) {
		out = append(out, task(s.Name, "systemd/daemon-reload", "systemd/daemon-reload", "daemon-reload"))
	}
	if !absent {
		out = append(out, processes...)
	}
	return out, nil
}
