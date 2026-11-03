package plan_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/plan"
)

func TestSupportedPlanVersionsAreAccepted(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.1", "1.2", "1.3", "999", plan.LegacyVersion, plan.Version} {
		p := &plan.Plan{Version: version, Hosts: []plan.Host{}}
		err := p.Validate()
		if (err != nil) != (version != plan.Version && version != plan.LegacyVersion) {
			t.Fatalf("version %q: %v", version, err)
		}
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, []byte(`{"version":"`+version+`","hosts":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		store, err := plan.Open(path)
		if (err != nil) != (version != plan.Version && version != plan.LegacyVersion) {
			t.Fatalf("open version %q: %v", version, err)
		}
		if store != nil {
			store.Close()
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
