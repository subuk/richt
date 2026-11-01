package rpm

import (
	"context"
	"errors"
	"reflect"
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
