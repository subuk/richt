package artifact

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

// Bind the Python test source to Go's test cache as well as the subprocess.
//
//go:embed target_test.py
var targetTestSource string

func binary() inventory.Binary {
	return inventory.Binary{Type: "download", URL: "https://example.test/tool", SHA256: strings.Repeat("a", 64), Path: "/opt/tool"}
}

func TestPreparedInputsAndObservation(t *testing.T) {
	p, err := Prepare(binary(), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Timeout != "5m" || p.Mode == nil || *p.Mode != 0755 || len(p.Token) != 32 {
		t.Fatalf("bad defaults: %+v", p)
	}
	other, _ := Prepare(binary(), "")
	if p.Token == other.Token {
		t.Fatal("different prepared tasks share a token")
	}
	b := Backend{RunInput: func(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
		if name != "python3" || len(args) != 3 || args[0] != "-I" || args[1] != "-c" || !strings.Contains(args[2], "def download") {
			t.Fatal("helper not delivered literally")
		}
		var request struct {
			Op     string               `json:"op"`
			Change *plan.ArtifactChange `json:"change"`
		}
		if err := json.Unmarshal(input, &request); err != nil {
			t.Fatal(err)
		}
		if request.Op != "observe" || request.Change.URL != p.URL {
			t.Fatal("unexpected request")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("observation has no deadline")
		}
		return []byte(`{"changed":true,"identity":{"device":7,"inode":11},"token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`), nil
	}}
	changed, err := b.Observe(context.Background(), p)
	if err != nil || !changed || p.Identity == nil || p.Identity.Inode != 11 || p.ExpectedToken != strings.Repeat("b", 32) {
		t.Fatalf("observe: %+v %v %v", p, changed, err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactValidationBeforeTransport(t *testing.T) {
	for _, mutate := range []func(*inventory.Binary){
		func(b *inventory.Binary) { b.URL = "http://example.test/tool" },
		func(b *inventory.Binary) { b.URL = "https://user:password@example.test/tool" },
		func(b *inventory.Binary) { b.URL = "https://example.test/tool?token=secret" },
		func(b *inventory.Binary) { b.SHA256 = "bad" },
		func(b *inventory.Binary) { b.Path = "relative" },
		func(b *inventory.Binary) { b.Path = "/opt/.richt-artifact-private/tool" },
		func(b *inventory.Binary) { b.Mode = "4755" },
		func(b *inventory.Binary) { b.Timeout = "25h" },
		func(b *inventory.Binary) { b.Type = "archive"; b.Format = "zip" },
	} {
		b := binary()
		mutate(&b)
		if _, err := Prepare(b, "present"); err == nil {
			t.Fatalf("accepted invalid binary: %+v", b)
		}
	}
}

func TestSafeDiagnosticsAndTaskShape(t *testing.T) {
	p, _ := Prepare(binary(), "present")
	b := Backend{RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New("arbitrary-secret-body")
	}}
	if _, err := b.Observe(context.Background(), p); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe failure: %v", err)
	}
	if _, err := b.Resolve(plan.Task{Operation: "artifact", Artifact: p, File: &plan.FileChange{}}); err == nil {
		t.Fatal("accepted mixed task")
	}
	a, err := b.Resolve(plan.Task{Operation: "artifact", Artifact: p})
	if err != nil || len(a) != 1 || a[0].Kind != "ensure-artifact" {
		t.Fatalf("resolve: %+v %v", a, err)
	}
	if err := b.Execute(context.Background(), plan.Action{Kind: "remove-artifact", Artifact: p}); err == nil {
		t.Fatal("accepted contradictory action")
	}
}

func TestSavedArtifactPreconditions(t *testing.T) {
	for _, mutate := range []func(*plan.ArtifactChange){
		func(p *plan.ArtifactChange) { p.Token = "not-a-token" },
		func(p *plan.ArtifactChange) { p.Identity = &plan.PathIdentity{Device: 1, Inode: 1} },
		func(p *plan.ArtifactChange) { p.ExpectedToken = strings.Repeat("b", 32) },
		func(p *plan.ArtifactChange) {
			p.Identity = &plan.PathIdentity{Device: 1}
			p.ExpectedToken = strings.Repeat("b", 32)
		},
		func(p *plan.ArtifactChange) { p.URL = "https://example.test/file#" },
		func(p *plan.ArtifactChange) { p.Owner = "bad account" },
		func(p *plan.ArtifactChange) { p.Path = "/opt/tab\ttool" },
	} {
		p, err := Prepare(binary(), "present")
		if err != nil {
			t.Fatal(err)
		}
		mutate(p)
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid saved artifact: %+v", p)
		}
	}
}

func TestTargetHelperInvariants(t *testing.T) {
	if targetTestSource == "" {
		t.Fatal("target helper tests are empty")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python3 unavailable")
	}
	cmd := exec.Command(python, "-B", "target_test.py")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("target helper tests: %v\n%s", err, out)
	}
}
