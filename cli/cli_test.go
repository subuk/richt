package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

func testInventory(t *testing.T) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "inventory.yaml")
	text := fmt.Sprintf("tags:\n  example:\n    services: {example: {}}\nservices:\n  example:\n    order: 10\n    configs:\n      main:\n        path: %s\n        template:\n          inline: 'example content'\nhosts:\n  - name: local\n    tags: [example]\n    transport:\n      type: local\n", target)
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
	code, out, err = run(t, "apply", "-y", "--parallelism", "1", saved)
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
	code, out, err = run(t, "apply", "-y", "--parallelism", "1", saved)
	if code != 0 || !strings.Contains(out, "previously completed") {
		t.Fatalf("resume: %d %s %s", code, out, err)
	}
}

func TestCLITagSelectorsAndFrozenOrderedContinuation(t *testing.T) {
	dir := t.TempDir()
	first, blocked, last := filepath.Join(dir, "first"), filepath.Join(dir, "blocked"), filepath.Join(dir, "last")
	filename, saved := filepath.Join(dir, "inventory.yaml"), filepath.Join(dir, "plan.json")
	source := fmt.Sprintf(`tags:
  shared:
    services: {last: {}, first: {}}
  worker:
    services: {second: {}}
services:
  last:
    order: 30
    configs:
      main: {path: %q, template: {inline: last}}
  second:
    order: 20
    configs:
      main: {path: %q, template: {inline: second}}
  first:
    order: 10
    configs:
      main: {path: %q, template: {inline: first}}
hosts:
  - {name: a, tags: [worker, shared], transport: {type: local}}
  - {name: b, tags: [shared], transport: {type: local}}
`, last, filepath.Join(blocked, "second"), first)
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "inventory", "show", "-i", filename, "--host", "a", "--tag", "shared", "--service", "second")
	var selected inventory.Resolved
	if code != 0 || json.Unmarshal([]byte(stdout), &selected) != nil {
		t.Fatalf("selected inventory: %d %s %s", code, stdout, stderr)
	}
	if len(selected.Hosts) != 2 || len(selected.Hosts[0].Services) != 1 || selected.Hosts[0].Services[0].Name != "second" || len(selected.Hosts[1].Services) != 0 {
		t.Fatalf("host/tag union and service intersection: %+v", selected.Hosts)
	}
	// Service selection keeps only inherited assignments and adds no prerequisites.
	code, _, stderr = run(t, "diff", "-i", filename, "--host", "a", "--service", "second", "-o", saved)
	if code != 0 {
		t.Fatal(stderr)
	}
	readPlan := func() *plan.Plan {
		t.Helper()
		store, err := plan.Open(saved)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		return store.Plan
	}
	if tasks := readPlan().Hosts[0].Tasks; len(tasks) != 1 || tasks[0].Service != "second" {
		t.Fatalf("service filter added work: %+v", tasks)
	}
	code, _, stderr = run(t, "diff", "-i", filename, "--host", "a", "-o", saved)
	if code != 0 {
		t.Fatal(stderr)
	}
	tasks := readPlan().Hosts[0].Tasks
	var services []string
	for _, task := range tasks {
		services = append(services, task.Service)
	}
	if strings.Join(services, ",") != "first,second,last" {
		t.Fatalf("inherited services must occur once in definition order: %v", services)
	}
	if err := os.WriteFile(blocked, []byte("deliberate obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	code, _, _ = run(t, "apply", "-y", saved)
	tasks = readPlan().Hosts[0].Tasks
	if code == 0 || tasks[0].Progress.Status != "success" || tasks[1].Progress.Status != "error" || tasks[2].Progress.Status != "pending" {
		t.Fatalf("ordered failure did not block later work: code %d tasks %+v", code, tasks)
	}
	if err := os.WriteFile(first, []byte("external change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run(t, "apply", "-y", saved)
	if code != 0 || !strings.Contains(stdout, "previously completed") {
		t.Fatalf("frozen continuation: %d %s %s", code, stdout, stderr)
	}
	for path, want := range map[string]string{first: "external change", filepath.Join(blocked, "second"): "second", last: "last"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("continued target %s: %q, %v", path, got, err)
		}
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
	f.Apply.Parallelism = 20
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

func TestParallelismValidation(t *testing.T) {
	for _, n := range []string{"0", "-1"} {
		code, _, err := run(t, "apply", "-y", "--parallelism="+n, "missing-plan.json")
		if code == 0 || !strings.Contains(err, "parallelism must be positive") {
			t.Fatal(code, err)
		}
	}
}

func TestCLISecretPlansFreezeInputsAndProtectOutput(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "managed")
	target := filepath.Join(parent, "config")
	defaultValue, alternateValue := "synthetic-default-marker\nsecond-line", "synthetic-alternate-marker"
	defaultSource, alternateSource := filepath.Join(dir, "default.yaml"), filepath.Join(dir, "alternate.yaml")
	for path, value := range map[string]string{defaultSource: defaultValue, alternateSource: alternateValue} {
		if err := os.WriteFile(path, []byte("token: "+strconv.Quote(value)+"\n"), 0600); err != nil {
			t.Fatal("could not write synthetic source")
		}
	}
	inventoryPath, saved := filepath.Join(dir, "inventory.yaml"), filepath.Join(dir, "plan.json")
	text := fmt.Sprintf(`secret_sources:
  default: {type: yaml, file: default.yaml}
  alternate: {type: yaml, file: alternate.yaml}
tags:
  example:
    services: {example: {}}
services:
  example:
    order: 10
    configs:
      main:
        path: %s
        sensitive: false
        template:
          inline: '{{ secret "token" }}|{{ secret "token" "alternate" }}|{{ printf "%%q" (secret "token") }}|{{ printf "%%x" (secret "token") }}'
hosts:
  - name: local
    tags: [example]
    transport: {type: local}
`, target)
	if err := os.WriteFile(inventoryPath, []byte(text), 0600); err != nil {
		t.Fatal("could not write synthetic inventory")
	}
	assertSafe := func(stdout, stderr string) {
		t.Helper()
		for _, marker := range []string{"synthetic-default-marker", "synthetic-alternate-marker", "second-line", fmt.Sprintf("%x", defaultValue)} {
			if strings.Contains(stdout+stderr, marker) {
				t.Fatal("CLI exposed a synthetic confidential value")
			}
		}
	}
	code, stdout, stderr := run(t, "diff", "-i", inventoryPath, "-o", saved)
	assertSafe(stdout, stderr)
	if code != 0 || !strings.Contains(stdout, "Content hidden: sensitive") {
		t.Fatal("secret diff did not produce a sensitive saved plan")
	}
	s, err := plan.Open(saved)
	if err != nil {
		t.Fatal("could not inspect saved plan")
	}
	want := defaultValue + "|" + alternateValue + "|" + strconv.Quote(defaultValue) + "|" + fmt.Sprintf("%x", defaultValue)
	f := s.Plan.Hosts[0].Tasks[0].File
	if !f.Sensitive || f.Content != want || !strings.Contains(f.Patch, defaultValue[:len("synthetic-default-marker")]) {
		s.Close()
		t.Fatal("plan did not retain exact prepared secret bytes and sensitivity")
	}
	s.Close()
	for _, path := range []string{saved, saved + ".lock"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("saved plan or lock permissions are not restrictive")
		}
	}
	for _, path := range []string{inventoryPath, defaultSource, alternateSource} {
		if err := os.Remove(path); err != nil {
			t.Fatal("could not remove preparation source")
		}
	}
	// Fail the frozen task, then repair the filesystem and explicitly continue.
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal("could not install failure fixture")
	}
	code, stdout, stderr = run(t, "apply", "-y", saved)
	assertSafe(stdout, stderr)
	if code == 0 || !strings.Contains(stdout+stderr, "sensitive file operation failed") {
		t.Fatal("expected a protected sensitive apply failure")
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal("could not remove failure fixture")
	}
	code, stdout, stderr = run(t, "apply", "-y", saved)
	assertSafe(stdout, stderr)
	if code != 0 {
		t.Fatal("frozen saved plan could not continue without secret sources")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != want {
		t.Fatal("saved apply did not write frozen prepared bytes")
	}
	code, stdout, stderr = run(t, "apply", "-y", saved)
	assertSafe(stdout, stderr)
	if code != 0 || !strings.Contains(stdout, "previously completed") {
		t.Fatal("saved secret task did not preserve successful progress")
	}
	// Explicit sensitivity still hides the old secret when the new template
	// removes every secret call and no source exists anymore.
	text = strings.Replace(text, "sensitive: false", "sensitive: true", 1)
	start := strings.Index(text, "          inline:")
	end := start + strings.Index(text[start:], "\nhosts:")
	text = text[:start] + "          inline: 'public replacement'" + text[end:]
	if err := os.WriteFile(inventoryPath, []byte(text), 0600); err != nil {
		t.Fatal("could not write replacement inventory")
	}
	code, stdout, stderr = run(t, "apply", "-y", "-i", inventoryPath)
	assertSafe(stdout, stderr)
	if code != 0 || !strings.Contains(stdout, "Content hidden: sensitive") {
		t.Fatal("explicit sensitive replacement failed without sources")
	}
}

func TestCLIMalformedPlanDoesNotExposeSecretFields(t *testing.T) {
	marker := "synthetic-confidential-marker"
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"version":"1.1","hosts":[],"`+marker+`":true}`), 0600); err != nil {
		t.Fatal("could not write malformed fixture")
	}
	code, stdout, stderr := run(t, "apply", "-y", path)
	if code == 0 || strings.Contains(stdout+stderr, marker) {
		t.Fatal("malformed plan CLI diagnostic was not protected")
	}
}

func TestCLISecretPreparationErrorsDoNotExposeValues(t *testing.T) {
	dir := t.TempDir()
	marker := "synthetic-confidential-marker\nsecond-line"
	if err := os.WriteFile(filepath.Join(dir, "source.yaml"), []byte("token: "+strconv.Quote(marker)+"\n"), 0600); err != nil {
		t.Fatal("could not write synthetic source")
	}
	path := filepath.Join(dir, "inventory.yaml")
	for _, source := range []string{
		`{{printf "%x" (secret "token")}}{{call (secret "token")}}`,
		`{{secret "missing-key"}}`,
		`{{secret "token" "missing-source"}}`,
	} {
		text := fmt.Sprintf(`secret_sources:
  default: {type: yaml, file: source.yaml}
tags:
  example:
    services: {example: {}}
services:
  example:
    order: 10
    configs:
      main:
        path: %s
        template:
          inline: '%s'
hosts:
  - name: local
    tags: [example]
    transport: {type: local}
`, filepath.Join(dir, "target"), source)
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal("could not write synthetic inventory")
		}
		code, stdout, stderr := run(t, "diff", "-i", path)
		if code == 0 {
			t.Fatal("invalid secret preparation succeeded")
		}
		for _, value := range []string{"synthetic-confidential-marker", "second-line", fmt.Sprintf("%x", marker)} {
			if strings.Contains(stdout+stderr, value) {
				t.Fatal("preparation error exposed a confidential value or transformation")
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "target")); !os.IsNotExist(err) {
			t.Fatal("failed preparation changed the target")
		}
	}
}
