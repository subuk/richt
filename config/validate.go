package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/subuk/richt/plan"
)

// Check saved inputs here as well as at inventory validation: executors can be
// used directly and saved plans must not bypass the command boundary.
func validatorDuration(v *plan.FileValidator) (time.Duration, error) {
	if v == nil {
		return 0, nil
	}
	if len(v.Args) == 0 || !filepath.IsAbs(v.Args[0]) || v.Args[0] == "/" {
		return 0, errors.New("file validator requires an absolute executable path")
	}
	candidates := 0
	for i, arg := range v.Args {
		if strings.ContainsRune(arg, 0) {
			return 0, errors.New("file validator arguments cannot contain NUL")
		}
		if strings.Contains(arg, "{candidate}") {
			if i == 0 || arg != "{candidate}" {
				return 0, errors.New("file validator requires a separate {candidate} argument")
			}
			candidates++
		}
	}
	if candidates != 1 {
		return 0, errors.New("file validator requires exactly one {candidate} argument")
	}
	duration, err := time.ParseDuration(v.Timeout)
	if err != nil || duration <= 0 {
		return 0, errors.New("file validator timeout must be a positive duration")
	}
	return duration, nil
}

func validateLocalCandidate(ctx context.Context, f *plan.FileChange) (err error) {
	duration, err := validatorDuration(f.Validate)
	if err != nil {
		return err
	}
	// A fixed target-side root avoids controller or environment-selected working
	// directories. The directory and contents remain private even for public files.
	dir, err := os.MkdirTemp("/tmp", "richt-validate-")
	if err != nil {
		return errors.New("file validator candidate creation failed")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.New("file validator candidate cleanup failed")
		}
	}()
	if err := os.Chmod(dir, 0700); err != nil {
		return errors.New("file validator candidate creation failed")
	}
	candidate := filepath.Join(dir, "candidate")
	if err := os.WriteFile(candidate, []byte(f.Content), 0600); err != nil {
		return errors.New("file validator candidate creation failed")
	}
	if err := os.Chmod(candidate, 0600); err != nil {
		return errors.New("file validator candidate creation failed")
	}
	args := append([]string(nil), f.Validate.Args...)
	for i := range args {
		if args[i] == "{candidate}" {
			args[i] = candidate
		}
	}
	validationCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	command := exec.CommandContext(validationCtx, args[0], args[1:]...)
	command.Dir = "/"
	// nil stdout/stderr attach /dev/null, without retaining diagnostic bytes or
	// leaving pipes held open by a validator's descendants.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	err = command.Run()
	if validationCtx.Err() != nil {
		if errors.Is(validationCtx.Err(), context.DeadlineExceeded) {
			return errors.New("file validator timed out")
		}
		return errors.New("file validator canceled")
	}
	return validatorFailure(err)
}

// Never wrap runner errors: validator output can include the entire candidate,
// including transformed secrets, even when the file was not marked sensitive.
func validatorFailure(err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("file validator failed (exit status %d)", exit.ExitCode())
	}
	return errors.New("file validator could not execute")
}

func (t Target) validateCandidate(ctx context.Context, f *plan.FileChange) error {
	if t.RunInput == nil {
		return errors.New("missing target input runner")
	}
	duration, err := validatorDuration(f.Validate)
	if err != nil {
		return err
	}
	// GNU timeout accepts seconds, not Go duration syntax. Serialize from the
	// parsed duration so the target receives no shell expressions.
	seconds := strconv.FormatFloat(duration.Seconds(), 'f', 9, 64) + "s"
	args := []string{"-c", targetValidateCandidate, "richt-file-validator", seconds}
	args = append(args, f.Validate.Args...)
	_, err = t.RunInput(ctx, []byte(f.Content), "sh", args...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 124 || exit.ExitCode() == 137) {
			return errors.New("file validator timed out")
		}
		if ctx.Err() != nil {
			return errors.New("file validator canceled")
		}
	}
	return validatorFailure(err)
}

const targetValidateCandidate = `set -eu
exec >/dev/null 2>&1
duration=$1
shift
umask 077
directory=$(mktemp -d /tmp/richt-validate-XXXXXXXXXX)
cleanup() {
  status=$?
  trap - 0
  rm -rf -- "$directory" || exit 125
  exit "$status"
}
trap cleanup 0
trap 'exit 125' HUP INT TERM
chmod 0700 "$directory"
candidate=$directory/candidate
cat > "$candidate"
chmod 0600 "$candidate"
for argument do
  if [ "$argument" = '{candidate}' ]; then argument=$candidate; fi
  set -- "$@" "$argument"
  shift
done
cd /
timeout --signal=KILL "$duration" "$@" < /dev/null
`
