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
	p.Version = plan.LegacyVersion
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

func TestContinuingLegacyPlanPreservesVersionAndProgress(t *testing.T) {
	p := rpmPlan("all", "present")
	p.Version = plan.LegacyVersion
	p.Hosts[0].Tasks[0].Progress.Status = "success"
	file := filepath.Join(t.TempDir(), "legacy.json")
	if err := plan.Save(file, p); err != nil {
		t.Fatal(err)
	}
	s, err := plan.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Save(s.Plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || s.Plan.Version != plan.LegacyVersion || !strings.Contains(string(data), `"status": "success"`) || !strings.Contains(string(data), `"version": "1.5"`) {
		t.Fatalf("legacy continuation changed its contract: %s; %v", data, err)
	}
}
