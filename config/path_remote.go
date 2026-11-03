package config

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/subuk/richt/plan"
)

func pathArgs(p *plan.PathChange) []string {
	mode := ""
	if p.Mode != nil {
		mode = strconv.FormatUint(uint64(*p.Mode), 8)
	}
	device, inode := "", ""
	if p.Identity != nil {
		device = strconv.FormatUint(p.Identity.Device, 10)
		inode = strconv.FormatUint(p.Identity.Inode, 10)
	}
	return []string{p.Path, p.Owner, p.Group, "", p.State, p.Type, mode, device, inode}
}

func (t Target) ObservePath(ctx context.Context, p *plan.PathChange) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	out, err := t.run(ctx, `set -eu
`+targetAccounts+`
path=$1
if [ ! -e "$path" ] && [ ! -L "$path" ]; then printf 'missing\n'; exit 0; fi
if [ "$5" = absent ] && [ -L "$path" ]; then
  printf 'symlink\n'
  stat -c '%d
%i
%a
%u
%g' -- "$path"
else
  if [ "$6" = file ]; then [ -f "$path" ] || exit 71; else [ -d "$path" ] || exit 71; fi
  printf 'present\n'
  stat -Lc '%d
%i
%a
%u
%g' -- "$path"
fi
printf '%s\n%s\n' "$uid" "$gid"
`, pathArgs(p)...)
	if err != nil {
		return false, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(fields) == 1 && fields[0] == "missing" {
		return p.State == "present", nil
	}
	if len(fields) != 8 || fields[0] != "present" && fields[0] != "symlink" {
		return false, errors.New("target returned invalid path metadata")
	}
	numbers := make([]uint64, 5)
	for i := range numbers {
		base := 10
		if i == 2 {
			base = 8
		}
		numbers[i], err = strconv.ParseUint(fields[i+1], base, 64)
		if err != nil {
			return false, errors.New("target returned invalid path metadata")
		}
	}
	p.Identity = &plan.PathIdentity{Device: numbers[0], Inode: numbers[1]}
	if p.State == "absent" {
		return true, nil
	}
	return p.Mode != nil && uint64(*p.Mode) != numbers[2] || fields[6] != "-" && fields[6] != fields[4] || fields[7] != "-" && fields[7] != fields[5], nil
}

func (Target) ResolvePath(task plan.Task) ([]plan.Action, error) { return (Local{}).ResolvePath(task) }

func (t Target) ObservePathMissing(ctx context.Context, p *plan.PathChange) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	if p.State != "present" {
		return false, errors.New("missing path observation requires present state")
	}
	if _, err := t.run(ctx, "set -eu\n"+targetAccounts, pathArgs(p)...); err != nil {
		return false, err
	}
	p.Identity = nil
	return true, nil
}
func (Local) ObservePathMissing(ctx context.Context, p *plan.PathChange) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := p.Validate(); err != nil {
		return false, err
	}
	if p.State != "present" {
		return false, errors.New("missing path observation requires present state")
	}
	if _, _, err := pathOwnership(p); err != nil {
		return false, err
	}
	p.Identity = nil
	return true, nil
}

func (t Target) ExecutePath(ctx context.Context, a plan.Action) error {
	p := a.Path
	if p == nil {
		return errors.New("missing path inputs")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.State == "absent" {
		if a.Kind != "remove-path" {
			return errors.New("invalid path removal action")
		}
		_, err := t.run(ctx, `set -eu
path=$1
if [ ! -e "$path" ] && [ ! -L "$path" ]; then exit 0; fi
[ -n "$8" ] && [ -n "$9" ] || { printf 'path identity missing\n' >&2; exit 1; }
identity=$(stat -c '%d:%i' -- "$path")
[ "$identity" = "$8:$9" ] || { printf 'path identity changed since preparation\n' >&2; exit 1; }
if [ -L "$path" ]; then rm -- "$path"; exit 0; fi
if [ "$6" = file ]; then [ -f "$path" ] || exit 71; rm -- "$path"; exit 0; fi
[ -d "$path" ] || exit 71
# Refuse bind mounts as well as mounts backed by a different filesystem.
if find "$path" -type d -exec sh -c 'mountpoint -q -- "$1" && printf "mounted\n"; exit 0' sh {} \; | grep -q mounted; then
  printf 'recursive directory deletion crosses an active mount\n' >&2; exit 1
fi
[ "$(stat -c '%d:%i' -- "$path")" = "$8:$9" ] || exit 1
rm -rf --one-file-system -- "$path"
[ ! -e "$path" ] && [ ! -L "$path" ]
`, pathArgs(p)...)
		return err
	}
	if a.Kind != "ensure-path" {
		return fmt.Errorf("invalid path action %q", a.Kind)
	}
	_, err := t.run(ctx, `set -eu
`+targetAccounts+`
path=$1
umask 022
if [ -n "$8" ]; then
  [ -e "$path" ] && [ "$(stat -Lc '%d:%i' -- "$path")" = "$8:$9" ] || { printf 'path identity changed since preparation\n' >&2; exit 1; }
fi
mkdir -p -- "${path%/*}/"
if [ "$6" = file ]; then
  if [ -e "$path" ] || [ -L "$path" ]; then [ -f "$path" ] || exit 71;
  else (set -C; : > "$path") || [ -f "$path" ]; fi
  [ -f "$path" ] || exit 71
else
  if [ -e "$path" ] || [ -L "$path" ]; then [ -d "$path" ] || exit 71; else mkdir -- "$path"; fi
  [ -d "$path" ] || exit 71
fi
if [ -n "$8" ]; then
  [ "$(stat -Lc '%d:%i' -- "$path")" = "$8:$9" ] || { printf 'path identity changed since preparation\n' >&2; exit 1; }
fi
if [ "$uid" != - ] || [ "$gid" != - ]; then
  if [ "$uid" = - ]; then ownership=:+$gid
  elif [ "$gid" = - ]; then ownership=+$uid
  else ownership=+$uid:+$gid; fi
  chown -- "$ownership" "$path"
fi
[ -z "$7" ] || chmod -- "$7" "$path"
`, pathArgs(p)...)
	return err
}
