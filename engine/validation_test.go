package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

func TestValidationRunsAfterPackageAndBlocksReactionUntilContinuation(t *testing.T) {
	dir := tempDir(t)
	validator := filepath.Join(dir, "package-supplied-validator")
	guard, calls := filepath.Join(dir, "allow-validation"), filepath.Join(dir, "validation-calls")
	path, source := filepath.Join(dir, "config"), filepath.Join(dir, "template")
	if err := os.WriteFile(source, []byte("frozen prepared content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old content"), 0640); err != nil {
		t.Fatal(err)
	}
	s := inventory.Service{
		Binaries: map[string]inventory.Binary{"package": {Type: "rpm", Package: "example"}},
		Configs: map[string]inventory.Config{"main": {Path: path, Template: inventory.Template{File: source}, Validate: &inventory.Validator{
			Args: []string{validator, "{candidate}", guard, calls}, Timeout: "5s",
		}}},
		Processes: map[string]inventory.Process{"main": {Unit: "example.service", ReloadOn: []string{"config.main"}}},
	}
	transactions, reloads := 0, 0
	e := Local{
		RPM: rpm.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
			if command == "rpm" {
				return nil, rpm.ErrNotInstalled
			}
			if args[0] == "--version" {
				return []byte("4.20.0"), nil
			}
			transactions++
			// The checker is absent during preparation and appears only after
			// its prerequisite package task has completed.
			err := os.WriteFile(validator, []byte("#!/bin/sh\nprintf 'validated\\n' >> \"$3\"\nprintf 'synthetic-secret-diagnostic' >&2\n[ -f \"$1\" ] && [ -e \"$2\" ]\n"), 0700)
			return nil, err
		}},
		Systemd: systemd.Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "show" {
				return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=disabled\nCanReload=yes\n"), nil
			}
			if args[0] == "reload" {
				reloads++
				if reloads == 1 {
					return nil, errors.New("simulated reload failure")
				}
			}
			return nil, nil
		}},
	}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal("diff checked a validator not installed yet:", err)
	}
	if len(p.Hosts[0].Tasks) != 4 || p.Hosts[0].Tasks[0].Operation != "rpm" || p.Hosts[0].Tasks[1].Operation != "file" || p.Hosts[0].Tasks[3].Operation != "systemd" {
		t.Fatal("package, validation/write, reaction order changed")
	}
	saved := filepath.Join(dir, "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p = store.Plan
	if _, err := plan.Apply(context.Background(), p, e, store.Save); err == nil || strings.Contains(err.Error(), "synthetic-secret-diagnostic") {
		t.Fatalf("expected safe validation failure: %v", err)
	}
	if transactions != 1 || reloads != 0 || p.Hosts[0].Tasks[1].Progress.Status != "error" || p.Hosts[0].Tasks[3].Progress.Status != "pending" {
		t.Fatal("validation failure did not block process reaction")
	}
	bytes, _ := os.ReadFile(path)
	if string(bytes) != "old content" {
		t.Fatal("failed validation changed destination")
	}
	if err := os.WriteFile(guard, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), p, e, store.Save); err == nil {
		t.Fatal("expected subsequent simulated reload failure")
	}
	bytes, _ = os.ReadFile(path)
	if transactions != 1 || string(bytes) != "frozen prepared content" || p.Hosts[0].Tasks[1].Progress.Status != "success" {
		t.Fatal("continuation did not skip package and validate/write frozen inputs")
	}
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), p, e, store.Save); err != nil {
		t.Fatal("completed config was revalidated:", err)
	}
	bytes, _ = os.ReadFile(calls)
	if transactions != 1 || reloads != 2 || string(bytes) != "validated\nvalidated\n" {
		t.Fatal("continuation replayed successful tasks or skipped unfinished validation")
	}
}
