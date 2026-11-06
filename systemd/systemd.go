// Package systemd resolves and executes explicit process lifecycle branches.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/unit"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

type Backend struct {
	Run      rpm.Runner
	ReadFile func(context.Context, string) ([]byte, error)
	Glob     func(context.Context, string) ([]string, error)
}

func (b Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, "systemctl", args...)
	}
	return rpm.Run(ctx, "systemctl", args...)
}

type Status struct {
	Load, Active, Enabled string
	CanReload             bool
	Fragment              string
	DropIns               []string
}

const statusProperties = "LoadState,ActiveState,UnitFileState,CanReload,FragmentPath,DropInPaths"

func (b Backend) Status(ctx context.Context, unit string) (Status, error) {
	out, err := b.run(ctx, "show", "--property="+statusProperties, "--", unit)
	if err != nil {
		return Status{}, err
	}
	s := Status{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "LoadState":
			s.Load = value
		case "ActiveState":
			s.Active = value
		case "UnitFileState":
			s.Enabled = value
		case "CanReload":
			s.CanReload = value == "yes"
		case "FragmentPath":
			s.Fragment = value
		case "DropInPaths":
			for _, p := range strings.Fields(value) {
				decoded, err := strconv.Unquote("\"" + p + "\"")
				if err != nil {
					return s, errors.New("invalid escaped systemd drop-in path")
				}
				s.DropIns = append(s.DropIns, decoded)
			}
		}
	}
	if s.Load == "" || s.Active == "" {
		return s, errors.New("systemd returned incomplete unit state")
	}
	return s, nil
}

var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_@:.\\-]+\.(service|socket)$`)

func Validate(c *plan.ProcessChange) error {
	if c == nil || !unitPattern.MatchString(c.Unit) || strings.HasPrefix(c.Unit, "-") {
		return errors.New("invalid systemd unit")
	}
	if c.State != "running" && c.State != "stopped" {
		return errors.New("invalid process target state")
	}
	if c.Reaction != "" && c.Reaction != "reload" && c.Reaction != "restart" {
		return errors.New("invalid process reaction")
	}
	if c.Reaction == "reload" && strings.HasSuffix(c.Unit, ".socket") {
		return errors.New("systemd socket units do not support reload")
	}
	if c.Masked != nil && *c.Masked && (c.State != "stopped" || c.Enabled != nil && *c.Enabled) {
		return errors.New("a masked process must target stopped and cannot be enabled")
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return errors.New("invalid process timeout")
	}
	return nil
}
func lifecycle(s Status, c *plan.ProcessChange) (string, error) {
	if s.Load == "not-found" {
		if c.MissingOK && c.State == "stopped" {
			return "", nil
		}
		return "", fmt.Errorf("unit %s does not exist", c.Unit)
	}
	if s.Load == "masked" && c.State == "running" {
		return "", fmt.Errorf("unit %s is masked; set masked: false before starting it", c.Unit)
	}
	if s.Load != "loaded" && !(s.Load == "masked" && c.State == "stopped") {
		return "", fmt.Errorf("unit %s has unexpected load state", c.Unit)
	}
	switch s.Active {
	case "inactive", "failed":
		if c.State == "running" {
			return "start", nil
		}
		return "", nil
	case "active":
		if c.State == "stopped" {
			return "stop", nil
		}
		return c.Reaction, nil
	default:
		return "", fmt.Errorf("unit %s has unexpected active state", c.Unit)
	}
}
func enabled(s Status) bool { return s.Enabled == "enabled" || s.Enabled == "enabled-runtime" }

func Enabled(s Status) bool { return enabled(s) }

// Masked includes persistent and runtime masks. Neither implies stopped.
func Masked(s Status) bool {
	return s.Load == "masked" || s.Enabled == "masked" || s.Enabled == "masked-runtime"
}

// MaskNeedsChange distinguishes a requested persistent mask from a runtime mask.
func MaskNeedsChange(s Status, target bool) bool {
	if target {
		return s.Load != "masked" || s.Enabled != "masked"
	}
	return Masked(s)
}

func (b Backend) NeedsChange(ctx context.Context, c *plan.ProcessChange) (bool, Status, error) {
	s, err := b.Status(ctx, c.Unit)
	if err != nil {
		return false, s, err
	}
	if s.Load == "not-found" {
		return c.State == "running" || c.Masked != nil && MaskNeedsChange(s, *c.Masked), s, nil
	}
	if Masked(s) && c.Enabled != nil && *c.Enabled && (c.Masked == nil || *c.Masked) {
		return false, s, fmt.Errorf("unit %s is masked; set masked: false before enabling it", c.Unit)
	}
	observed := s
	if s.Load == "masked" && c.Masked != nil && !*c.Masked {
		// The explicit unmask task runs before the prepared lifecycle task.
		observed.Load = "loaded"
	}
	op, err := lifecycle(observed, c)
	if err != nil {
		return false, s, err
	}
	return op != "" || (c.Enabled != nil && enabled(s) != *c.Enabled) || c.Masked != nil && MaskNeedsChange(s, *c.Masked), s, nil
}
func (b Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.File != nil || t.Account != nil || t.Artifact != nil || t.RPM != nil || t.Oneshot != nil {
		return nil, errors.New("systemd task contains another operation payload")
	}
	if t.Operation == "daemon-reload" {
		if t.Process != nil {
			return nil, errors.New("daemon-reload task must not contain a process payload")
		}
		return []plan.Action{{Kind: "daemon-reload", Description: []string{"systemctl daemon-reload"}}}, nil
	}
	if t.Operation != "systemd" && t.Operation != "systemd-enable" && t.Operation != "systemd-mask" {
		return nil, fmt.Errorf("unsupported systemd operation %q", t.Operation)
	}
	if err := Validate(t.Process); err != nil {
		return nil, err
	}
	c := t.Process
	if t.Operation == "systemd-mask" {
		if c.Masked == nil || c.Enabled != nil || c.Reaction != "" {
			return nil, errors.New("mask task requires only a masked target")
		}
		commands := []string{"if mask differs: systemctl unmask -- " + c.Unit, "then: systemctl unmask --runtime -- " + c.Unit}
		if *c.Masked {
			commands = []string{"if persistent mask differs: systemctl mask -- " + c.Unit}
		}
		lines := append([]string{"check: systemctl show --property=" + statusProperties + " -- " + c.Unit}, commands...)
		lines = append(lines, "verify mask using the same check; command and wait timeout "+c.Timeout+"; no retries")
		return []plan.Action{{Kind: "systemd-mask", Process: c, Description: lines}}, nil
	}
	if c.Masked != nil {
		return nil, errors.New("masking requires a separate systemd-mask task")
	}
	if t.Operation == "systemd" && c.Enabled != nil {
		return nil, errors.New("enablement requires a separate systemd-enable task")
	}
	if t.Operation == "systemd-enable" {
		if c.Enabled == nil {
			return nil, errors.New("enablement task has no enabled value")
		}
		verb := "disable"
		if *c.Enabled {
			verb = "enable"
		}
		return []plan.Action{{Kind: "systemd-enable", Process: c, Description: []string{"check: systemctl show --property=" + statusProperties + " -- " + c.Unit, "if enablement differs: systemctl " + verb + " -- " + c.Unit, "verify enablement using the same check; command and wait timeout " + c.Timeout}}}, nil
	}
	lines := []string{"check: systemctl show --property=" + statusProperties + " -- " + c.Unit}
	if c.State == "running" {
		lines = append(lines, "if stopped: systemctl start -- "+c.Unit)
		if c.Reaction != "" {
			lines = append(lines, "if running: systemctl "+c.Reaction+" -- "+c.Unit)
		} else {
			lines = append(lines, "if running: no lifecycle action")
		}
	} else {
		lines = append(lines, "if running: systemctl stop -- "+c.Unit, "if stopped: no lifecycle action")
	}
	if c.Enabled != nil {
		verb := "disable"
		if *c.Enabled {
			verb = "enable"
		}
		lines = append(lines, "if enablement differs: systemctl "+verb+" -- "+c.Unit)
	}
	if c.MissingOK {
		lines = append(lines, "if unit is absent: no action")
	}
	lines = append(lines, "wait for "+c.State+"; command and wait timeout "+c.Timeout+"; no retries")
	return []plan.Action{{Kind: "systemd", Process: c, Description: lines}}, nil
}
func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind == "daemon-reload" {
		_, err := b.run(ctx, "daemon-reload")
		return err
	}
	if a.Kind != "systemd" && a.Kind != "systemd-enable" && a.Kind != "systemd-mask" {
		return errors.New("unsupported systemd action")
	}
	c := a.Process
	if err := Validate(c); err != nil {
		return err
	}
	timeout, _ := time.ParseDuration(c.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := b.Status(ctx, c.Unit)
	if err != nil {
		return err
	}
	if a.Kind == "systemd-mask" {
		if c.Masked == nil || c.Enabled != nil || c.Reaction != "" {
			return errors.New("mask task requires only a masked target")
		}
		if s.Load != "loaded" && s.Load != "not-found" && s.Load != "masked" {
			return fmt.Errorf("unit %s has unexpected load state", c.Unit)
		}
		if !MaskNeedsChange(s, *c.Masked) {
			return nil
		}
		if *c.Masked {
			if _, err = b.run(ctx, "mask", "--", c.Unit); err != nil {
				return err
			}
		} else {
			if _, err = b.run(ctx, "unmask", "--", c.Unit); err != nil {
				return err
			}
			if _, err = b.run(ctx, "unmask", "--runtime", "--", c.Unit); err != nil {
				return err
			}
		}
		for {
			state, err := b.Status(ctx, c.Unit)
			if err != nil {
				return err
			}
			if !MaskNeedsChange(state, *c.Masked) && (state.Load == "loaded" || state.Load == "not-found" || state.Load == "masked") {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("unit %s mask did not converge: %w", c.Unit, ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	if a.Kind == "systemd-enable" {
		if c.Enabled == nil {
			return errors.New("enablement task has no enabled value")
		}
		if s.Load == "not-found" && c.MissingOK {
			return nil
		}
		if s.Load != "loaded" && !(s.Load == "masked" && !*c.Enabled) {
			return fmt.Errorf("unit %s has unexpected load state", c.Unit)
		}
		if enabled(s) == *c.Enabled {
			return nil
		}
		verb := "disable"
		if *c.Enabled {
			verb = "enable"
		}
		if _, err = b.run(ctx, verb, "--", c.Unit); err != nil {
			return err
		}
		for {
			state, err := b.Status(ctx, c.Unit)
			if err != nil {
				return err
			}
			if enabled(state) == *c.Enabled {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("unit %s enablement did not converge: %w", c.Unit, ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	op, err := lifecycle(s, c)
	if err != nil {
		return err
	}
	if s.Load == "not-found" && c.MissingOK {
		return nil
	}
	if op == "reload" && !s.CanReload {
		return fmt.Errorf("unit %s does not support reload", c.Unit)
	}
	if op != "" {
		if _, err = b.run(ctx, op, "--", c.Unit); err != nil {
			return err
		}
	}
	if c.Enabled != nil && enabled(s) != *c.Enabled {
		verb := "disable"
		if *c.Enabled {
			verb = "enable"
		}
		if _, err = b.run(ctx, verb, "--", c.Unit); err != nil {
			return err
		}
	}
	// Repeated status observations are waiting, never retries of the lifecycle command.
	for {
		state, err := b.Status(ctx, c.Unit)
		if err != nil {
			return err
		}
		if c.State == "running" && state.Active == "active" {
			return nil
		}
		if c.State == "stopped" && (state.Active == "inactive" || state.Active == "failed") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("unit %s did not become %s: %w", c.Unit, c.State, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// ReloadSetting applies ExecReload resets and additions from a rendered unit/drop-in.
// The caller supplies files in systemd precedence order.
func ReloadSetting(content string, current bool) (bool, error) {
	options, err := unit.DeserializeOptions(strings.NewReader(content))
	if err != nil {
		return false, errors.New("invalid systemd unit configuration")
	}
	for _, option := range options {
		if option.Section == "Service" && option.Name == "ExecReload" {
			current = strings.TrimSpace(option.Value) != ""
		}
	}
	return current, nil
}

// ReloadSupported evaluates the final fragment and effective drop-ins. Matching
// basenames replace existing drop-ins; all drop-ins then apply lexicographically.
func ReloadSupported(unitName string, status Status, files []*plan.FileChange) (bool, error) {
	return (Backend{}).ReloadSupported(context.Background(), unitName, status, files)
}

func (b Backend) ReloadSupported(ctx context.Context, unitName string, status Status, files []*plan.FileChange) (supported bool, err error) {
	// Service-only settings cannot confer reload capability on socket units.
	if !strings.HasSuffix(unitName, ".service") {
		return false, nil
	}
	defer func() {
		if err != nil {
			for _, f := range files {
				if f != nil && f.Sensitive {
					err = errors.New("sensitive unit capability check failed")
					break
				}
			}
		}
	}()
	readFile := func(path string) ([]byte, error) {
		if b.ReadFile != nil {
			return b.ReadFile(ctx, path)
		}
		return os.ReadFile(path)
	}
	glob := func(pattern string) ([]string, error) {
		if b.Glob != nil {
			return b.Glob(ctx, pattern)
		}
		return filepath.Glob(pattern)
	}
	type capabilities struct {
		execReload   bool
		notifyReload bool
	}
	state := capabilities{execReload: status.CanReload}
	apply := func(content string) error {
		options, err := unit.DeserializeOptions(strings.NewReader(content))
		if err != nil {
			return errors.New("invalid systemd unit configuration")
		}
		for _, option := range options {
			if option.Section != "Service" {
				continue
			}
			switch option.Name {
			case "ExecReload":
				state.execReload = strings.TrimSpace(option.Value) != ""
			case "Type":
				state.notifyReload = strings.TrimSpace(option.Value) == "notify-reload"
			}
		}
		return nil
	}
	var full *plan.FileChange
	dropins := map[string]string{}
	for _, path := range status.DropIns {
		dropins[filepath.Base(path)] = path
	}
	// This also discovers drop-ins for a unit not installed at diff time.
	for _, directory := range []string{"/usr/lib/systemd/system/", "/run/systemd/system/", "/etc/systemd/system/"} {
		matches, err := glob(directory + unitName + ".d/*.conf")
		if err != nil {
			return false, err
		}
		for _, path := range matches {
			dropins[filepath.Base(path)] = path
		}
	}
	rendered := map[string]*plan.FileChange{}
	for _, f := range files {
		if filepath.Base(f.Path) == unitName {
			full = f
		} else {
			rendered[filepath.Base(f.Path)] = f
			dropins[filepath.Base(f.Path)] = f.Path
		}
	}
	if full != nil {
		state = capabilities{}
		if err := apply(full.Content); err != nil {
			return false, err
		}
	} else if status.Fragment != "" {
		content, err := readFile(status.Fragment)
		if err != nil {
			return false, err
		}
		state = capabilities{}
		if err = apply(string(content)); err != nil {
			return false, err
		}
	}
	names := make([]string, 0, len(dropins))
	for name := range dropins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if f, ok := rendered[name]; ok {
			if err := apply(f.Content); err != nil {
				return false, err
			}
			continue
		}
		content, err := readFile(dropins[name])
		if err != nil {
			return false, err
		}
		if err = apply(string(content)); err != nil {
			return false, err
		}
	}
	return state.execReload || state.notifyReload, nil
}
