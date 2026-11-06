package apt

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

func request(t *testing.T, version, state string) *plan.DEBChange {
	t.Helper()
	c, err := Prepare(inventory.Binary{Type: "deb", Package: "example+", Version: version}, state)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestPrepareAndValidate(t *testing.T) {
	c := request(t, "", "")
	if c.Manager != "apt" || c.State != "present" || c.CacheMaxAge != "48h" || c.LockTimeout != "60s" || c.Timeout != "15m" || c.ConffilePolicy != "keep" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if Validate(nil) == nil {
		t.Fatal("nil request accepted")
	}
	for _, v := range []inventory.Binary{{Manager: "apt-get", Package: "example"}, {Package: "example:arm64"}, {Package: "example*"}, {Package: "example", Version: "latest"}} {
		if _, err := Prepare(v, ""); err == nil {
			t.Fatalf("invalid request accepted: %+v", v)
		}
	}
}
func TestObserveInstalledDatabaseOnly(t *testing.T) {
	cases := []struct {
		name, output, version, state string
		changed, fail                bool
	}{
		{name: "missing", changed: true},
		{name: "installed preserve version", output: "example+\t1.0-1\tii \tamd64\n"},
		{name: "installed exact", output: "example+\t1.0-1\tii \tamd64\n", version: "1.0-1"},
		{name: "removed conffiles", output: "example+\t1.0-1\trc \tamd64\n", changed: true},
		{name: "removed conffiles absent", output: "example+\t1.0-1\trc \tamd64\n", state: "absent"},
		{name: "remove installed", output: "example+\t1.0-1\tii \tamd64\n", state: "absent", changed: true},
		{name: "foreign is ignored", output: "example+\t1.0-1\tii \tarm64\n", changed: true},
		{name: "all architecture", output: "example+\t1.0-1\tii \tall\n"},
		{name: "held unchanged", output: "example+\t1.0-1\thi \tamd64\n"},
		{name: "held removal", output: "example+\t1.0-1\thi \tamd64\n", state: "absent", fail: true},
		{name: "half configured", output: "example+\t1.0-1\tiF \tamd64\n", fail: true},
		{name: "unpacked", output: "example+\t1.0-1\tiU \tamd64\n", fail: true},
		{name: "reinstall required", output: "example+\t1.0-1\tiiR\tamd64\n", fail: true},
		{name: "invalid status", output: "example+\t1.0-1\tbad\tamd64\n", fail: true},
		{name: "malformed data", output: "example+ SECRET\n", fail: true},
		{name: "invalid installed version", output: "example+\tSECRET\tii \tamd64\n", fail: true},
		{name: "duplicate native", output: "example+\t1.0-1\tii \tamd64\nexample+\t1.0-1\tii \tall\n", fail: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name == "dpkg" && len(args) == 1 && args[0] == "--print-architecture" {
					return []byte("amd64\n"), nil
				}
				if name == "dpkg-query" && strings.Join(args, " ") == "--show --showformat="+queryFormat {
					return []byte(tt.output), nil
				}
				t.Fatalf("unexpected observation command %s %v", name, args)
				return nil, nil
			}}
			changed, err := b.Observe(context.Background(), request(t, tt.version, tt.state))
			if (err != nil) != tt.fail || (err == nil && changed != tt.changed) {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("metadata leaked")
			}
		})
	}
}
func TestObserveDebianVersionEquality(t *testing.T) {
	for _, equal := range []bool{true, false} {
		t.Run(map[bool]string{true: "equivalent spelling", false: "version change"}[equal], func(t *testing.T) {
			var failure error
			if !equal {
				failure = exec.Command("sh", "-c", "exit 1").Run()
			}
			comparisons := 0
			b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "dpkg-query" {
					return []byte("example+\t1.0-0\tii \tamd64\n"), nil
				}
				if len(args) == 1 && args[0] == "--print-architecture" {
					return []byte("amd64"), nil
				}
				if name == "dpkg" && strings.Join(args, " ") == "--compare-versions 0:1.0 eq 1.0-0" {
					comparisons++
					return nil, failure
				}
				t.Fatalf("unexpected command %s %v", name, args)
				return nil, nil
			}}
			changed, err := b.Observe(context.Background(), request(t, "0:1.0", ""))
			if err != nil || changed == equal || comparisons != 1 {
				t.Fatalf("changed=%v err=%v comparisons=%d", changed, err, comparisons)
			}
		})
	}
}
func TestObserveErrorsAreSafe(t *testing.T) {
	for _, command := range []string{"dpkg", "dpkg-query", "compare"} {
		b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == command || command == "compare" && len(args) > 1 {
				return []byte("SECRET"), errors.New("SECRET")
			}
			if name == "dpkg-query" {
				return []byte("example+\t1.0\tii \tamd64\n"), nil
			}
			return []byte("amd64"), nil
		}}
		_, err := b.Observe(context.Background(), request(t, "2.0", ""))
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe error %v", err)
		}
	}
}
func TestResolveAndExecute(t *testing.T) {
	c := request(t, "1.0-1", "")
	b := Backend{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "python3" || len(args) != 4 || args[0] != "-I" || args[1] != "-c" || !strings.Contains(args[2], "def refresh(") {
			t.Fatalf("unexpected helper invocation %s", name)
		}
		var input struct {
			Op                                 string          `json:"op"`
			Change                             *plan.DEBChange `json:"change"`
			Seconds, LockSeconds, CacheSeconds float64
		}
		var data map[string]json.RawMessage
		if json.Unmarshal([]byte(args[3]), &input) != nil || json.Unmarshal([]byte(args[3]), &data) != nil || input.Op != "apply" || input.Change.Package != c.Package {
			t.Fatalf("invalid input %s", args[3])
		}
		if string(data["seconds"]) != "900" || string(data["lock_seconds"]) != "60" || string(data["cache_seconds"]) != "172800" {
			t.Fatalf("invalid bounds %s", args[3])
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("helper is unbounded")
		}
		return []byte("{}"), nil
	}}
	actions, err := b.Resolve(plan.Task{Operation: "deb", DEB: c})
	if err != nil || len(actions) != 1 {
		t.Fatal(actions, err)
	}
	display := strings.Join(actions[0].Description, "\n")
	for _, term := range []string{"48h", "60s", "15m", "conffiles", "signature", "downgrade", "no automatic retries"} {
		if !strings.Contains(display, term) {
			t.Fatalf("missing %s in %s", term, display)
		}
	}
	if err := b.Execute(context.Background(), actions[0]); err != nil {
		t.Fatal(err)
	}
	c.State = "absent"
	actions, err = b.Resolve(plan.Task{Operation: "deb", DEB: c})
	if err != nil || strings.Contains(actions[0].Description[0], "=1.0") {
		t.Fatal(actions, err)
	}
	for _, task := range []plan.Task{{Operation: "rpm", DEB: c}, {Operation: "deb", DEB: c, RPM: &plan.RPMChange{}}, {Operation: "deb"}} {
		if _, err := b.Resolve(task); err == nil {
			t.Fatal("invalid task accepted")
		}
	}
	if b.Execute(context.Background(), plan.Action{Kind: "deb", DEB: c, RPM: &plan.RPMChange{}}) == nil {
		t.Fatal("mixed action accepted")
	}
}
func TestHelperErrorCategoriesAreSafe(t *testing.T) {
	for _, out := range []string{`{"error":"update"}`, `{"error":"SECRET"}`, `{"error":null}`, `{"extra":1}`, `null`, `SECRET`, strings.Repeat("S", 4097)} {
		b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }}
		err := b.Execute(context.Background(), plan.Action{Kind: "deb", DEB: request(t, "", "")})
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe error %v", err)
		}
	}
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("SECRET") }}
	if err := b.Execute(context.Background(), plan.Action{Kind: "deb", DEB: request(t, "", "")}); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal(err)
	}
}
func TestCheckManager(t *testing.T) {
	for _, version := range []string{"apt 1.8.2 (amd64)", "apt 2.0.9 (amd64)", "SECRET", "apt 2.2.4 (amd64)", "apt 3.0.0 (amd64)"} {
		b := Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "apt-get" {
				return []byte(version), nil
			}
			if name == "python3" {
				return []byte("{}"), nil
			}
			return []byte("Debian dpkg-query 1.22"), nil
		}}
		err := b.CheckManager(context.Background(), "apt")
		valid := strings.HasPrefix(version, "apt 2.2") || strings.HasPrefix(version, "apt 3.")
		if (err == nil) != valid {
			t.Fatalf("version=%s err=%v", version, err)
		}
	}
}
func TestTargetHelper(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python3 unavailable for helper boundary tests")
	}
	command := exec.Command(python, "-B", "target_test.py")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper tests: %v\n%s", err, out)
	}
}

func TestFixedTransportDiagnostics(t *testing.T) {
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("SSH authentication failed; noninteractive authentication is required: SECRET")
	}}
	err := b.Execute(context.Background(), plan.Action{Kind: "deb", DEB: request(t, "", "")})
	if err == nil || err.Error() != "SSH authentication failed; noninteractive authentication is required" {
		t.Fatal(err)
	}
}
