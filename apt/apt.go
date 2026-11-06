// Package apt implements native Debian package requests through APT and dpkg.
package apt

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

//go:embed target.py
var targetHelper string

type Runner func(context.Context, string, ...string) ([]byte, error)
type Backend struct{ Run Runner }

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	client := transport.Client{Connection: inventory.Transport{Type: "local"}}
	return client.Run(ctx, name, args...)
}
func (b Backend) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, name, args...)
	}
	return Run(ctx, name, args...)
}

var aptVersion = regexp.MustCompile(`^apt ([0-9]+)\.([0-9]+)(?:\.|\s)`)
var architecture = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// CheckManager checks local capabilities without reading repositories or cache stamps.
func (b Backend) CheckManager(ctx context.Context, manager string) error {
	if manager != "apt" {
		return errors.New("DEB manager must be apt")
	}
	out, err := b.run(ctx, "apt-get", "--version")
	if err != nil {
		return safeError(ctx, "APT capability check failed", err)
	}
	match := aptVersion.FindStringSubmatch(string(out))
	if len(match) != 3 {
		return errors.New("APT 2.2 or newer is required")
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major < 2 || (major == 2 && minor < 2) {
		return errors.New("APT 2.2 or newer is required")
	}
	if _, err := b.run(ctx, "dpkg-query", "--version"); err != nil {
		return safeError(ctx, "dpkg-query capability check failed", err)
	}
	_, err = b.call(ctx, map[string]any{"op": "check"}, 30*time.Second)
	return err
}

func Validate(c *plan.DEBChange) error { return c.Validate() }
func Prepare(v inventory.Binary, state string) (*plan.DEBChange, error) {
	if v.Manager == "" {
		v.Manager = "apt"
	}
	if state == "" {
		state = "present"
	}
	c := &plan.DEBChange{Manager: v.Manager, Package: v.Package, Version: v.Version, State: state, CacheMaxAge: "48h", LockTimeout: "60s", Timeout: "15m", ConffilePolicy: "keep"}
	return c, Validate(c)
}

const queryFormat = "${Package}\\t${Version}\\t${db:Status-Abbrev}\\t${Architecture}\\n"

// Observe reads only dpkg's installed database. It never reads APT's repository
// indexes, consults the freshness stamp, takes a lock or refreshes metadata.
func (b Backend) Observe(ctx context.Context, c *plan.DEBChange) (bool, error) {
	if err := Validate(c); err != nil {
		return false, err
	}
	out, err := b.run(ctx, "dpkg", "--print-architecture")
	if err != nil {
		return false, safeError(ctx, "dpkg architecture query failed", err)
	}
	native := strings.TrimSpace(string(out))
	if !architecture.MatchString(native) {
		return false, errors.New("dpkg returned invalid native architecture")
	}
	out, err = b.run(ctx, "dpkg-query", "--show", "--showformat="+queryFormat)
	if err != nil {
		return false, safeError(ctx, "dpkg installed database query failed", err)
	}
	if len(out) > 32<<20 {
		return false, errors.New("dpkg installed database query exceeded its output limit")
	}
	installed, held, version, err := installedState(string(out), c.Package, native)
	if err != nil {
		return false, err
	}
	changed := installed
	if c.State == "present" {
		changed = !installed
		if installed && c.Version != "" && c.Version != version {
			_, err := b.run(ctx, "dpkg", "--compare-versions", c.Version, "eq", version)
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) && exit.ExitCode() == 1 {
					changed = true
				} else {
					return false, safeError(ctx, "dpkg version comparison failed", err)
				}
			}
		}
	}
	if changed && held {
		return false, errors.New("DEB package is held; changing held packages is unsupported")
	}
	return changed, nil
}

func installedState(out, name, native string) (installed, held bool, version string, err error) {
	found := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return false, false, "", errors.New("dpkg returned invalid installed package metadata")
		}
		if fields[0] != name || (fields[3] != native && fields[3] != "all") {
			continue
		}
		status := fields[2]
		if found || len(status) != 3 || !strings.ContainsRune("uihrp", rune(status[0])) || status[2] != ' ' {
			return false, false, "", errors.New("DEB package database state is broken or unsupported")
		}
		found = true
		held = status[0] == 'h'
		switch status[1] {
		case 'i':
			if fields[1] == "" || inventory.ValidateDEBVersion(fields[1]) != nil {
				return false, false, "", errors.New("dpkg returned invalid installed version")
			}
			installed = true
			version = fields[1]
		case 'n', 'c': // Missing or removed with conffiles retained.
		default:
			return false, false, "", errors.New("DEB package database state is broken or unsupported")
		}
	}
	return
}

func (b Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "deb" || t.RPM != nil || t.File != nil || t.Artifact != nil || t.Process != nil || t.Oneshot != nil || t.Path != nil || t.Storage != nil || t.Mount != nil || t.Account != nil {
		return nil, errors.New("invalid DEB task")
	}
	if err := Validate(t.DEB); err != nil {
		return nil, err
	}
	c := t.DEB
	request := c.Package
	if c.State == "present" && c.Version != "" {
		request += "=" + c.Version
	}
	description := []string{"apt-get remove " + request + "; retain conffiles; never purge or autoremove"}
	if c.State == "present" {
		description = []string{"apt-get install " + request + "; preserve an installed version when version is omitted", "At apply, refresh APT indexes only when Richt's successful refresh is older than " + c.CacheMaxAge + ", configuration or keys change, or indexes change or disappear"}
	}
	description = append(description, "Target APT resolves repositories, dependencies and maintainer scripts at apply; exact requests may upgrade or downgrade", "Noninteractive; keep existing conffiles; preserve signature checks and held/essential protections; no automatic retries or repair", "Serialize APT tasks on the target; lock wait "+c.LockTimeout+"; total operation timeout "+c.Timeout+"; verify the installed result")
	return []plan.Action{{Kind: "deb", DEB: c, Description: description}}, nil
}

func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind != "deb" || a.RPM != nil || a.File != nil || a.Artifact != nil || a.Process != nil || a.Oneshot != nil || a.Path != nil || a.Storage != nil || a.Mount != nil || a.Account != nil {
		return errors.New("unsupported DEB action")
	}
	if err := Validate(a.DEB); err != nil {
		return err
	}
	total, _ := time.ParseDuration(a.DEB.Timeout)
	lock, _ := time.ParseDuration(a.DEB.LockTimeout)
	age, _ := time.ParseDuration(a.DEB.CacheMaxAge)
	_, err := b.call(ctx, map[string]any{"op": "apply", "change": a.DEB, "seconds": total.Seconds(), "lock_seconds": lock.Seconds(), "cache_seconds": age.Seconds()}, total+5*time.Second)
	return err
}

var helperErrors = map[string]string{
	"unsupported": "APT requires Linux, Python 3.8 or newer, APT 2.2 or newer and dpkg",
	"layout":      "APT configuration uses an unsupported filesystem layout",
	"metadata":    "APT configuration or repository metadata is invalid or exceeds its safety limits",
	"permissions": "APT requires root and protected target metadata paths",
	"lock":        "APT target or native package lock wait timed out",
	"timeout":     "APT operation timed out; inspect the target before manual continuation",
	"database":    "DEB package database state is broken or unsupported; automatic repair is disabled",
	"held":        "DEB package is held; changing held packages is unsupported",
	"update":      "APT index refresh failed; package transaction was not attempted",
	"unavailable": "APT has no matching native package version",
	"transaction": "APT package transaction failed; automatic retries and repair are disabled",
	"verify":      "APT transaction completed but its installed postcondition was not met",
	"stamp":       "APT freshness metadata could not be safely persisted",
	"request":     "invalid APT target request",
}

func safeError(ctx context.Context, message string, failures ...error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Preserve only the transport's fixed diagnostic categories. Never return or
	// wrap arbitrary runner errors, stdout or stderr.
	for _, failure := range failures {
		for _, fixed := range []string{
			"SSH host key has changed; verify the target's known_hosts entry",
			"SSH host key is unknown; verify it through your normal SSH setup",
			"SSH host key verification failed; verify the target's known_hosts entry",
			"SSH authentication failed; noninteractive authentication is required",
			"SSH connection was refused", "SSH target is unreachable", "SSH connection timed out", "SSH hostname could not be resolved",
			"sudo requires a password; noninteractive sudo authorization is required",
			"sudo authorization was denied", "sudo requires a terminal; noninteractive sudo authorization is required",
		} {
			if failure != nil && strings.HasPrefix(failure.Error(), fixed+": ") {
				return errors.New(fixed)
			}
		}
	}
	return errors.New(message)
}
func (b Backend) call(ctx context.Context, input map[string]any, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, _ := json.Marshal(input)
	out, err := b.run(ctx, "python3", "-I", "-c", targetHelper, string(raw))
	if err != nil {
		return nil, safeError(ctx, "APT target helper failed; Linux and Python 3.8 or newer are required", err)
	}
	var result map[string]json.RawMessage
	if len(out) > 4096 || json.Unmarshal(out, &result) != nil || result == nil || len(result) > 1 {
		return nil, errors.New("APT target helper returned invalid metadata")
	}
	if len(result) == 1 {
		var category string
		raw, exists := result["error"]
		if !exists || json.Unmarshal(raw, &category) != nil || category == "" {
			return nil, errors.New("APT target helper returned invalid metadata")
		}
		if message, ok := helperErrors[category]; ok {
			return nil, fmt.Errorf("APT: %s", message)
		}
		return nil, errors.New("APT target helper failed")
	}
	return out, nil
}
