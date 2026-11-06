package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/systemd"
)

func TestSocketReloadDeclarationsFailPreparation(t *testing.T) {
	const content = "[Service]\nExecReload=/bin/true\nType=notify-reload\n"
	for _, form := range []string{"external", "unit_file", "override"} {
		for _, state := range []string{"present", "absent"} {
			t.Run(form+"/"+state, func(t *testing.T) {
				process := inventory.Process{Unit: "richt-test.socket", ReloadOn: []string{"config.main"}, RestartOn: []string{"config.main"}}
				unit := inventory.Config{Path: "/etc/systemd/system/richt-test.socket", Template: inventory.Template{Inline: inline(content)}}
				switch form {
				case "unit_file":
					process.UnitFile = &unit
				case "override":
					unit.Path += ".d/20-reload.conf"
					process.Overrides = map[string]inventory.Config{"20-reload.conf": unit}
				}
				s := inventory.Service{
					Configs:   []inventory.Config{{Name: "main", Path: "/etc/richt-test.conf", Template: inventory.Template{Inline: inline(content)}}},
					Processes: []inventory.Process{process},
				}
				target := config.Target{
					Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
					RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
						return fileObservation("regular", content), nil
					},
				}
				e := Local{Files: &target, Systemd: systemd.Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if args[0] != "show" {
						t.Fatal("preparation mutated a unit", args)
					}
					return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nCanReload=yes\n"), nil
				}}}
				r := resolved(s)
				r.Hosts[0].Services[0].State = state
				p, err := e.Diff(context.Background(), r)
				if state == "present" {
					if err == nil || !strings.Contains(err.Error(), "socket units do not support reload_on; use restart_on") || strings.Contains(err.Error(), "ExecReload") {
						t.Fatal("socket reload declaration accepted or misleading diagnostic", err)
					}
					return
				}
				if err != nil {
					t.Fatal("absent socket validated ignored reload declaration", err)
				}
				for _, task := range p.Hosts[0].Tasks {
					if task.Process != nil && (task.Process.Reaction != "" || task.Process.State != "stopped") {
						t.Fatal("absent socket retained reaction", task)
					}
				}
			})
		}
	}
}
