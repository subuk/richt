package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInventory(t *testing.T) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "inventory.yaml")
	text := fmt.Sprintf("services:\n  example:\n    configs:\n      main:\n        path: %s\n        template:\n          inline: 'example content'\nhosts:\n  - name: local\n    transport:\n      type: local\n    services:\n      - name: example\n", target)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path, target
}
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Run(args, &out, &err)
	return code, out.String(), err.String()
}

func TestCLIInventoryAndSavedApply(t *testing.T) {
	inventory, target := testInventory(t)
	for _, sub := range []string{"validate", "show"} {
		code, _, err := run(t, "inventory", sub, "-i", inventory)
		if code != 0 {
			t.Fatal(err)
		}
	}
	saved := filepath.Join(filepath.Dir(inventory), "plan.json")
	code, out, err := run(t, "diff", "-i", inventory, "-o", saved)
	if code != 0 {
		t.Fatal(err)
	}
	if !strings.Contains(out, "overwrite-file") {
		t.Fatal(out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("diff changed target")
	}
	code, _, err = run(t, "apply", "-y", saved, "--host", "local")
	if code == 0 || !strings.Contains(err, "filters cannot") {
		t.Fatalf("filters accepted: %d %s", code, err)
	}
	os.Remove(inventory)
	code, out, err = run(t, "apply", "-y", saved)
	if code != 0 {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Result:") {
		t.Fatal(out)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "example content" {
		t.Fatal(string(content))
	}
	code, out, err = run(t, "apply", "-y", saved)
	if code != 0 || !strings.Contains(out, "previously completed") {
		t.Fatalf("resume: %d %s %s", code, out, err)
	}
}
func TestNoTerminalRequiresYesAndNoAutosave(t *testing.T) {
	inventory, target := testInventory(t)
	pipe, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	write.Close()
	var out bytes.Buffer
	f := arguments{}
	f.Apply.Inventory = inventory
	if err := execute("apply", f, pipe, &out); err == nil || !strings.Contains(err.Error(), "use -y") {
		t.Fatalf("expected no TTY rejection: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("apply without confirmation changed resource")
	}
	f.Apply.Yes = true
	out.Reset()
	if err := execute("apply", f, pipe, &out); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(inventory))
	if len(entries) != 2 {
		t.Fatalf("unexpected autosave: %v", entries)
	}
}
func TestCLIHelpVersionAndBadArguments(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"apply", "--help"}} {
		if code, _, err := run(t, args...); code != 0 {
			t.Fatal(err)
		}
	}
	if code, _, _ := run(t, "diff", "--unknown"); code == 0 {
		t.Fatal("accepted unknown flag")
	}
}
