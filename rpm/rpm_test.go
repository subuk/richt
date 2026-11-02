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
		{name: "upstream no-op", version: "1.34.3", installed: "0:1.34.3-1.noarch", state: "present"},
		{name: "upstream ignores epoch release architecture", version: "1.34.3", installed: "7:1.34.3-42.el9.aarch64", state: "present"},
		{name: "upstream upgrade", version: "1.34.3", installed: "0:1.34.2-1.x86_64", state: "present", want: true},
		{name: "upstream downgrade", version: "1.34.3", installed: "0:1.35.0-1.x86_64", state: "present", want: true},
		{name: "upstream missing", version: "1.34.3", state: "present", missing: true, want: true},
		{name: "upstream does not match prefix", version: "1.34.3", installed: "0:1.34.30-1.x86_64", state: "present", want: true},
		{name: "upstream preserves literal version", version: "1.34.3", installed: "0:1.34.03-1.x86_64", state: "present", want: true},
		{name: "exact observes release", version: "0:1.0-1.noarch", installed: "0:1.0-2.noarch", state: "present", want: true},
		{name: "exact observes epoch", version: "0:1.0-1.noarch", installed: "1:1.0-1.noarch", state: "present", want: true},
		{name: "exact observes architecture", version: "0:1.0-1.noarch", installed: "0:1.0-1.x86_64", state: "present", want: true},
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
	for _, bad := range []string{"1.0-*", "0:1.0-1", "0:1.0-1.noarch --allowerasing"} {
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

func TestUpstreamObserveAcceptsAnyInstalledMatch(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("other\t0:1.34.3-1.noarch\ndemo\t0:1.34.30-1.x86_64\ndemo\t7:1.34.3-9.i686\n"), nil
	}}
	changed, err := b.Observe(context.Background(), &plan.RPMChange{Manager: "dnf", Package: "demo", Version: "1.34.3", State: "present"})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestUpstreamVersionCommand(t *testing.T) {
	for _, manager := range []string{"dnf", "yum"} {
		t.Run(manager, func(t *testing.T) {
			c := &plan.RPMChange{Manager: manager, Package: "demo", Version: "1.34.3", State: "present"}
			want := []string{"-y", "install", "--", "demo-1.34.3"}
			if !reflect.DeepEqual(Arguments(c), want) {
				t.Fatal(Arguments(c))
			}
			calls := 0
			b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name != manager || !reflect.DeepEqual(args, want) {
					t.Fatalf("unexpected command: %s %q", name, args)
				}
				return nil, nil
			}}
			if err := b.Execute(context.Background(), plan.Action{Kind: "rpm", RPM: c}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || c.Version != "1.34.3" {
				t.Fatalf("calls=%d; request=%+v", calls, c)
			}
		})
	}
}

func TestUpstreamVersionFailureIsNotRetried(t *testing.T) {
	failure := errors.New("DNF request failed")
	calls := 0
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, failure
	}}
	err := b.Execute(context.Background(), plan.Action{Kind: "rpm", RPM: &plan.RPMChange{Manager: "dnf", Package: "demo", Version: "1.34.3", State: "present"}})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestMalformedVersionFailsBeforeCommand(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("malformed request reached DNF")
		return nil, nil
	}}
	for _, version := range []string{"1.34.*", "1.34.3 --allowerasing", "0:1.34.3"} {
		if err := b.Execute(context.Background(), plan.Action{Kind: "rpm", RPM: &plan.RPMChange{Manager: "dnf", Package: "demo", Version: version, State: "present"}}); err == nil {
			t.Fatal("accepted malformed version", version)
		}
	}
}

func TestUpstreamResolveDisplaysApplyBoundaryWithoutRepositoryQuery(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("display contacted target")
		return nil, nil
	}}
	c := &plan.RPMChange{Manager: "dnf", Package: "demo", Version: "1.34.3", State: "present"}
	actions, err := b.Resolve(plan.Task{Operation: "rpm", RPM: c})
	if err != nil {
		t.Fatal(err)
	}
	shown := strings.Join(actions[0].Description, "\n")
	for _, text := range []string{"dnf -y install -- demo-1.34.3", "Upstream version 1.34.3", "at apply", "epoch, release, and architecture"} {
		if !strings.Contains(shown, text) {
			t.Fatalf("missing %q in %q", text, shown)
		}
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
