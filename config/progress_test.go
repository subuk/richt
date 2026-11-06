package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/plan"
)

func TestPublishedFileResumesAfterFinalProgressWriteFailure(t *testing.T) {
	dir := directory(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("old\n"), 0640); err != nil {
		t.Fatal(err)
	}
	r := resolved(path, "prepared", false)
	r.Hosts[0].Services[0].Definition.Configs[0].Backup = true
	p := diff(t, r)
	backup := p.Hosts[0].Tasks[0].File.BackupPath
	saved := filepath.Join(dir, "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	persist := func(current *plan.Plan) error {
		if current.Hosts[0].Tasks[0].Progress.Status == "success" {
			return errors.New("injected final progress persistence failure")
		}
		return plan.Save(saved, current)
	}
	if _, err := plan.Apply(context.Background(), p, config.Local{}, persist); err == nil {
		t.Fatal("lost final progress was not reported")
	}
	old, err := os.ReadFile(backup)
	if err != nil || string(old) != "old\n" {
		t.Fatalf("recoverable old backup: %q %v", old, err)
	}
	store, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Plan.Hosts[0].Tasks[0].Progress.Status != "running" {
		t.Fatal("running checkpoint was lost")
	}
	if _, err := plan.Apply(context.Background(), store.Plan, config.Local{}, store.Save); err != nil {
		t.Fatal(err)
	}
	old, err = os.ReadFile(backup)
	if err != nil || string(old) != "old\n" || store.Plan.Hosts[0].Tasks[0].File.BackupPath != backup {
		t.Fatal("continuation replaced or renamed the old backup")
	}
}
