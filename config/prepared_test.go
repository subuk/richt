package config

import (
	"os/exec"
	"testing"
)

func TestPreparedFileHelperFaultBoundaries(t *testing.T) {
	command := exec.Command("python3", "-B", "file_helper_test.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepared file transaction tests: %v\n%s", err, output)
	}
}
