package plan_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/plan"
)

func TestNewPlanFeaturesRequireVersion12(t *testing.T) {
	no := false
	tasks := []plan.Task{
		{Operation: "file", File: &plan.FileChange{Validate: &plan.FileValidator{Args: []string{"/bin/check", "{candidate}"}, Timeout: "5s"}}},
		{Operation: "systemd-mask", Process: &plan.ProcessChange{Masked: &no}},
		{Operation: "systemd", Process: &plan.ProcessChange{Masked: &no}},
		{Operation: "rpm", RPM: &plan.RPMChange{Version: "1.34.3"}},
	}
	for _, task := range tasks {
		task.ID = "task"
		task.Progress.Status = "pending"
		for _, version := range []string{"1.0", "1.1", plan.Version} {
			p := &plan.Plan{Version: version, Hosts: []plan.Host{{Name: "host", Transport: "local", Tasks: []plan.Task{task}}}}
			err := p.Validate()
			if (err != nil) != (version != plan.Version) {
				t.Fatalf("version %s task %+v: %v", version, task, err)
			}
		}
	}
}

func TestLegacyPlanRoundTrip(t *testing.T) {
	for _, version := range []string{"1.0", "1.1"} {
		p := &plan.Plan{Version: version, Hosts: []plan.Host{{Name: "host", Transport: "local", Tasks: []plan.Task{{ID: "rpm", Operation: "rpm", RPM: &plan.RPMChange{Manager: "dnf", Package: "demo", Version: "0:1.2-1.noarch", State: "present"}, Progress: plan.Progress{Status: "success"}}}}}}
		filename := filepath.Join(t.TempDir(), "plan.json")
		if err := plan.Save(filename, p); err != nil {
			t.Fatal(err)
		}
		s, err := plan.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if s.Plan.Version != version || s.Plan.Hosts[0].Tasks[0].Progress.Status != "success" {
			t.Fatal("legacy data changed")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidatorPlanDisplay(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		f := &plan.FileChange{Path: "/etc/test.conf", State: "present", Write: true, Sensitive: sensitive, Content: "PRIVATE_CONTENT", Validate: &plan.FileValidator{Args: []string{"/bin/validator", "ARGUMENT_MARKER", "{candidate}"}, Timeout: "3s"}}
		p := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "host", Transport: "local", Tasks: []plan.Task{{ID: "file", Operation: "file", File: f, Progress: plan.Progress{Status: "pending"}}}}}}
		var out bytes.Buffer
		if err := plan.Display(&out, p, config.Local{}); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		if !strings.Contains(s, "validate candidate before write") || !strings.Contains(s, "3s") {
			t.Fatal(s)
		}
		if sensitive && (strings.Contains(s, "ARGUMENT_MARKER") || strings.Contains(s, "PRIVATE_CONTENT")) {
			t.Fatalf("sensitive detail leaked: %s", s)
		}
		if !sensitive && !strings.Contains(s, "{candidate}") {
			t.Fatal(s)
		}
	}
}
