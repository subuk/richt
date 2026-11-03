// Package mount manages active XFS mounts and frozen, persistent fstab entries.
package mount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/storage"
	"github.com/subuk/richt/transport"
)

type Backend struct {
	Run      rpm.Runner
	RunInput func(context.Context, []byte, string, ...string) ([]byte, error)
}

func (b Backend) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, name, args...)
	}
	return rpm.Run(ctx, name, args...)
}
func (b Backend) input(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	if b.RunInput != nil {
		return b.RunInput(ctx, input, name, args...)
	}
	if b.Run != nil {
		return nil, errors.New("mount backend requires stdin command capability")
	}
	client := transport.Client{Connection: inventory.Transport{Type: "local"}}
	return client.RunInput(ctx, input, name, args...)
}

// CanonicalPath resolves target aliases without creating or opening contents.
func (b Backend) CanonicalPath(ctx context.Context, path string) (string, error) {
	out, err := b.run(ctx, "readlink", "-m", "--", path)
	if err != nil {
		return "", err
	}
	canonical := strings.TrimSuffix(string(out), "\n")
	if !strings.HasPrefix(canonical, "/") || strings.ContainsAny(canonical, "\r\n\x00") {
		return "", errors.New("invalid canonical mount target")
	}
	return canonical, nil
}

// ProjectPath canonicalizes aliases only up to an earlier pending mount root.
// Remaining components belong to the intended filesystem, not its hidden directory.
func (b Backend) ProjectPath(ctx context.Context, path string, previous []*plan.MountChange) (string, error) {
	clean := filepath.Clean(path)
	pending := map[string]bool{}
	for _, p := range previous {
		if p.State == "present" && p.TargetPending {
			pending[p.Target] = true
			if clean == p.Target || strings.HasPrefix(clean, p.Target+"/") {
				return clean, nil
			}
		}
	}
	if len(pending) == 0 {
		return b.CanonicalPath(ctx, path)
	}
	components := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	prefix := ""
	var canonical string
	for i, part := range components {
		prefix += "/" + part
		var err error
		canonical, err = b.CanonicalPath(ctx, prefix)
		if err != nil {
			return "", err
		}
		if pending[canonical] {
			return filepath.Join(canonical, filepath.Join(components[i+1:]...)), nil
		}
	}
	return canonical, nil
}

func (b Backend) preparationPath(ctx context.Context, path string, previous []*plan.MountChange) (string, error) {
	projected, err := b.ProjectPath(ctx, path, previous)
	if err != nil {
		return "", err
	}
	for _, p := range previous {
		if p.State == "present" && p.TargetPending && strings.HasPrefix(projected, p.Target+"/") && !p.Identity.Fresh {
			return "", errors.New("cannot prepare a path inside an unmounted existing filesystem; mount it first")
		}
	}
	return projected, nil
}

type activeMount struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Filesystem string `json:"fstype"`
	Options    string `json:"options"`
	MajorMinor string `json:"maj:min"`
	Root       string `json:"fsroot"`
}

func (b Backend) active(ctx context.Context, target string) (*activeMount, error) {
	out, err := b.run(ctx, "findmnt", "--json", "--list", "--output", "SOURCE,TARGET,FSTYPE,OPTIONS,MAJ:MIN,FSROOT")
	if err != nil {
		return nil, err
	}
	var document struct {
		Filesystems []activeMount `json:"filesystems"`
	}
	if err = json.Unmarshal(out, &document); err != nil {
		return nil, errors.New("invalid findmnt observation")
	}
	var found *activeMount
	for i := range document.Filesystems {
		current := &document.Filesystems[i]
		if current.Target == target {
			if found != nil {
				return nil, errors.New("stacked mounts are unsupported")
			}
			found = current
		}
	}
	return found, nil
}
func checkActive(a *activeMount, c *plan.MountChange) error {
	if a == nil {
		return nil
	}
	if a.Root != "" && a.Root != "/" {
		return errors.New("mount target contains a bind or subtree mount")
	}
	if a.Filesystem != c.Filesystem || a.MajorMinor != c.Identity.MajorMinor || c.Identity.MajorMinor == "" {
		return errors.New("mount target has an incompatible active source or filesystem")
	}
	return nil
}
func optionsMatch(observed string, desired []string) bool {
	have := map[string]bool{}
	for _, v := range strings.Split(observed, ",") {
		have[v] = true
	}
	// mount(8) starts from defaults even when the list does not spell defaults.
	want := map[string]bool{"rw": true, "suid": true, "dev": true, "exec": true, "async": true}
	opposites := map[string]string{"rw": "ro", "ro": "rw", "suid": "nosuid", "nosuid": "suid", "dev": "nodev", "nodev": "dev", "exec": "noexec", "noexec": "exec", "async": "sync", "sync": "async", "atime": "noatime", "noatime": "atime", "diratime": "nodiratime", "nodiratime": "diratime", "relatime": "norelatime", "norelatime": "relatime"}
	for _, v := range desired {
		if v == "defaults" {
			continue
		}
		if opposite := opposites[v]; opposite != "" {
			delete(want, opposite)
		}
		want[v] = true
	}
	for v := range want {
		if v == "auto" || v == "noauto" || v == "nouser" || v == "user" || v == "users" || v == "nofail" || v == "_netdev" || strings.HasPrefix(v, "x-") {
			continue
		}
		if opposite := opposites[v]; opposite != "" && have[opposite] {
			return false
		}
		// Kernel output omits these positive defaults.
		if v == "suid" || v == "dev" || v == "exec" || v == "async" {
			continue
		}
		if !have[v] {
			return false
		}
	}
	return true
}

// Prepare observes only; anticipated is an earlier storage operation's fixed output.
// previous is the ordered list of mount payloads already prepared for this host.
func (b Backend) Prepare(ctx context.Context, config inventory.Config, state string, anticipated *plan.DeviceIdentity, previous []*plan.MountChange) (*plan.MountChange, bool, error) {
	c := &plan.MountChange{Source: config.Source, Target: config.Target, Filesystem: config.Filesystem, Options: append([]string(nil), config.Options...), State: state}
	canonical, err := b.preparationPath(ctx, c.Target, previous)
	if err != nil {
		return nil, false, err
	}
	c.Target = canonical
	for _, p := range previous {
		if p.Target == c.Target {
			return nil, false, fmt.Errorf("mount target %s is already claimed", c.Target)
		}
	}
	identity, err := (storage.Backend{Run: b.Run}).ProbeDevice(ctx, c.Source)
	if err != nil {
		if !errors.Is(err, storage.ErrDeviceAbsent) {
			return nil, false, err
		}
		if anticipated != nil {
			identity = *anticipated
		} else if state != "absent" {
			return nil, false, err
		}
	}
	if anticipated != nil {
		// The earlier operation may create a filesystem on an existing device.
		if identity.UUID == "" || anticipated.Fresh {
			identity = *anticipated
		}
	}
	c.Identity = identity
	if err = c.Validate(); err != nil {
		return nil, false, err
	}
	active, err := b.active(ctx, c.Target)
	if err != nil {
		return nil, false, err
	}
	if err = checkActive(active, c); err != nil {
		return nil, false, err
	}
	c.Active = active != nil
	c.TargetPending = state == "present" && !c.Active
	before, err := b.run(ctx, "cat", "--", "/etc/fstab")
	if err != nil {
		return nil, false, err
	}
	c.FstabBefore = string(before)
	for _, p := range previous {
		if p.FstabBefore != c.FstabBefore {
			return nil, false, errors.New("fstab changed during preparation")
		}
		c.FstabBefore = p.FstabAfter
	}
	compatible := func(source string) bool {
		if source == c.Source || source == c.Identity.Path || source == "UUID="+c.Identity.UUID {
			return true
		}
		if !strings.HasPrefix(source, "/") {
			return false
		}
		other, e := (storage.Backend{Run: b.Run}).ProbeDevice(ctx, source)
		return e == nil && other.MajorMinor == c.Identity.MajorMinor && other.UUID == c.Identity.UUID
	}
	c.FstabAfter, err = editFstab(c.FstabBefore, c, compatible, func(target string) (string, error) { return b.preparationPath(ctx, target, previous) })
	if err != nil {
		return nil, false, err
	}
	changed := c.FstabBefore != c.FstabAfter
	if state == "present" {
		changed = changed || active == nil || !optionsMatch(active.Options, c.Options)
	} else {
		changed = changed || active != nil
	}
	return c, changed, nil
}

func (Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "mount" || t.File != nil || t.RPM != nil || t.Process != nil || t.Oneshot != nil || t.Path != nil || t.Storage != nil {
		return nil, errors.New("invalid mount operation or mixed payload")
	}
	c := t.Mount
	if err := c.Validate(); err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("verify block source %s identity (major:minor=%s, filesystem UUID=%s, type=%s)", c.Source, c.Identity.MajorMinor, c.Identity.UUID, c.Filesystem), "check /etc/fstab equals frozen before or after bytes; preserve unrelated entries and comments"}
	if c.State == "present" {
		lines = append(lines, "mkdir -p -- "+c.Target, "write frozen fstab entry: "+fstabEntry(c), "systemctl daemon-reload", "if unmounted: mount --types "+c.Filesystem+" --options "+mountOptions(c)+" -- "+c.Source+" "+c.Target, "if mounted options differ: mount --options remount,"+mountOptions(c)+" -- "+c.Target, "verify active source identity, filesystem and requested options")
	} else {
		lines = append(lines, "if mounted: umount -- "+c.Target+" (no force or lazy unmount)", "remove frozen fstab entry for "+c.Target, "systemctl daemon-reload", "verify target is unmounted; retain source and filesystem")
	}
	return []plan.Action{{Kind: "mount", Mount: c, Description: lines}}, nil
}
func mountOptions(c *plan.MountChange) string {
	if len(c.Options) == 0 {
		return "defaults"
	}
	return strings.Join(c.Options, ",")
}

func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind != "mount" || a.File != nil || a.RPM != nil || a.Process != nil || a.Oneshot != nil || a.Path != nil || a.Storage != nil {
		return errors.New("unsupported mount action")
	}
	c := a.Mount
	if err := c.Validate(); err != nil {
		return err
	}
	canonical, err := b.CanonicalPath(ctx, c.Target)
	if err != nil {
		return err
	}
	if canonical != c.Target {
		return errors.New("mount target path identity changed since preparation")
	}
	storageBackend := storage.Backend{Run: b.Run}
	identity, err := storageBackend.ProbeDevice(ctx, c.Source)
	if err != nil && !(c.State == "absent" && errors.Is(err, storage.ErrDeviceAbsent)) {
		return err
	}
	if err == nil && c.Identity.Path != "" {
		expected := c.Identity
		expected.Path = c.Source
		if checkErr := storageBackend.CheckDevice(ctx, expected); checkErr != nil {
			return fmt.Errorf("mount source identity changed since preparation: %w", checkErr)
		}
	}
	current := *c
	current.Identity = identity
	active, err := b.active(ctx, c.Target)
	if err != nil {
		return err
	}
	if err = checkActive(active, &current); err != nil {
		return err
	}
	before, err := b.run(ctx, "cat", "--", "/etc/fstab")
	if err != nil {
		return err
	}
	if string(before) != c.FstabBefore && string(before) != c.FstabAfter {
		return errors.New("fstab differs from frozen precondition")
	}
	if c.State == "absent" && active != nil {
		if _, err = b.run(ctx, "umount", "--", c.Target); err != nil {
			return fmt.Errorf("unmount failed (busy mounts are not forced): %w", err)
		}
		active, err = b.active(ctx, c.Target)
		if err != nil {
			return err
		}
		if active != nil {
			return errors.New("mount target remains mounted")
		}
	}
	if c.State == "present" {
		if _, err = b.run(ctx, "mkdir", "-p", "--", c.Target); err != nil {
			return err
		}
	}
	if string(before) != c.FstabAfter {
		if _, err = b.input(ctx, []byte(c.FstabAfter), "sh", "-c", writeFstab); err != nil {
			return err
		}
	}
	// Reload on continuation as well: an interrupted write may precede its reload.
	if _, err = b.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if c.State == "absent" {
		return nil
	}
	if active == nil {
		_, err = b.run(ctx, "mount", "--types", c.Filesystem, "--options", mountOptions(c), "--", c.Source, c.Target)
	} else if !optionsMatch(active.Options, c.Options) {
		_, err = b.run(ctx, "mount", "--options", "remount,"+mountOptions(c), "--", c.Target)
	}
	if err != nil {
		return err
	}
	active, err = b.active(ctx, c.Target)
	if err != nil {
		return err
	}
	if active == nil {
		return errors.New("target was not mounted")
	}
	if err = checkActive(active, &current); err != nil {
		return err
	}
	if !optionsMatch(active.Options, c.Options) {
		return errors.New("requested mount options were not established")
	}
	return nil
}

// The stdin candidate replaces fstab atomically and retains its existing owner/mode.
const writeFstab = `set -eu
candidate=$(mktemp /etc/.richt-fstab.XXXXXX)
trap 'rm -f -- "$candidate"' EXIT HUP INT TERM
cat > "$candidate"
chmod --reference=/etc/fstab "$candidate"
chown --reference=/etc/fstab "$candidate"
context=$(stat -c %C -- /etc/fstab 2>/dev/null || true)
case "$context" in ""|"?") ;; *) chcon --reference=/etc/fstab "$candidate";; esac
mv -f -- "$candidate" /etc/fstab
`
