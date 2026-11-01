// Package rpm implements DNF 4 and its YUM compatibility command.
package rpm

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Backend struct{ Run Runner }

// CheckManager deliberately avoids repository metadata and transactions.
func (b Backend) CheckManager(ctx context.Context, manager string) error {
	out, err := b.run(ctx, manager, "--version")
	if err != nil {
		return err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !strings.HasPrefix(line, "4.") {
		return fmt.Errorf("%s: supported package manager is DNF 4 (including YUM 4 compatibility); got %q", manager, line)
	}
	return nil
}

func (b Backend) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, name, args...)
	}
	return Run(ctx, name, args...)
}

var packagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+_.-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9+_.~^]+-[A-Za-z0-9+_.~^]+\.[A-Za-z0-9_]+$`)

func Validate(c *plan.RPMChange) error {
	if c == nil {
		return errors.New("missing RPM request")
	}
	if c.Manager != "dnf" && c.Manager != "yum" {
		return errors.New("RPM manager must be dnf or yum (DNF 4)")
	}
	if !packagePattern.MatchString(c.Package) {
		return errors.New("invalid RPM package name")
	}
	if c.Version != "" && !versionPattern.MatchString(c.Version) {
		return errors.New("RPM version must be epoch:version-release.arch, for example 0:1.0-1.noarch")
	}
	if c.State != "present" && c.State != "absent" {
		return errors.New("invalid RPM state")
	}
	return nil
}
func Prepare(v inventory.Binary, state string) (*plan.RPMChange, error) {
	if v.Manager == "" {
		v.Manager = "dnf"
	}
	if state == "" {
		state = "present"
	}
	c := &plan.RPMChange{Manager: v.Manager, Package: v.Package, Version: v.Version, State: state}
	return c, Validate(c)
}

// Observe reads only the installed RPM database: a future repository need not exist yet.
func (b Backend) Observe(ctx context.Context, c *plan.RPMChange) (bool, error) {
	if err := Validate(c); err != nil {
		return false, err
	}
	// Query-all has an empty successful result for absence. Query-by-name uses
	// the same exit status for absence and RPM database failures.
	out, err := b.run(ctx, "rpm", "-qa", "--qf", "%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}\n")
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return c.State == "present", nil
		}
		return false, err
	}
	installed := false
	for _, line := range strings.Split(string(out), "\n") {
		name, version, ok := strings.Cut(line, "\t")
		if !ok || name != c.Package {
			continue
		}
		installed = true
		if c.State == "present" && (c.Version == "" || version == c.Version) {
			return false, nil
		}
	}
	if c.State == "absent" {
		return installed, nil
	}
	return true, nil
}

// ErrNotInstalled permits deterministic package database fakes without manufacturing an exit status.
var ErrNotInstalled = errors.New("RPM is not installed")

func Arguments(c *plan.RPMChange) []string {
	if c.State == "absent" {
		return []string{"-y", "remove", "--", c.Package}
	}
	if c.Version != "" {
		return []string{"-y", "install-nevra", "--", c.Package + "-" + c.Version}
	}
	return []string{"-y", "install-n", "--", c.Package}
}
func (b Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.File != nil || t.Process != nil {
		return nil, errors.New("RPM task contains another operation payload")
	}
	if t.Operation != "rpm" {
		return nil, fmt.Errorf("unsupported RPM operation %q", t.Operation)
	}
	if err := Validate(t.RPM); err != nil {
		return nil, err
	}
	return []plan.Action{{Kind: "rpm", RPM: t.RPM, Description: []string{t.RPM.Manager + " " + strings.Join(Arguments(t.RPM), " "), "RPM dependencies and scripts are resolved by the target at execution"}}}, nil
}
func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind != "rpm" {
		return errors.New("unsupported RPM action")
	}
	if err := Validate(a.RPM); err != nil {
		return err
	}
	_, err := b.run(ctx, a.RPM.Manager, Arguments(a.RPM)...)
	return err
}
