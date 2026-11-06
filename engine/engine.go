// Package engine prepares complete services and dispatches their semantic operations.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/subuk/richt/accounts"
	"github.com/subuk/richt/artifact"
	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/mount"
	"github.com/subuk/richt/oneshot"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/secrets"
	"github.com/subuk/richt/storage"
	"github.com/subuk/richt/systemd"
	"github.com/subuk/richt/transport"
)

type Local struct {
	Accounts  accounts.Backend
	RPM       rpm.Backend
	Artifacts artifact.Backend
	Systemd   systemd.Backend
	Oneshot   oneshot.Backend
	Files     *config.Target
	Storage   storage.Backend
	Mount     mount.Backend
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
		e.Oneshot = oneshot.Backend{Run: client.RunDiscard}
	}
	if e.Connect != nil {
		e.Oneshot = oneshot.Backend{Run: func(ctx context.Context, name string, args ...string) error {
			if target.Run == nil {
				return errors.New("missing target command runner")
			}
			_, err := target.Run(ctx, name, args...)
			return err
		}}
	}
	e.Artifacts = artifact.Backend{RunInput: target.RunInput}
	e.Files = &target
	e.Storage = storage.Backend{Run: target.Run}
	e.Accounts = accounts.Backend{Runner: target.Run}
	e.Mount = mount.Backend{Run: target.Run, RunInput: target.RunInput}
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
	resources := 0
	for _, present := range []bool{t.Account != nil, t.Artifact != nil, t.File != nil, t.Path != nil, t.Storage != nil, t.Mount != nil, t.RPM != nil, t.Process != nil, t.Oneshot != nil} {
		if present {
			resources++
		}
	}
	if resources > 1 {
		return nil, errors.New("task contains mixed resource payloads")
	}
	if t.Account != nil && t.Operation != "account" || t.Artifact != nil && t.Operation != "artifact" || t.Path != nil && t.Operation != "path" || t.Storage != nil && t.Operation != "storage" || t.Mount != nil && t.Operation != "mount" {
		return nil, errors.New("resource payload does not match operation")
	}
	if t.Operation == "daemon-reload" && (t.Account != nil || t.Artifact != nil || t.File != nil || t.RPM != nil || t.Process != nil || t.Oneshot != nil || t.Path != nil || t.Storage != nil || t.Mount != nil) {
		return nil, errors.New("daemon-reload task must not contain a resource payload")
	}
	if t.Oneshot != nil && t.Operation != "oneshot" {
		return nil, errors.New("unexpected oneshot payload")
	}
	switch t.Operation {
	case "account":
		return e.Accounts.Resolve(t)
	case "artifact":
		return e.Artifacts.Resolve(t)
	case "path":
		return (config.Local{}).ResolvePath(t)
	case "storage":
		return e.Storage.Resolve(t)
	case "mount":
		return e.Mount.Resolve(t)
	case "file":
		return (config.Local{}).Resolve(t)
	case "oneshot":
		return e.Oneshot.Resolve(t)
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
	case "account":
		return e.Accounts.Execute(ctx, a)
	case "ensure-artifact", "remove-artifact":
		return e.Artifacts.Execute(ctx, a)
	case "ensure-path", "remove-path":
		if e.Files != nil {
			return e.Files.ExecutePath(ctx, a)
		}
		return (config.Local{}).ExecutePath(ctx, a)
	case "storage":
		return e.Storage.Execute(ctx, a)
	case "mount":
		return e.Mount.Execute(ctx, a)
	case "oneshot":
		return e.Oneshot.Execute(ctx, a)
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
	definition                  *inventory.Config
	changed                     bool
}
type preparedService struct {
	service  inventory.ResolvedService
	files    []preparedFile
	binaries []preparedBinary
}

type preparedBinary struct {
	index    int
	name     string
	change   *plan.RPMChange
	artifact *plan.ArtifactChange
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
				if c.Type != "" && c.Type != "template" {
					ps.files = append(ps.files, preparedFile{id: id, component: component, ref: ref, definition: &c})
					return nil
				}
				f, err := config.Prepare(c, s.State, h.Vars, lookup)
				if err != nil {
					return fmt.Errorf("host %s service %s %s: %w", h.Name, s.Name, component, err)
				}
				if process == "" {
					for key, value := range s.Definition.Processes {
						if value.Type == "oneshot" {
							continue
						}
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
				b := preparedBinary{index: index, name: binary.Name}
				var err error
				switch binary.Type {
				case "download", "archive":
					b.artifact, err = artifact.Prepare(binary, s.State)
				default:
					b.change, err = rpm.Prepare(binary, s.State)
				}
				if err != nil {
					return nil, fmt.Errorf("service %s binary %s: %w", s.Name, label(binary.Name, index), err)
				}
				ps.binaries = append(ps.binaries, b)
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
				if p.Type == "oneshot" {
					continue
				}
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
			if s.State == "absent" {
				// Owned process definitions are written after configs when present;
				// remove them before reversing the config list and its accounts.
				ordered := make([]preparedFile, 0, len(ps.files))
				for _, f := range ps.files {
					if strings.HasPrefix(f.id, "process/") {
						ordered = append(ordered, f)
					}
				}
				for _, f := range ps.files {
					if !strings.HasPrefix(f.id, "process/") {
						ordered = append(ordered, f)
					}
				}
				ps.files = ordered
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
		checkedArtifacts := false
		checkedAccounts := false
		for _, service := range prepared[hi] {
			for _, f := range service.files {
				if c := f.definition; c != nil && (c.Type == "user" || c.Type == "group") && !checkedAccounts {
					if err := bound[hi].Accounts.Check(ctx); err != nil {
						return nil, fmt.Errorf("host %s: %w", h.Name, err)
					}
					checkedAccounts = true
				}
			}
			for _, prepared := range service.binaries {
				if prepared.artifact != nil {
					if !checkedArtifacts {
						if err := bound[hi].Artifacts.Check(ctx); err != nil {
							return nil, fmt.Errorf("host %s: %w", h.Name, err)
						}
						checkedArtifacts = true
					}
					continue
				}
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
		sequence := newConfigSequence()
		ownership := prepared[hi]
		if len(h.OwnershipServices) > 0 {
			ownership = ownershipPaths(h.OwnershipServices)
		}
		if err := bound[hi].checkArtifactPaths(ctx, ownership); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Name, err)
		}
		if err := checkAccountOrder(prepared[hi]); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Name, err)
		}
		if err := bound[hi].checkConfigPaths(ctx, prepared[hi]); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Name, err)
		}
		for _, ps := range prepared[hi] {
			tasks, err := bound[hi].observe(ctx, ps, sequence)
			if err != nil {
				return nil, fmt.Errorf("host %s service %s: %w", h.Name, ps.service.Name, err)
			}
			ph.Tasks = append(ph.Tasks, tasks...)
		}
		p.Hosts = append(p.Hosts, ph)
	}
	return p, p.Validate()
}
func (e Local) observe(ctx context.Context, ps preparedService, sequence *configSequence) ([]plan.Task, error) {
	s := ps.service
	absent := s.State == "absent"
	changed := map[string]bool{}
	unitChanged := map[string]bool{}
	daemonReload := false
	var binaries, files, processes, unmasks []plan.Task
	packageChanged := false
	for _, position := range positions(len(ps.binaries), absent) {
		binary := ps.binaries[position]
		var yes bool
		var err error
		op := "rpm"
		if binary.artifact != nil {
			op = "artifact"
			yes, err = e.Artifacts.Observe(ctx, binary.artifact)
		} else {
			yes, err = e.RPM.Observe(ctx, binary.change)
		}
		if err != nil {
			return nil, err
		}
		if yes {
			if binary.name != "" {
				changed["binary."+binary.name] = true
			}
			t := task(s.Name, "binary/"+strconv.Itoa(binary.index), "binary/"+label(binary.name, binary.index), op)
			t.RPM, t.Artifact = binary.change, binary.artifact
			packageChanged = packageChanged || binary.change != nil
			binaries = append(binaries, t)
		}
	}
	order := positions(len(s.Definition.Processes), absent)
	processLabels := map[string]string{}
	for index, process := range s.Definition.Processes {
		processLabels[strconv.Itoa(index)] = label(process.Name, index)
	}
	hasSystemd := false
	statuses := map[string]systemd.Status{}
	processChanges := map[string]*plan.ProcessChange{}
	for _, index := range order {
		name := strconv.Itoa(index)
		v := s.Definition.Processes[index]
		if v.Type == "oneshot" {
			continue
		}
		hasSystemd = true
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
				if f.file != nil && f.process == name && f.file.Sensitive {
					err = errors.New("sensitive unit observation failed")
				}
			}
			return nil, err
		}
		statuses[name], processChanges[name] = status, c
	}
	for i := range ps.files {
		f := &ps.files[i]
		if f.definition != nil {
			t, yes, err := e.observeTyped(ctx, s, *f, sequence)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.component, err)
			}
			f.changed = yes
			if yes {
				files = append(files, t)
				if f.ref != "" {
					changed[f.ref] = true
				}
			}
			continue
		}
		ownership, err := sequence.ownership(f.file.Owner, f.file.Group, f.file.State)
		if err != nil {
			if f.file.Sensitive {
				err = errors.New("sensitive file observation failed")
			}
			return nil, fmt.Errorf("%s: %w", f.component, err)
		}
		hidden, err := e.hiddenConfigPath(ctx, sequence, f.file.Path, f.file.State)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.component, err)
		}
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
		if (unmaskFullUnit || hidden) && e.Files != nil {
			yes, err = e.Files.ObserveMissing(ctx, f.file, ownership)
		} else if unmaskFullUnit || hidden {
			yes, err = config.ObserveMissing(f.file, ownership)
		} else if e.Files != nil {
			yes, err = e.Files.Observe(ctx, f.file, ownership)
		} else {
			yes, err = config.Observe(f.file, ownership)
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
		if v.Type == "oneshot" {
			if absent {
				continue
			}
			var triggers []string
			for _, ref := range v.RunOn {
				if changed[ref] {
					triggers = append(triggers, ref)
				}
			}
			if len(triggers) != 0 {
				timeout := v.Timeout
				if timeout == "" {
					timeout = "5s"
				}
				t := task(s.Name, "process/"+name, "process/"+label(v.Name, index), "oneshot")
				t.Oneshot = &plan.OneshotChange{Command: append([]string(nil), v.Command...), Timeout: timeout, Triggers: triggers}
				if _, err := e.Oneshot.Resolve(t); err != nil {
					return nil, err
				}
				processes = append(processes, t)
			}
			continue
		}
		var unitFiles []*plan.FileChange
		hasFullUnit := false
		for _, f := range ps.files {
			if f.file != nil && f.process == name {
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
		if !absent && status.Load == "not-found" && !hasFullUnit && !packageChanged && !(c.Masked != nil && (*c.Masked || c.State == "stopped")) {
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
				// Removing a full-unit mask must precede its prepared file write.
				unmasks = append(unmasks, t)
			}
		}
		c.Masked = nil
		var enableTask *plan.Task
		if c.Enabled != nil && (unmaskRequested || (status.Load != "not-found" && systemd.Enabled(status) != *c.Enabled) || (!absent && status.Load == "not-found" && (*c.Enabled || hasFullUnit || packageChanged))) {
			ec := *c
			if unmaskRequested && !*c.Enabled && !hasFullUnit && !packageChanged {
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
	if daemonReload || len(unitChanged) > 0 || (packageChanged && hasSystemd) {
		out = append(out, task(s.Name, "systemd/daemon-reload", "systemd/daemon-reload", "daemon-reload"))
	}
	if !absent {
		out = append(out, processes...)
	}
	return out, nil
}
