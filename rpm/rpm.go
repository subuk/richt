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
	"github.com/subuk/richt/transport"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	client := transport.Client{Connection: inventory.Transport{Type: "local"}}
	out, err := client.Run(ctx, name, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Preserve exit status without retaining stderr, command arguments, or
		// executable paths, any of which may contain secrets from target inputs.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("local command failed: %w", &exec.ExitError{ProcessState: exit.ProcessState})
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("local command failed: %w", exec.ErrNotFound)
		}
		return nil, errors.New("local command failed")
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
		return errors.New("supported package manager is DNF 4 (including YUM 4 compatibility)")
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
	if err := inventory.ValidateRPMVersion(c.Version); err != nil {
		return err
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
		if c.State == "present" && installedMatches(c.Version, version) {
			return false, nil
		}
	}
	if c.State == "absent" {
		return installed, nil
	}
	return true, nil
}

func installedMatches(requested, installed string) bool {
	if requested == "" || requested == installed {
		return true
	}
	if strings.Contains(requested, ":") {
		return false
	}
	_, upstream, ok := strings.Cut(installed, ":")
	if !ok {
		return false
	}
	upstream, _, ok = strings.Cut(upstream, "-")
	return ok && upstream == requested
}

// ErrNotInstalled permits deterministic package database fakes without manufacturing an exit status.
var ErrNotInstalled = errors.New("RPM is not installed")

func upstreamRequest(c *plan.RPMChange) bool {
	return c.State == "present" && c.Version != "" && !strings.Contains(c.Version, ":")
}

// Arguments returns DNF transaction arguments for the prepared package request.
func Arguments(c *plan.RPMChange) []string {
	if c.State == "absent" {
		return []string{"-y", "remove", "--", c.Package}
	}
	if upstreamRequest(c) {
		return []string{"-y", "install", "--", c.Package + "-" + c.Version}
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
	description := []string{t.RPM.Manager + " " + strings.Join(Arguments(t.RPM), " ")}
	if upstreamRequest(t.RPM) {
		description = append(description, "Upstream version "+t.RPM.Version+": at apply, DNF resolves epoch, release, and architecture")
	}
	description = append(description, "RPM dependencies and scripts are resolved by the target at execution")
	return []plan.Action{{Kind: "rpm", RPM: t.RPM, Description: description}}, nil
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
