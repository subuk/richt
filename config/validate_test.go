package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

const validatorSecret = "synthetic-validator-secret\nsecond-line"

// A subprocess lets the tests inspect the actual candidate without depending on
// application validators, platform-specific stat commands, or shell expansion.
func TestFileValidatorHelperProcess(t *testing.T) {
	if os.Getenv("RICHT_TEST_VALIDATOR") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	candidate, result, record, content := args[0], args[1], args[2], args[3]
	if err := os.WriteFile(record, []byte(candidate), 0600); err != nil {
		os.Exit(80)
	}
	file, err := os.Stat(candidate)
	if err != nil || file.Mode().Perm() != 0600 {
		os.Exit(81)
	}
	dir, err := os.Stat(filepath.Dir(candidate))
	if err != nil || dir.Mode().Perm() != 0700 {
		os.Exit(82)
	}
	wd, _ := os.Getwd()
	if wd != "/" {
		os.Exit(83)
	}
	bytes, err := os.ReadFile(candidate)
	if err != nil || string(bytes) != content {
		os.Exit(84)
	}
	fmt.Fprint(os.Stdout, validatorSecret)
	fmt.Fprint(os.Stderr, validatorSecret)
	switch result {
	case "failure":
		os.Exit(42)
	case "timeout":
		time.Sleep(time.Hour)
	case "mutate":
		if err := os.WriteFile(candidate, []byte("modified by validator"), 0600); err != nil {
			os.Exit(85)
		}
	}
	os.Exit(0)
}

func validatorFor(t *testing.T, result, record, content string) *plan.FileValidator {
	t.Helper()
	t.Setenv("RICHT_TEST_VALIDATOR", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &plan.FileValidator{Args: []string{exe, "-test.run=TestFileValidatorHelperProcess", "--", "{candidate}", result, record, content}, Timeout: "5s"}
}

func checkCandidateCleanup(t *testing.T, record string) {
	t.Helper()
	candidate, err := os.ReadFile(record)
	if err != nil {
		t.Fatal("validator did not run:", err)
	}
	if _, err := os.Stat(filepath.Dir(string(candidate))); !os.IsNotExist(err) {
		t.Fatalf("candidate directory was not removed: %v", err)
	}
}

func TestValidatorBeforeDestinationWrite(t *testing.T) {
	for _, targetName := range []string{"local", "target"} {
		for _, result := range []string{"success", "mutate", "failure", "missing"} {
			t.Run(targetName+"/"+result, func(t *testing.T) {
				var executor plan.Executor = Local{}
				dir := t.TempDir()
				if targetName == "target" {
					target, commands := shellTarget(t)
					// GNU timeout is exercised on Linux; this stand-in only checks
					// that the target script passes literal command arguments.
					if _, err := exec.LookPath("timeout"); err != nil {
						if err := os.WriteFile(filepath.Join(commands, "timeout"), []byte("#!/bin/sh\n[ \"$1\" = --signal=KILL ] || exit 89\nshift 2\nexec \"$@\"\n"), 0700); err != nil {
							t.Fatal(err)
						}
					}
					executor = target
				}
				path := filepath.Join(dir, "managed")
				if err := os.WriteFile(path, []byte("old bytes"), 0640); err != nil {
					t.Fatal(err)
				}
				before, _ := os.Stat(path)
				record := filepath.Join(dir, "record")
				f := &plan.FileChange{Path: path, State: "present", Write: true, Content: "frozen bytes '$HOME' $(literal)\n"}
				f.Validate = validatorFor(t, result, record, f.Content)
				if result == "missing" {
					f.Validate.Args = []string{filepath.Join(dir, "missing-validator"), "{candidate}"}
				}
				if _, err := Observe(f); err != nil {
					t.Fatal(err)
				}
				err := executor.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f})
				failed := result == "failure" || result == "missing"
				if (err != nil) != failed {
					t.Fatalf("validator result: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), validatorSecret) {
					t.Fatal("validator output escaped into diagnostics")
				}
				want := f.Content
				if failed {
					want = "old bytes"
				}
				got, _ := os.ReadFile(path)
				after, _ := os.Stat(path)
				if string(got) != want || after.Mode().Perm() != 0640 || !os.SameFile(before, after) {
					t.Fatal("destination contents, permissions, or inode changed unexpectedly")
				}
				if result != "missing" {
					checkCandidateCleanup(t, record)
				}
			})
		}
	}
}

func TestValidatorDiscardsRunnerDiagnosticsWithoutSensitiveFlag(t *testing.T) {
	f := &plan.FileChange{Path: "/unused", State: "present", Write: true, Content: validatorSecret, Validate: &plan.FileValidator{Args: []string{"/missing-validator", "{candidate}"}, Timeout: "5s"}}
	target := Target{RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte(validatorSecret), errors.New(validatorSecret)
	}}
	if err := freezeFile(f, &plan.FileState{Kind: "absent"}, nil); err != nil {
		t.Fatal(err)
	}
	err := target.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f})
	if err == nil || err.Error() != "file validator could not execute" {
		t.Fatalf("runner diagnostics were retained: %v", err)
	}
}

func TestValidatedWriteReplacesFinalSymlinkAndPreservesReferent(t *testing.T) {
	dir := t.TempDir()
	path, link := filepath.Join(dir, "managed"), filepath.Join(dir, "link")
	if err := os.WriteFile(path, []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	f := &plan.FileChange{Path: link, State: "present", AtomicWrites: true, Content: "new", Validate: validatorFor(t, "success", filepath.Join(dir, "record"), "new")}
	if _, err := Observe(f); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	entry, _ := os.Lstat(link)
	content, _ := os.ReadFile(path)
	published, _ := os.ReadFile(link)
	if !os.SameFile(before, after) || after.Mode().Perm() != 0640 || !entry.Mode().IsRegular() || string(content) != "old" || string(published) != "new" {
		t.Fatal("validated write followed the final symlink or changed its referent")
	}
}

func TestFailedValidationDoesNotRestrictSensitiveDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	if err := os.WriteFile(path, []byte("old private bytes"), 0640); err != nil {
		t.Fatal(err)
	}
	mode := uint32(0600)
	f := &plan.FileChange{Path: path, State: "present", Write: true, Sensitive: true, Mode: &mode, Content: validatorSecret}
	f.Validate = validatorFor(t, "failure", filepath.Join(dir, "record"), f.Content)
	if _, err := Observe(f); err != nil {
		t.Fatal(err)
	}
	err := (Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f})
	if err == nil || err.Error() != "sensitive file operation failed" {
		t.Fatalf("sensitive validation failure: %v", err)
	}
	content, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(content) != "old private bytes" || info.Mode().Perm() != 0640 {
		t.Fatal("sensitive write restricted permissions or replaced bytes before validation passed")
	}
	checkCandidateCleanup(t, filepath.Join(dir, "record"))
}

func TestValidatorTimeoutLeavesDestinationUnopenedAndCleansCandidate(t *testing.T) {
	for _, targetName := range []string{"local", "target"} {
		t.Run(targetName, func(t *testing.T) {
			var executor plan.Executor = Local{}
			if targetName == "target" {
				if _, err := exec.LookPath("timeout"); err != nil {
					t.Skip("GNU timeout is unavailable on this platform; Linux acceptance covers target deadlines")
				}
				target, _ := shellTarget(t)
				executor = target
			}
			dir := t.TempDir()
			record := filepath.Join(dir, "record")
			f := &plan.FileChange{Path: filepath.Join(dir, "missing"), State: "present", Write: true, Content: "prepared"}
			f.Validate = validatorFor(t, "timeout", record, f.Content)
			f.Validate.Timeout = "250ms"
			if _, err := Observe(f); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			err := executor.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f})
			if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(started) > 3*time.Second {
				t.Fatalf("deadline result: %v", err)
			}
			if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
				t.Fatal("validation timeout opened destination")
			}
			checkCandidateCleanup(t, record)
		})
	}
}

func TestValidatorOnlyForContentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	if err := os.WriteFile(path, []byte("unchanged"), 0640); err != nil {
		t.Fatal(err)
	}
	mode := uint32(0600)
	f := &plan.FileChange{Path: path, State: "present", Content: "unchanged", Mode: &mode, Validate: &plan.FileValidator{Args: []string{"/missing-validator", "{candidate}"}, Timeout: "5s"}}
	changed, err := Observe(f)
	if err != nil || !changed || f.Write {
		t.Fatalf("attribute-only observation: changed=%v error=%v", changed, err)
	}
	actions, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if err := (Local{}).Execute(context.Background(), a); err != nil {
			t.Fatal("attribute-only change invoked validator:", err)
		}
	}
	f.State, f.Content = "absent", ""
	if _, err := Observe(f); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).Execute(context.Background(), plan.Action{Kind: "remove-file", File: f}); err != nil {
		t.Fatal("removal invoked validator:", err)
	}
}

func TestValidatorPreparationAndSavedInputsAreFrozen(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("frozen"), 0600); err != nil {
		t.Fatal(err)
	}
	v := validatorFor(t, "success", filepath.Join(dir, "record"), "frozen")
	c := inventory.Config{Path: filepath.Join(dir, "target"), Template: inventory.Template{File: source}, Validate: &inventory.Validator{Args: v.Args}}
	f, err := Prepare(c, "present", nil)
	if err != nil || f.Validate.Timeout != "5s" {
		t.Fatalf("prepare validator: %v", err)
	}
	c.Validate.Args[0] = "/changed-inventory"
	if _, err := Observe(f); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	var saved plan.FileChange
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: &saved}); err != nil {
		t.Fatal("saved inputs changed or source was reopened:", err)
	}
	checkCandidateCleanup(t, filepath.Join(dir, "record"))
}

func TestValidatorRejectsMalformedPreparedInputs(t *testing.T) {
	for _, v := range []*plan.FileValidator{
		{Args: []string{"relative", "{candidate}"}, Timeout: "5s"},
		{Args: []string{"/bin/true"}, Timeout: "5s"},
		{Args: []string{"/bin/true", "prefix{candidate}"}, Timeout: "5s"},
		{Args: []string{"/bin/true", "{candidate}", "{candidate}"}, Timeout: "5s"},
		{Args: []string{"/bin/true", "{candidate}", "nul\x00arg"}, Timeout: "5s"},
		{Args: []string{"/bin/true", "{candidate}"}, Timeout: "0s"},
		{Args: []string{"/bin/true", "{candidate}"}},
	} {
		f := &plan.FileChange{Path: "/somewhere", State: "present", Write: true, Validate: v}
		if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err == nil {
			t.Fatalf("malformed validator accepted: %#v", v)
		}
	}
}
