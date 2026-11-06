package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/artifact"
	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/mount"
	"github.com/subuk/richt/oneshot"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
	"github.com/subuk/richt/systemd"
)

func pathFake() mount.Backend {
	return mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "findmnt" {
			return []byte(`{"filesystems":[]}`), nil
		}
		if command != "readlink" || len(args) == 0 {
			return nil, errors.New("unexpected target path command")
		}
		return []byte(args[len(args)-1] + "\n"), nil
	}}
}

func artifactBinary(name string) inventory.Binary {
	return inventory.Binary{Name: name, Type: "download", Path: "/opt/" + name, URL: "https://example.com/" + name, SHA256: strings.Repeat("a", 64)}
}

func artifactFake(t *testing.T, changed bool, execute func(*plan.ArtifactChange) error) artifact.Backend {
	t.Helper()
	return artifact.Backend{RunInput: func(_ context.Context, input []byte, command string, args ...string) ([]byte, error) {
		if command != "python3" || len(args) != 3 || args[0] != "-I" || args[1] != "-c" {
			t.Fatal("unexpected helper invocation")
		}
		var request struct {
			Op     string               `json:"op"`
			Change *plan.ArtifactChange `json:"change"`
		}
		if err := json.Unmarshal(input, &request); err != nil {
			t.Fatal(err)
		}
		switch request.Op {
		case "check":
			return []byte(`{}`), nil
		case "observe":
			return json.Marshal(map[string]any{"changed": changed})
		case "execute":
			if execute != nil {
				if err := execute(request.Change); err != nil {
					return nil, err
				}
			}
			return []byte(`{}`), nil
		default:
			t.Fatalf("unexpected helper operation %s", request.Op)
			return nil, nil
		}
	}}
}

func TestArtifactReactionsDoNotImplyPackageUnitDiscovery(t *testing.T) {
	s := inventory.Service{Binaries: []inventory.Binary{artifactBinary("app")}, Processes: []inventory.Process{{Type: "systemd", Unit: "app.service", RestartOn: []string{"binary.app"}}}}
	e := Local{Mount: pathFake(), Artifacts: artifactFake(t, true, nil), Systemd: fakeSystemd("active", true), RPM: rpm.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("artifact requires no RPM backend")
		return nil, nil
	}}}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(taskIDs(p), []string{"app/binary/0", "app/process/0"}) || p.Hosts[0].Tasks[1].Process.Reaction != "restart" {
		t.Fatalf("unexpected reaction or daemon reload: %+v", p.Hosts[0].Tasks)
	}
	e.Systemd = systemd.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=disabled\nCanReload=no\n"), nil
	}}
	if _, err := e.Diff(context.Background(), resolved(s)); err == nil || !strings.Contains(err.Error(), "no package installation") {
		t.Fatalf("artifact cannot supply an undeclared systemd unit: %v", err)
	}
}

func TestArtifactSavedInputsAndContinuationKeepReactionPending(t *testing.T) {
	s := inventory.Service{Binaries: []inventory.Binary{artifactBinary("app")}, Processes: []inventory.Process{oneshotProcess("notify", "binary.app")}}
	attempts, reactions := 0, 0
	e := Local{Mount: pathFake(), Artifacts: artifactFake(t, true, func(c *plan.ArtifactChange) error {
		attempts++
		if c.URL != "https://example.com/app" || c.SHA256 != strings.Repeat("a", 64) {
			t.Fatal("saved artifact input changed")
		}
		if attempts == 1 {
			return errors.New("simulated transport failure")
		}
		return nil
	}), Oneshot: oneshot.Backend{Run: func(context.Context, string, ...string) error { reactions++; return nil }}}
	r := resolved(s)
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Hosts[0].Services[0].Definition.Binaries[0].URL = "https://example.com/replaced"
	saved := filepath.Join(t.TempDir(), "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var display bytes.Buffer
	if err := plan.Display(&display, store.Plan, e); err != nil || !strings.Contains(display.String(), "SHA256") {
		t.Fatal(display.String(), err)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err == nil || reactions != 0 {
		t.Fatal("failure did not block reaction", err, reactions)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil || reactions != 1 {
		t.Fatal(err, reactions)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, e, store.Save); err != nil || attempts != 2 || reactions != 1 {
		t.Fatal("successful artifact/reaction replayed", err, attempts, reactions)
	}
}

func TestArtifactNoopAndReverseAbsence(t *testing.T) {
	s := inventory.Service{Binaries: []inventory.Binary{artifactBinary("first"), artifactBinary("second")}, Processes: []inventory.Process{oneshotProcess("notify", "binary.first", "binary.second")}}
	e := Local{Mount: pathFake(), Artifacts: artifactFake(t, false, nil)}
	p, err := e.Diff(context.Background(), resolved(s))
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal("unchanged artifact scheduled reaction", p, err)
	}
	r := resolved(s)
	r.Hosts[0].Services[0].State = "absent"
	e.Artifacts = artifactFake(t, true, nil)
	p, err = e.Diff(context.Background(), r)
	if err != nil || !reflect.DeepEqual(taskIDs(p), []string{"app/binary/1", "app/binary/0"}) {
		t.Fatal("absence did not reverse binary order or skipped oneshot", p, err)
	}
}

func TestArtifactRejectsConfigAliasIntoOwnedTree(t *testing.T) {
	b := artifactBinary("app")
	b.Type, b.Format = "archive", "tar.gz"
	s := inventory.Service{Binaries: []inventory.Binary{b}, Configs: []inventory.Config{{Path: "/alias/config", Template: inventory.Template{Inline: inline("content")}}}}
	e := Local{Artifacts: artifactFake(t, true, nil), Mount: mount.Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "findmnt" {
			return []byte(`{"filesystems":[]}`), nil
		}
		if name != "readlink" {
			t.Fatal(name)
		}
		p := args[len(args)-1]
		if p == "/alias/config" {
			p = "/opt/app/config"
		}
		return []byte(p + "\n"), nil
	}}}
	if _, err := e.Diff(context.Background(), resolved(s)); err == nil || !strings.Contains(err.Error(), "resource owned") {
		t.Fatalf("config alias allowed into archive: %v", err)
	}
}

func TestArtifactAliasesAcrossActiveMountAndFstab(t *testing.T) {
	for _, fstabAlias := range []bool{false, true} {
		b := artifactBinary("app")
		b.Type, b.Format = "archive", "tar.gz"
		if !fstabAlias {
			b.Path = "/srv/data/app"
		}
		services := []preparedService{{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "app", State: "present"}}, binaries: []preparedBinary{{name: "app", artifact: &plan.ArtifactChange{Path: b.Path, Type: b.Type}}}, files: []preparedFile{
			{component: "config/mount", definition: &inventory.Config{Type: "mount", Target: "/srv/data", Source: "/dev/example"}},
			{component: "config/file", file: &plan.FileChange{Path: "/srv/data/alias/file"}},
		}}}
		e := Local{Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
			if command == "findmnt" {
				return []byte(`{"filesystems":[{"target":"/srv/data"}]}`), nil
			}
			p := args[len(args)-1]
			if fstabAlias && p == "/etc/fstab" {
				p = "/opt/app/fstab"
			} else if !fstabAlias && p == "/srv/data/alias/file" {
				p = "/srv/data/app/file"
			}
			return []byte(p + "\n"), nil
		}}}
		if err := e.checkArtifactPaths(context.Background(), services); err == nil || !strings.Contains(err.Error(), "resource owned") {
			t.Fatalf("fstab alias=%t: conflicting target alias was accepted: %v", fstabAlias, err)
		}
	}
}

func TestArtifactRequiresMountedFilesystemBeforePreparation(t *testing.T) {
	services := []preparedService{{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "app", State: "present"}}, binaries: []preparedBinary{{artifact: &plan.ArtifactChange{Path: "/srv/data/app", Type: "download"}}}, files: []preparedFile{
		{definition: &inventory.Config{Type: "mount", Target: "/srv/data", Source: "/dev/example"}},
	}}}
	e := Local{Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "findmnt" {
			return []byte(`{"filesystems":[]}`), nil
		}
		return []byte(args[len(args)-1] + "\n"), nil
	}}}
	if err := e.checkArtifactPaths(context.Background(), services); err == nil || !strings.Contains(err.Error(), "apply the mount separately") {
		t.Fatalf("unmounted contents accepted: %v", err)
	}
}

func TestArtifactBindMountAliasConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, artifactPath, configPath, fsroot, device string
		wantConflict                                   bool
	}{
		{"config-via-bind", "/opt/app", "/mnt/alias/app/config", "/opt", "8:1", true},
		{"artifact-via-bind", "/mnt/alias/app", "/opt/app/config", "/opt", "8:1", true},
		{"sidecar-via-bind", "/opt/app", "/mnt/alias/" + filepath.Base(inventory.ArtifactMetadataPath("/opt/app")) + "/manifest.json", "/opt", "8:1", true},
		{"different-device", "/opt/app", "/mnt/alias/app/config", "/opt", "8:2", false},
		{"different-root", "/opt/app", "/mnt/alias/app/config", "/unrelated", "8:1", false},
		{"sibling-prefix", "/opt/app", "/mnt/alias/application/config", "/opt", "8:1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := []preparedService{{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "app", State: "present"}}, binaries: []preparedBinary{{name: "app", artifact: &plan.ArtifactChange{Path: tc.artifactPath, Type: "archive"}}}, files: []preparedFile{{component: "config/main", file: &plan.FileChange{Path: tc.configPath}}}}}
			e := Local{Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
				if command == "findmnt" {
					return json.Marshal(map[string]any{"filesystems": []map[string]string{{"target": "/", "maj:min": "8:1", "fsroot": "/"}, {"target": "/mnt/alias", "maj:min": tc.device, "fsroot": tc.fsroot}}})
				}
				if command != "readlink" {
					t.Fatal(command)
				}
				return []byte(args[len(args)-1] + "\n"), nil
			}}}
			err := e.checkArtifactPaths(context.Background(), services)
			if tc.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "resource owned") {
					t.Fatalf("bind alias ownership conflict not detected: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unrelated filesystem path rejected: %v", err)
			}
		})
	}
}

func TestArtifactBindAliasesBetweenArtifactOnlyServices(t *testing.T) {
	services := []preparedService{
		{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "archive", State: "present"}}, binaries: []preparedBinary{{name: "app", artifact: &plan.ArtifactChange{Path: "/opt/app", Type: "archive"}}}},
		{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "download", State: "present"}}, binaries: []preparedBinary{{name: "tool", artifact: &plan.ArtifactChange{Path: "/mnt/alias/app/tool", Type: "download"}}}},
	}
	e := Local{Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "findmnt" {
			return []byte(`{"filesystems":[{"target":"/","maj:min":"8:1","fsroot":"/"},{"target":"/mnt/alias","maj:min":"8:1","fsroot":"/opt"}]}`), nil
		}
		return []byte(args[len(args)-1] + "\n"), nil
	}}}
	if err := e.checkArtifactPaths(context.Background(), services); err == nil {
		t.Fatal("artifact-only bind aliases overlap")
	}
}

func TestArtifactUnselectedOwnershipAliasesWithoutPreparingExcludedServices(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "disjoint", true: "alias-conflict"}[alias], func(t *testing.T) {
			order := func(n int) *int { return &n }
			b := artifactBinary("app")
			b.Type, b.Format = "archive", "tar.gz"
			in := &inventory.Inventory{
				Tags: map[string]inventory.Tag{"all": {Services: map[string]inventory.ServiceAssignment{"artifact": {}, "selected": {}}}},
				Services: map[string]inventory.ServiceDefinition{
					"artifact": {Order: order(1), Service: inventory.Service{Binaries: []inventory.Binary{b}, Configs: []inventory.Config{{Path: "/etc/excluded.conf", Template: inventory.Template{File: "/missing/excluded-template"}}}}},
					"selected": {Order: order(2), Service: inventory.Service{Configs: []inventory.Config{{Path: "/alias/config", Template: inventory.Template{Inline: inline("selected")}}}}},
				},
				Hosts: []inventory.Host{{Name: "local", Tags: []string{"all"}, Transport: inventory.Transport{Type: "local"}}},
			}
			r, err := in.Resolve(inventory.Selectors{Services: []string{"selected"}})
			if err != nil {
				t.Fatal(err)
			}
			observed := 0
			e := Local{
				Artifacts: artifact.Backend{RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
					t.Fatal("excluded artifact capability or observation was invoked")
					return nil, nil
				}},
				Files: &config.Target{Run: func(context.Context, string, ...string) ([]byte, error) {
					return []byte("-\n-\n"), nil
				}, RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
					observed++
					return fileObservation("absent", ""), nil
				}},
				Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
					if command == "findmnt" {
						return []byte(`{"filesystems":[]}`), nil
					}
					p := args[len(args)-1]
					if p == "/alias/config" && alias {
						p = "/opt/app/config"
					}
					return []byte(p + "\n"), nil
				}},
			}
			p, err := e.Diff(context.Background(), r)
			if alias {
				if err == nil || !strings.Contains(err.Error(), "resource owned") || observed != 0 {
					t.Fatalf("excluded artifact alias not rejected before observation: %v, observed=%d", err, observed)
				}
				return
			}
			if err != nil || len(p.Hosts[0].Tasks) != 1 || p.Hosts[0].Tasks[0].Service != "selected" || observed != 1 {
				t.Fatalf("excluded service was prepared or scheduled: %v, plan=%+v, observed=%d", err, p, observed)
			}
		})
	}
}

func TestArtifactPathChecksSkipHostsWithoutArtifactDeclarations(t *testing.T) {
	e := Local{Mount: mount.Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("artifact path checks contacted host without artifacts")
		return nil, nil
	}}}
	services := ownershipPaths([]inventory.ResolvedService{{Definition: inventory.Service{Configs: []inventory.Config{{Type: "mount", Source: "/dev/example", Target: "/srv/data"}}}}})
	if err := e.checkArtifactPaths(context.Background(), services); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactUnselectedConfigAliasDoesNotRenderTemplate(t *testing.T) {
	b := artifactBinary("app")
	b.Type, b.Format = "archive", "tar.gz"
	r := resolved(inventory.Service{Binaries: []inventory.Binary{b}})
	r.Hosts[0].OwnershipServices = append([]inventory.ResolvedService{}, r.Hosts[0].Services...)
	r.Hosts[0].OwnershipServices = append(r.Hosts[0].OwnershipServices, inventory.ResolvedService{
		Assignment: inventory.Assignment{Name: "excluded", State: "present"},
		Definition: inventory.Service{Configs: []inventory.Config{{Path: "/alias/config", Template: inventory.Template{File: "/missing/excluded-template"}}}},
	})
	e := Local{Artifacts: artifactFake(t, true, nil), Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "findmnt" {
			return []byte(`{"filesystems":[]}`), nil
		}
		p := args[len(args)-1]
		if p == "/alias/config" {
			p = "/opt/app/config"
		}
		return []byte(p + "\n"), nil
	}}}
	if _, err := e.Diff(context.Background(), r); err == nil || !strings.Contains(err.Error(), "resource owned") {
		t.Fatalf("excluded alias declaration not checked without rendering: %v", err)
	}
}

func TestArtifactCannotOwnAliasedExternalSystemdDropins(t *testing.T) {
	services := ownershipPaths([]inventory.ResolvedService{{Assignment: inventory.Assignment{Name: "app", State: "present"}, Definition: inventory.Service{
		Binaries:  []inventory.Binary{{Name: "distribution", Type: "archive", Path: "/mnt/units/app.service.d"}},
		Processes: []inventory.Process{{Type: "systemd", Unit: "app.service"}},
	}}})
	e := Local{Mount: mount.Backend{Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command == "findmnt" {
			return []byte(`{"filesystems":[{"target":"/","maj:min":"8:1","fsroot":"/"},{"target":"/mnt/units","maj:min":"8:1","fsroot":"/etc/systemd/system"}]}`), nil
		}
		return []byte(args[len(args)-1] + "\n"), nil
	}}}
	if err := e.checkArtifactPaths(context.Background(), services); err == nil || !strings.Contains(err.Error(), "systemd unit") {
		t.Fatalf("external unit alias allowed into archive scope: %v", err)
	}
}
