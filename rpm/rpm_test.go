package rpm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func TestInstalledVersionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, version, installed, state string
		missing, want                   bool
	}{
		{name: "unversioned does not upgrade", installed: "0:1.0-1.noarch", state: "present"},
		{name: "absent installs", state: "present", missing: true, want: true},
		{name: "exact no-op", version: "0:1.0-1.noarch", installed: "0:1.0-1.noarch", state: "present"},
		{name: "upgrade", version: "0:2.0-1.noarch", installed: "0:1.0-1.noarch", state: "present", want: true},
		{name: "downgrade", version: "0:1.0-1.noarch", installed: "0:2.0-1.noarch", state: "present", want: true},
		{name: "remove", installed: "0:1.0-1.noarch", state: "absent", want: true},
		{name: "absent remove no-op", state: "absent", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "rpm" {
					t.Fatal("diff queried repository", name)
				}
				if tc.missing {
					return nil, ErrNotInstalled
				}
				return []byte("demo\t" + tc.installed), nil
			}}
			got, err := b.Observe(context.Background(), &plan.RPMChange{Manager: "dnf", Package: "demo", Version: tc.version, State: tc.state})
			if err != nil || got != tc.want {
				t.Fatalf("changed=%v err=%v", got, err)
			}
		})
	}
}
func TestExactNEVRACommandAndValidation(t *testing.T) {
	c := &plan.RPMChange{Manager: "yum", Package: "demo", Version: "2:1.0-3.noarch", State: "present"}
	want := []string{"-y", "install-nevra", "--", "demo-2:1.0-3.noarch"}
	if !reflect.DeepEqual(Arguments(c), want) {
		t.Fatal(Arguments(c))
	}
	for _, bad := range []string{"1.0", "0:1.0-1", "0:1.0-1.noarch --allowerasing"} {
		c.Version = bad
		if Validate(c) == nil {
			t.Fatal("accepted", bad)
		}
	}
	c.Version = ""
	c.Package = "--downloadonly"
	if Validate(c) == nil {
		t.Fatal("accepted option as package")
	}
}
func TestQueryFailureAndManagerVersion(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("database unavailable")
	}}
	if _, err := b.Observe(context.Background(), &plan.RPMChange{Manager: "dnf", Package: "demo", State: "present"}); err == nil {
		t.Fatal("ignored database error")
	}
	for _, v := range []string{"3.4.3\n", "5.1.0\n"} {
		b.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte(v), nil }
		if b.CheckManager(context.Background(), "dnf") == nil {
			t.Fatal("accepted", v)
		}
	}
}

func TestLocalCommandErrorsDoNotRetainArgumentsOrOutput(t *testing.T) {
	const private = "EXAMPLE_PRIVATE_COMMAND_VALUE"
	out, err := Run(context.Background(), "sh", "-c", `printf '%s stdout\n' "$1"; printf '%s stderr\n' "$1" >&2; exit 42`, "richt-test", private)
	if err == nil || len(out) != 0 {
		t.Fatal("expected command failure without output")
	}
	for _, diagnostic := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err)} {
		if strings.Contains(diagnostic, private) || strings.Contains(diagnostic, "printf") || strings.Contains(diagnostic, "stdout") || strings.Contains(diagnostic, "stderr") {
			t.Fatal("command diagnostic exposed sensitive inputs")
		}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || len(exit.Stderr) != 0 {
		t.Fatal("exit status was lost or retained stderr")
	}
	_, err = Run(context.Background(), "/does-not-exist/"+private)
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), private) {
		t.Fatal("executable path leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Run(ctx, "sh", "-c", "exit 0"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved")
	}
}

func TestLocalCommandReturnsOnlyStdout(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", `printf 'expected'; printf 'EXAMPLE_PRIVATE_STDERR' >&2`)
	if err != nil || string(out) != "expected" {
		t.Fatal("stderr contaminated successful stdout")
	}
}

func TestDefaultRPMCommandFailureDoesNotExposePackageOutput(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'EXAMPLE_PRIVATE_RPM_STDOUT\\n'\nprintf 'EXAMPLE_PRIVATE_RPM_STDERR\\n' >&2\nprintf '%s\\n' \"$*\" >&2\nexit 42\n"
	if err := os.WriteFile(filepath.Join(dir, "dnf"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	err := (Backend{}).Execute(context.Background(), plan.Action{Kind: "rpm", RPM: &plan.RPMChange{Manager: "dnf", Package: "EXAMPLE_PRIVATE_PACKAGE", State: "present"}})
	if err == nil || strings.Contains(fmt.Sprintf("%v %+v", err, err), "EXAMPLE_PRIVATE") {
		t.Fatal("RPM diagnostic exposed command inputs or output")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || len(exit.Stderr) != 0 {
		t.Fatal("RPM error retained output or lost exit status")
	}
}

func TestManagerVersionDiagnosticDoesNotExposeOutput(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("EXAMPLE_PRIVATE_VERSION_OUTPUT\n"), nil
	}}
	err := b.CheckManager(context.Background(), "dnf")
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "EXAMPLE_PRIVATE") {
		t.Fatal("manager diagnostic exposed command output")
	}
}
