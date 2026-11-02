package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
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
uid=$4
gid=$5
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
if [ -n "$6" ]; then
  [ -e /sys/fs/selinux/enforce ] || exit 74
fi
`

func targetFileArguments(f *plan.FileChange) []string {
	uid, gid := "-", "-"
	if f.UID != nil {
		uid = strconv.Itoa(*f.UID)
	}
	if f.GID != nil {
		gid = strconv.Itoa(*f.GID)
	}
	return []string{f.Path, f.Owner, f.Group, uid, gid, f.SELinux, f.State}
}

// Observe computes the patch and attribute changes on the target. Owner/group
// names remain in the plan; their numeric identities are not frozen locally.
func (t Target) Observe(ctx context.Context, f *plan.FileChange) (changed bool, err error) {
	defer func() {
		if err != nil && f != nil && f.Sensitive {
			err = errors.New("sensitive file observation failed")
		}
	}()
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return false, err
	}
	out, err := t.run(ctx, `set -eu
`+targetAccounts+`
path=$1
if [ "$7" = absent ] && [ -L "$path" ]; then
  printf 'present\n0\n0\n0\n%s\n%s\n\n' "$uid" "$gid"
elif [ ! -e "$path" ]; then
  printf 'missing\n0\n0\n0\n%s\n%s\n\n' "$uid" "$gid"
else
  [ -f "$path" ] || exit 71
  printf 'present\n'
  stat -Lc '%a
%u
%g' -- "$path"
  printf '%s\n%s\n' "$uid" "$gid"
  if [ -n "$6" ]; then stat -Lc '%C' -- "$path"; else printf '\n'; fi
  cat -- "$path"
fi
`, targetFileArguments(f)...)
	if err != nil {
		return false, err
	}
	fields := bytes.SplitN(out, []byte{'\n'}, 8)
	if len(fields) != 8 {
		return false, errors.New("target returned incomplete file metadata")
	}
	if string(fields[0]) == "missing" {
		if f.State == "absent" {
			return false, nil
		}
		f.Write = true
		f.Patch = udiff.Unified("/dev/null", f.Path, "", f.Content)
		return true, nil
	}
	if string(fields[0]) != "present" {
		return false, errors.New("target returned invalid file metadata")
	}
	old := fields[7]
	if f.State == "absent" {
		f.Patch = udiff.Unified(f.Path, "/dev/null", string(old), "")
		return true, nil
	}
	mode, err := strconv.ParseUint(string(fields[1]), 8, 12)
	if err != nil {
		return false, errors.New("target returned invalid file permissions")
	}
	for _, i := range []int{2, 3} {
		if _, err := strconv.ParseUint(string(fields[i]), 10, 32); err != nil {
			return false, errors.New("target returned invalid file ownership")
		}
	}
	f.Write = !bytes.Equal(old, []byte(f.Content))
	f.Patch = ""
	if f.Write {
		f.Patch = udiff.Unified(f.Path, f.Path, string(old), f.Content)
	}
	changed = f.Write || (f.Mode != nil && *f.Mode != uint32(mode))
	changed = changed || (string(fields[4]) != "-" && !bytes.Equal(fields[2], fields[4])) || (string(fields[5]) != "-" && !bytes.Equal(fields[3], fields[5]))
	changed = changed || (f.SELinux != "" && string(fields[6]) != f.SELinux)
	return changed, nil
}

func (Target) Resolve(task plan.Task) ([]plan.Action, error) { return (Local{}).Resolve(task) }

// ObserveMissing checks target capabilities and accounts without opening a file
// whose prepared predecessor task will remove it, for example a systemd mask.
func (t Target) ObserveMissing(ctx context.Context, f *plan.FileChange) (changed bool, err error) {
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
	if _, err := t.run(ctx, "set -eu\n"+targetAccounts, targetFileArguments(f)...); err != nil {
		return false, err
	}
	f.Write = true
	f.Patch = udiff.Unified("/dev/null", f.Path, "", f.Content)
	return true, nil
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
	switch a.Kind {
	case "create-parent-directories":
		_, err := t.run(ctx, `set -eu
path=$1
mkdir -p -- "${path%/*}/"
`, f.Path)
		return err
	case "remove-file":
		_, err := t.run(ctx, `set -eu
path=$1
if [ -L "$path" ]; then rm -f -- "$path";
elif [ -e "$path" ]; then [ -f "$path" ] || exit 71; rm -f -- "$path"; fi
`, f.Path)
		return err
	case "overwrite-file":
		if t.RunInput == nil {
			return errors.New("missing target input runner")
		}
		if f.Write && f.Validate != nil {
			if err := t.validateCandidate(ctx, f); err != nil {
				return err
			}
		}
		privateWrite := ""
		if f.Sensitive && f.Mode != nil {
			privateWrite = "private"
		}
		_, err := t.RunInput(ctx, []byte(f.Content), "sh", "-c", `set -eu
path=$1
if [ -e "$path" ]; then [ -f "$path" ] || exit 71; fi
if [ "$2" = private ]; then
  umask 077
  : >> "$path"
  chmod -- 0600 "$path"
else
  umask 022
fi
cat > "$path"
sync -f -- "$path"
`, "richt-file", f.Path, privateWrite)
		return targetFileError(err)
	case "set-file-attributes":
		mode := ""
		if f.Mode != nil {
			mode = strconv.FormatUint(uint64(*f.Mode), 8)
		}
		_, err := t.run(ctx, `set -eu
`+targetAccounts+`
path=$1
[ -f "$path" ] || exit 71
if [ "$uid" != - ] || [ "$gid" != - ]; then
  if [ "$uid" = - ]; then ownership=:+$gid
  elif [ "$gid" = - ]; then ownership=+$uid
  else ownership=+$uid:+$gid; fi
  chown -- "$ownership" "$path"
fi
[ -z "$8" ] || chmod -- "$8" "$path"
[ -z "$6" ] || chcon -- "$6" "$path"
sync -f -- "$path"
`, append(targetFileArguments(f), mode)...)
		return err
	default:
		return fmt.Errorf("unsupported target file action %q", a.Kind)
	}
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
