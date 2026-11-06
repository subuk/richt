package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/subuk/richt/plan"
)

// Target implements file operations through target commands. It is used for
// SSH and local sudo, so content and account lookup always use the same target.
// Commands require the standard GNU/Linux utilities already used by Richt's
// RPM and systemd targets; no remote agent or interpreter installation is needed.
type Target struct {
	Run      func(context.Context, string, ...string) ([]byte, error)
	RunInput func(context.Context, []byte, string, ...string) ([]byte, error)
}

func (t Target) run(ctx context.Context, script string, args ...string) ([]byte, error) {
	if t.Run == nil {
		return nil, errors.New("missing target command runner")
	}
	out, err := t.Run(ctx, "sh", append([]string{"-c", script, "richt-file"}, args...)...)
	return out, targetFileError(err)
}

func targetFileError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 71:
			return errors.New("target is not a regular file")
		case 72:
			return errors.New("file owner does not exist on target")
		case 73:
			return errors.New("file group does not exist on target")
		case 74:
			return errors.New("SELinux is not enabled on target")
		}
	}
	return err
}

const targetAccounts = `
uid=-
gid=-
if [ -n "$2" ]; then
  record=$(getent passwd -- "$2") || exit 72
  uid=$(printf '%s' "$record" | cut -d: -f3)
  case "$uid" in ''|*[!0-9]*) exit 72;; esac
fi
if [ -n "$3" ]; then
  record=$(getent group -- "$3") || exit 73
  gid=$(printf '%s' "$record" | cut -d: -f3)
  case "$gid" in ''|*[!0-9]*) exit 73;; esac
fi
if [ -n "$4" ]; then
  [ -e /sys/fs/selinux/enforce ] || exit 74
fi
`

func targetFileArguments(f *plan.FileChange, anticipated ...Ownership) []string {
	owner, group := projectedOwnership(anticipated).observedNames(f.Owner, f.Group)
	if f.State == "absent" {
		owner, group = "", ""
	}
	return []string{f.Path, owner, group, f.SELinux, f.State}
}

// Observe computes the patch and attribute changes on the target. Owner/group
// names remain in the plan; their numeric identities are not frozen locally.
func (t Target) Observe(ctx context.Context, f *plan.FileChange, anticipated ...Ownership) (changed bool, err error) {
	defer func() {
		if err != nil && f != nil && f.Sensitive {
			err = errors.New("sensitive file observation failed")
		}
	}()
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return false, err
	}
	uid, gid, err := t.targetOwnership(ctx, f, anticipated...)
	if err != nil {
		return false, err
	}
	state, old, err := observeEntry(ctx, t.RunInput, f)
	if err != nil {
		return false, err
	}
	return entryChanged(f, state, old, uid, gid, projectedOwnership(anticipated).pending())
}

func (Target) Resolve(task plan.Task) ([]plan.Action, error) { return (Local{}).Resolve(task) }

// ObserveMissing checks target capabilities and accounts without opening a file
// whose prepared predecessor task will remove it, for example a systemd mask.
func (t Target) ObserveMissing(ctx context.Context, f *plan.FileChange, anticipated ...Ownership) (changed bool, err error) {
	defer func() {
		if err != nil && f != nil && f.Sensitive {
			err = errors.New("sensitive file observation failed")
		}
	}()
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return false, err
	}
	if f.State != "present" {
		return false, errors.New("missing file observation requires present state")
	}
	if _, err := t.run(ctx, "set -eu\n"+targetAccounts, targetFileArguments(f, anticipated...)...); err != nil {
		return false, err
	}
	f.Write = true
	return true, freezeFile(f, &plan.FileState{Kind: "absent"}, nil)
}

func (t Target) Execute(ctx context.Context, a plan.Action) (err error) {
	defer func() {
		if err != nil && a.File != nil && a.File.Sensitive {
			err = errors.New("sensitive file operation failed")
		}
	}()
	if a.File == nil {
		return errors.New("missing file action inputs")
	}
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: a.File}); err != nil {
		return err
	}
	f := a.File
	if err := f.ValidatePrepared(); err != nil {
		return err
	}
	if a.Kind == "create-parent-directories" {
		_, err := t.run(ctx, `set -eu
mkdir -p -- "${1%/*}/"
`, f.Path)
		return err
	}
	if a.Kind != "overwrite-file" && a.Kind != "set-file-attributes" && a.Kind != "remove-file" {
		return fmt.Errorf("unsupported target file action %q", a.Kind)
	}
	if a.Kind == "overwrite-file" && f.Write && f.Validate != nil {
		if err := t.validateCandidate(ctx, f); err != nil {
			return err
		}
	}
	uid, gid, err := t.targetOwnership(ctx, f)
	if err != nil {
		return err
	}
	_, err = fileCommand(ctx, t.RunInput, fileRequest{Action: a.Kind, File: f, Resume: a.Resume, UID: uid, GID: gid})
	return err
}

func (t Target) targetOwnership(ctx context.Context, f *plan.FileChange, anticipated ...Ownership) (int, int, error) {
	out, err := t.run(ctx, "set -eu\n"+targetAccounts+`printf '%s\n%s\n' "$uid" "$gid"
`, targetFileArguments(f, anticipated...)...)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, errors.New("target returned invalid file ownership")
	}
	owner, group := projectedOwnership(anticipated).remote(fields[0], fields[1])
	ids := []int{-1, -1}
	for i, value := range []string{owner, group} {
		if value != "-" {
			ids[i], err = strconv.Atoi(value)
			if err != nil || ids[i] < 0 {
				return 0, 0, errors.New("target returned invalid file ownership")
			}
		}
	}
	return ids[0], ids[1], nil
}

func (t Target) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return t.run(ctx, `set -eu
[ -f "$1" ] || exit 71
cat -- "$1"
`, path)
}

// Glob matches the final component of an absolute path on the target. Systemd
// drop-in discovery uses literal parent directories, avoiding shell evaluation.
func (t Target) Glob(ctx context.Context, pattern string) ([]string, error) {
	dir, pattern := filepath.Dir(pattern), filepath.Base(pattern)
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "*?[") {
		return nil, errors.New("target glob requires a literal absolute parent directory")
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	out, err := t.run(ctx, `set -eu
[ -d "$1" ] || exit 0
find -H "$1" -mindepth 1 -maxdepth 1 -name "$2" -print0
`, dir, pattern)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("target returned invalid glob paths")
	}
	paths := strings.Split(string(out[:len(out)-1]), "\x00")
	sort.Strings(paths)
	return paths, nil
}
