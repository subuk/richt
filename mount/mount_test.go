package mount

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

func change(target string) *plan.MountChange {
	return &plan.MountChange{Source: "/dev/data", Target: target, Filesystem: "xfs", State: "present", Options: []string{"defaults", "noatime"}, Identity: plan.DeviceIdentity{Path: "/dev/data", MajorMinor: "8:1", SizeBytes: 1024, UUID: "fs-data", Filesystem: "xfs"}}
}
func TestFstabCompositionPreservesComments(t *testing.T) {
	before := "# root disk\nUUID=root / xfs defaults 0 0\n\n# data follows\n"
	a := change("/srv/one")
	first, err := editFstab(before, a, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	b := change("/srv/two")
	second, err := editFstab(first, b, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second, before) || strings.Count(second, "UUID=fs-data") != 2 {
		t.Fatal(second)
	}
	a.State = "absent"
	third, err := editFstab(second, a, func(s string) bool { return s == "UUID=fs-data" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(third, before) || strings.Contains(third, "/srv/one") || !strings.Contains(third, "/srv/two") {
		t.Fatal(third)
	}
}
func TestFstabDuplicatesAndConflict(t *testing.T) {
	c := change("/srv/data")
	before := "/dev/data /srv/data xfs defaults 0 0 # first\n/dev/data /srv/data xfs defaults 0 0 # second\n"
	after, err := editFstab(before, c, func(s string) bool { return s == "/dev/data" })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(after, "/srv/data") != 1 || !strings.Contains(after, "# first") || !strings.Contains(after, "# second") {
		t.Fatal(after)
	}
	_, err = editFstab("/dev/other /srv/data xfs defaults 0 0\n", c, func(string) bool { return false })
	if err == nil {
		t.Fatal("accepted conflicting source")
	}
}

type fake struct {
	fstab      string
	active     []activeMount
	calls      []string
	busy       bool
	failReload bool
	absent     bool
	uuid       string
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "readlink":
		return []byte(args[len(args)-1] + "\n"), nil
	case "sh":
		if f.absent {
			return nil, nil
		}
		return []byte("/dev/data\n"), nil
	case "lsblk":
		uuid := f.uuid
		if uuid == "" {
			uuid = "fs-data"
		}
		return []byte(`{"blockdevices":[{"name":"/dev/data","type":"disk","maj:min":"8:1","size":1024,"fstype":"xfs","uuid":"` + uuid + `","mountpoints":[]}]}`), nil
	case "wipefs":
		id := f.uuid
		if id == "" {
			id = "fs-data"
		}
		return []byte(`{"signatures":[{"type":"xfs","uuid":"` + id + `"}]}`), nil
	case "findmnt":
		return json.Marshal(map[string]any{"filesystems": f.active})
	case "cat":
		return []byte(f.fstab), nil
	case "mkdir":
		return nil, nil
	case "systemctl":
		if f.failReload {
			f.failReload = false
			return nil, errors.New("reload failed")
		}
		return nil, nil
	case "mount":
		f.active = []activeMount{{Source: "/dev/data", Target: "/srv/data", Filesystem: "xfs", Options: "rw,noatime", MajorMinor: "8:1"}}
		return nil, nil
	case "umount":
		if f.busy {
			return nil, errors.New("busy")
		}
		f.active = nil
		return nil, nil
	}
	return nil, errors.New("unexpected command " + name)
}
func (f *fake) input(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	if name != "sh" || len(args) != 2 || args[1] != writeFstab {
		return nil, errors.New("unexpected writer")
	}
	f.fstab = string(input)
	return nil, nil
}
func (f *fake) backend() Backend { return Backend{Run: f.run, RunInput: f.input} }
func config(target string) inventory.Config {
	return inventory.Config{Type: "mount", Source: "/dev/alias", Target: target, Filesystem: "xfs", Options: []string{"defaults", "noatime"}}
}

func TestPrepareFrozenCompositionAndAlias(t *testing.T) {
	f := &fake{fstab: "# retained\n/dev/other /else xfs defaults 0 0\n"}
	a, changed, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	b, _, err := f.backend().Prepare(context.Background(), config("/srv/second"), "present", nil, []*plan.MountChange{a})
	if err != nil {
		t.Fatal(err)
	}
	if b.FstabBefore != a.FstabAfter || !strings.Contains(b.FstabAfter, "/srv/data") || !strings.Contains(b.FstabAfter, "/srv/second") {
		t.Fatalf("%+v", b)
	}
	if !a.TargetPending || a.Active {
		t.Fatal("unmounted target not identified")
	}
	if f.fstab != "# retained\n/dev/other /else xfs defaults 0 0\n" {
		t.Fatal("preparation mutated fstab")
	}
	for _, command := range f.calls {
		if strings.HasPrefix(command, "mkdir ") || strings.HasPrefix(command, "mount ") || strings.HasPrefix(command, "systemctl ") {
			t.Fatal(command)
		}
	}
}
func TestExistingActiveOptionsAndIncompatibleSource(t *testing.T) {
	f := &fake{fstab: "UUID=fs-data /srv/data xfs defaults,noatime 0 0\n", active: []activeMount{{Target: "/srv/data", MajorMinor: "8:1", Filesystem: "xfs", Options: "rw,noatime"}}}
	c, changed, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil)
	if err != nil || changed || !c.Active {
		t.Fatalf("%+v changed=%v err=%v", c, changed, err)
	}
	f.active[0].MajorMinor = "8:2"
	if _, _, err = f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil); err == nil {
		t.Fatal("accepted wrong active source")
	}
}
func TestBusyUnmountDoesNotEditFstab(t *testing.T) {
	f := &fake{fstab: "UUID=fs-data /srv/data xfs defaults,noatime 0 0\n", active: []activeMount{{Target: "/srv/data", MajorMinor: "8:1", Filesystem: "xfs", Options: "rw,noatime"}}, busy: true}
	c, _, err := f.backend().Prepare(context.Background(), config("/srv/data"), "absent", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := f.fstab
	if err = f.backend().Execute(context.Background(), plan.Action{Kind: "mount", Mount: c}); err == nil {
		t.Fatal("busy unmount succeeded")
	}
	if before != f.fstab {
		t.Fatal("busy unmount removed persistence")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "--force") || strings.Contains(call, "--lazy") {
			t.Fatal(call)
		}
	}
}
func TestContinuationAfterFstabWriteAndAfterMount(t *testing.T) {
	f := &fake{fstab: "# keep\n", failReload: true}
	c, _, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := plan.Action{Kind: "mount", Mount: c}
	if err = f.backend().Execute(context.Background(), a); err == nil {
		t.Fatal("reload unexpectedly succeeded")
	}
	if f.fstab != c.FstabAfter {
		t.Fatal("write was not applied")
	}
	if err = f.backend().Execute(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	calls := append([]string(nil), f.calls...)
	if err = f.backend().Execute(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls[len(calls):] {
		if strings.HasPrefix(call, "mount ") {
			t.Fatal("continuation remounted compatible source")
		}
	}
	if strings.Count(f.fstab, "/srv/data") != 1 {
		t.Fatal(f.fstab)
	}
}
func TestReplacementAndFstabDriftFailBeforeMutation(t *testing.T) {
	for _, kind := range []string{"device", "fstab"} {
		t.Run(kind, func(t *testing.T) {
			f := &fake{fstab: "# keep\n"}
			c, _, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "device" {
				f.uuid = "replacement"
			} else {
				f.fstab += "# intervening edit\n"
			}
			before := f.fstab
			if err = f.backend().Execute(context.Background(), plan.Action{Kind: "mount", Mount: c}); err == nil {
				t.Fatal("accepted incompatible precondition")
			}
			if !reflect.DeepEqual(before, f.fstab) || len(f.active) != 0 {
				t.Fatal("mutated resource")
			}
		})
	}
}
func TestAbsentMissingSourceRemovesDeclaredEntry(t *testing.T) {
	f := &fake{fstab: "/dev/alias /srv/data xfs defaults 0 0\n# keep\n", absent: true}
	c, changed, err := f.backend().Prepare(context.Background(), config("/srv/data"), "absent", nil, nil)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if err = f.backend().Execute(context.Background(), plan.Action{Kind: "mount", Mount: c}); err != nil {
		t.Fatal(err)
	}
	if f.fstab != "# keep\n" {
		t.Fatal(f.fstab)
	}
}

func TestAnticipatedSourceFreezesFutureIdentity(t *testing.T) {
	f := &fake{fstab: "# unchanged\n", absent: true}
	future := plan.DeviceIdentity{Path: "/dev/data", Filesystem: "xfs", UUID: "prepared-future", Producer: "lv:prepared", Fresh: true}
	c, changed, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", &future, nil)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if c.Identity.UUID != future.UUID || !c.Identity.Fresh || !c.TargetPending || !strings.Contains(c.FstabAfter, "UUID=prepared-future") {
		t.Fatalf("%+v", c)
	}
	b := f.backend()
	b.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "sh" {
			return nil, errors.New("transport failed")
		}
		return f.run(ctx, name, args...)
	}
	if _, _, err = b.Prepare(context.Background(), config("/srv/data"), "present", &future, nil); err == nil {
		t.Fatal("anticipated source hid observation failure")
	}
}

func TestRequestedDefaultAndExplicitOptions(t *testing.T) {
	for _, test := range []struct {
		have  string
		want  []string
		match bool
	}{
		{"rw,relatime", nil, true},
		{"ro,relatime", []string{"defaults"}, false},
		{"ro,noatime", []string{"defaults", "ro", "noatime"}, true},
		{"rw,nosuid", []string{"defaults"}, false},
		{"rw,sync", []string{"defaults"}, false},
		{"rw,noatime", []string{"noatime", "nofail", "x-systemd.device-timeout=5s"}, true},
		{"rw,relatime", []string{"noatime"}, false},
	} {
		if got := optionsMatch(test.have, test.want); got != test.match {
			t.Errorf("optionsMatch(%q,%v)=%v", test.have, test.want, got)
		}
	}
}

func TestFstabAdoptionPersistsStableFilesystemIdentity(t *testing.T) {
	c := change("/srv/data")
	before := "/dev/dm-0 /srv/data xfs defaults,noatime 0 0 # keep\n"
	after, err := editFstab(before, c, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "/dev/dm-0") || !strings.HasPrefix(after, "UUID=fs-data") || !strings.Contains(after, "# keep") {
		t.Fatal(after)
	}
}

func TestCanonicalTargetAliasesConflictAndFstabAdoption(t *testing.T) {
	f := &fake{fstab: "UUID=fs-data /srv/alias xfs defaults,noatime 0 0 # retained\n"}
	b := f.backend()
	b.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "readlink" && args[len(args)-1] == "/srv/alias" {
			return []byte("/srv/data\n"), nil
		}
		return f.run(ctx, name, args...)
	}
	c, changed, err := b.Prepare(context.Background(), config("/srv/alias"), "present", nil, nil)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if c.Target != "/srv/data" || strings.Contains(c.FstabAfter, "/srv/alias") || !strings.Contains(c.FstabAfter, "# retained") {
		t.Fatalf("%+v", c)
	}
	if _, _, err = b.Prepare(context.Background(), config("/srv/data"), "present", nil, []*plan.MountChange{c}); err == nil {
		t.Fatal("accepted aliases claiming the same target")
	}
}

func TestActiveSubtreeIsIncompatible(t *testing.T) {
	c := change("/srv/data")
	if err := checkActive(&activeMount{Source: "/dev/data[/subdir]", Root: "/subdir", MajorMinor: "8:1", Filesystem: "xfs"}, c); err == nil {
		t.Fatal("accepted subtree as whole-filesystem mount")
	}
}

func TestPendingMountNeverResolvesHiddenChildSymlinks(t *testing.T) {
	prior := change("/srv/data")
	prior.TargetPending = true
	prior.Identity.Fresh = true
	b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("inspected hidden child path")
		return nil, nil
	}}
	path, err := b.preparationPath(context.Background(), "/srv/data/nested", []*plan.MountChange{prior})
	if err != nil || path != "/srv/data/nested" {
		t.Fatalf("%s %v", path, err)
	}
	prior.Identity.Fresh = false
	if _, err = b.preparationPath(context.Background(), "/srv/data/nested", []*plan.MountChange{prior}); err == nil {
		t.Fatal("accepted hidden existing filesystem child")
	}
}

func TestTargetSymlinkSubstitutionFailsBeforeMutation(t *testing.T) {
	f := &fake{fstab: "# retained\n"}
	c, _, err := f.backend().Prepare(context.Background(), config("/srv/data"), "present", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := f.backend()
	b.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "readlink" {
			return []byte("/other-target\n"), nil
		}
		t.Fatal("passed substituted target precondition")
		return nil, nil
	}
	if err = b.Execute(context.Background(), plan.Action{Kind: "mount", Mount: c}); err == nil {
		t.Fatal("accepted substituted mount target")
	}
}

func TestProjectPathStopsAtAliasedPendingMountRoot(t *testing.T) {
	prior := change("/srv/data")
	prior.TargetPending = true
	b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "readlink" || args[len(args)-1] != "/alias" {
			t.Fatalf("resolved hidden suffix: %s %v", name, args)
		}
		return []byte("/srv/data\n"), nil
	}}
	path, err := b.ProjectPath(context.Background(), "/alias/hidden/child", []*plan.MountChange{prior})
	if err != nil || path != "/srv/data/hidden/child" {
		t.Fatalf("%s %v", path, err)
	}
}
