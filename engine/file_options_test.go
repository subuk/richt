package engine

import (
	"context"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
)

func TestOwnedUnitAndDropInRetainPreparedFileSafetyOptions(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			no := false
			s := inventory.Service{Processes: []inventory.Process{{
				Unit: "richt-prepared-test." + suffix, State: "stopped",
				UnitFile:  &inventory.Config{Path: "/etc/systemd/system/richt-prepared-test." + suffix, AtomicWrites: &no, Backup: true, Template: inventory.Template{Inline: inline("[Service]\nExecStart=/bin/true\n")}},
				Overrides: map[string]inventory.Config{"20-test.conf": {Path: "/etc/systemd/system/richt-prepared-test." + suffix + ".d/20-test.conf", Backup: true, Template: inventory.Template{Inline: inline("[Service]\nEnvironment=TEST=1\n")}}},
			}}}
			target := config.Target{
				Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
				RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
					return fileObservation("regular", "old unit bytes\n"), nil
				},
			}
			e := Local{Files: &target, Systemd: fakeSystemd("inactive", false)}
			for _, state := range []string{"present", "absent"} {
				r := resolved(s)
				r.Hosts[0].Services[0].State = state
				p, err := e.Diff(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				files := 0
				for _, task := range p.Hosts[0].Tasks {
					f := task.File
					if f == nil {
						continue
					}
					files++
					if f.State != state || !f.Backup || f.BackupPath == "" || f.Expected.Kind != "regular" {
						t.Fatalf("owned file lost prepared safety settings: %#v", f)
					}
					isUnit := f.Path == "/etc/systemd/system/richt-prepared-test."+suffix
					if f.AtomicWrites == isUnit {
						t.Fatal("owned unit explicit opt-out or drop-in atomic default was lost")
					}
				}
				if files != 2 {
					t.Fatalf("expected unit and drop-in, got %d", files)
				}
			}
		})
	}
}
