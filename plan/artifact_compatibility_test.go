package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/artifact"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

func TestArtifactPlanVersionAndPayloadCompatibility(t *testing.T) {
	c, err := artifact.Prepare(inventory.Binary{Type: "download", Path: "/opt/app", URL: "https://example.com/app", SHA256: strings.Repeat("a", 64)}, "present")
	if err != nil {
		t.Fatal(err)
	}
	p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "app/binary/0", Operation: "artifact", Artifact: c, Progress: plan.Progress{Status: "pending"}}}}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Version = "1.6"
	if err := p.Validate(); err == nil {
		t.Fatal("legacy plan accepted a new artifact operation")
	}
	p.Version = plan.Version
	task := &p.Hosts[0].Tasks[0]
	task.Operation = "rpm"
	if err := p.Validate(); err == nil {
		t.Fatal("artifact payload accepted for another operation")
	}
	task.Operation = "artifact"
	task.RPM = &plan.RPMChange{Manager: "dnf", Package: "app", State: "present"}
	if err := p.Validate(); err == nil {
		t.Fatal("mixed artifact payload accepted")
	}
	task.RPM, task.Artifact = nil, nil
	if err := p.Validate(); err == nil {
		t.Fatal("artifact operation accepted without payload")
	}
}

func TestOldPlansRequireRepreparation(t *testing.T) {
	for _, version := range []string{"1.5", "1.6"} {
		file := filepath.Join(t.TempDir(), "old.json")
		if err := os.WriteFile(file, []byte(`{"version":"`+version+`","hosts":[{"tasks":[{"file":{"path":"/old"}}]}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Open(file); err == nil || !strings.Contains(err.Error(), "prepare a new plan") {
			t.Fatalf("old version %s: %v", version, err)
		}
	}
}
