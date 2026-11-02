package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

func TestSavedUpstreamRPMRequestResolvesOnlyAtApply(t *testing.T) {
	for _, installed := range []string{"", "0:1.34.2-1.x86_64", "0:1.35.0-1.x86_64"} {
		t.Run("installed="+installed, func(t *testing.T) {
			service := inventory.Service{Binaries: map[string]inventory.Binary{
				"app": {Type: "rpm", Package: "demo", Version: "1.34.3"},
			}}
			diffCalls := 0
			e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				diffCalls++
				if name == "dnf" && reflect.DeepEqual(args, []string{"--version"}) {
					return []byte("4.20.0\n"), nil
				}
				if name == "rpm" && len(args) > 0 && args[0] == "-qa" {
					if installed == "" {
						return nil, nil
					}
					return []byte("demo\t" + installed + "\n"), nil
				}
				return nil, fmt.Errorf("preparation contacted repositories: %s %q", name, args)
			}}}
			p, err := e.Diff(context.Background(), resolved(service))
			if err != nil {
				t.Fatal(err)
			}
			if diffCalls != 2 || len(p.Hosts[0].Tasks) != 1 || p.Hosts[0].Tasks[0].RPM.Version != "1.34.3" {
				t.Fatalf("calls=%d plan=%+v", diffCalls, p)
			}
			path := filepath.Join(tempDir(t), "upstream.json")
			if err = plan.Save(path, p); err != nil {
				t.Fatal(err)
			}
			// Applying the saved request must not inspect this now-invalid source.
			service.Binaries["app"] = inventory.Binary{Type: "rpm", Package: "changed", Version: "invalid-*"}
			store, err := plan.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			applyCalls := 0
			e.RPM.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				applyCalls++
				if name != "dnf" || !reflect.DeepEqual(args, []string{"-y", "install", "--", "demo-1.34.3"}) {
					t.Fatalf("apply lost saved short request: %s %q", name, args)
				}
				if applyCalls != 1 {
					t.Fatal("completed RPM task was executed again")
				}
				return nil, nil
			}
			if _, err = plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil {
				t.Fatal(err)
			}
			if applyCalls != 1 || store.Plan.Hosts[0].Tasks[0].RPM.Version != "1.34.3" || store.Plan.Hosts[0].Tasks[0].Progress.Status != "success" {
				t.Fatalf("applyCalls=%d task=%+v", applyCalls, store.Plan.Hosts[0].Tasks[0])
			}
			if _, err = plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamRPMInstalledMatchPreparesNoTask(t *testing.T) {
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dnf" && reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("4.20.0\n"), nil
		}
		if name == "rpm" && len(args) > 0 && args[0] == "-qa" {
			return []byte("demo\t7:1.34.3-42.aarch64\n"), nil
		}
		return nil, fmt.Errorf("unexpected preparation command: %s %q", name, args)
	}}}
	p, err := e.Diff(context.Background(), resolved(inventory.Service{Binaries: map[string]inventory.Binary{
		"app": {Type: "rpm", Package: "demo", Version: "1.34.3"},
	}}))
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatalf("plan=%+v error=%v", p, err)
	}
}
