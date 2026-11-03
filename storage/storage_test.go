package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

type fixture struct {
	devices               map[string]*block
	aliases               map[string]string
	sigs                  map[string][]signature
	md                    map[string]string
	memberMD              map[string]string
	pvs, vgs, lvs         []row
	writes                []string
	failAfter             string
	backing               string
	memberMetadataMissing bool
}

func newFixture() *fixture {
	f := &fixture{devices: map[string]*block{}, aliases: map[string]string{}, sigs: map[string][]signature{}, md: map[string]string{}, memberMD: map[string]string{}, backing: "/tmp/disk\n1:100"}
	for i, path := range []string{"/dev/a", "/dev/b"} {
		f.devices[path] = &block{Name: path, Type: "disk", MajorMinor: fmt.Sprintf("8:%d", i), Size: 1 << 30, Serial: path}
	}
	return f
}
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func (f *fixture) path(p string) string {
	if a := f.aliases[p]; a != "" {
		return a
	}
	return p
}
func (f *fixture) run(_ context.Context, name string, args ...string) ([]byte, error) {
	last := args[len(args)-1]
	switch name {
	case "sh":
		if args[1] == canonicalScript {
			p := f.path(last)
			if f.devices[p] != nil {
				return []byte(p + "\n"), nil
			}
			return nil, nil
		}
		return []byte(f.backing + "\n"), nil
	case "lsblk":
		d := f.devices[f.path(last)]
		if d == nil {
			return nil, errors.New("missing device")
		}
		return jsonBytes(map[string]any{"blockdevices": []*block{d}}), nil
	case "wipefs":
		return jsonBytes(map[string]any{"signatures": f.sigs[f.path(last)]}), nil
	case "env":
		role := "0"
		if f.path(last) == "/dev/b" {
			role = "1"
		}
		return []byte("        Version : 1.2\n    Device Role : Active device " + role + "\n"), nil
	case "pvs", "vgs", "lvs":
		var rows []row
		section := "pv"
		if name == "pvs" {
			rows = f.pvs
		}
		if name == "vgs" {
			rows = f.vgs
			section = "vg"
		}
		if name == "lvs" {
			rows = f.lvs
			section = "seg"
		}
		if rows == nil {
			rows = []row{}
		}
		return jsonBytes(map[string]any{"report": []any{map[string]any{section: rows}}}), nil
	case "mdadm":
		if args[0] == "--detail" || args[0] == "--examine" {
			id := f.md[f.path(last)]
			if args[0] == "--examine" {
				id = f.memberMD[f.path(last)]
			}
			if id == "" {
				return nil, errors.New("no MD")
			}
			if args[0] == "--examine" && args[1] == "--brief" {
				return []byte("ARRAY /dev/md/data metadata=1.2 UUID=" + id + "\n"), nil
			}
			role := "0"
			if f.path(last) == "/dev/b" {
				role = "1"
			}
			metadata := "MD_METADATA=1.2\n"
			roleField := "MD_DEVICE_ROLE=" + role + "\n"
			if f.memberMetadataMissing && args[0] == "--examine" {
				metadata = ""
				roleField = ""
			}
			return []byte("MD_UUID=" + id + "\nMD_LEVEL=raid1\n" + metadata + "MD_DEVICES=2\n" + roleField), nil
		}
	}
	f.writes = append(f.writes, name+" "+strings.Join(args, " "))
	arg := func(option string) string {
		i := slices.Index(args, option)
		if i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	switch name {
	case "mdadm":
		switch args[0] {
		case "--create", "--assemble":
			path := args[1]
			id := ""
			if args[0] == "--assemble" {
				path = args[2]
				id = f.memberMD[f.path(last)]
			}
			for _, a := range args {
				if strings.HasPrefix(a, "--uuid=") {
					id = strings.TrimPrefix(a, "--uuid=")
				}
			}
			f.aliases[path] = "/dev/md127"
			f.devices["/dev/md127"] = &block{Name: "/dev/md127", Type: "raid1", MajorMinor: "9:127", Size: (1 << 30) - (1 << 20)}
			f.md["/dev/md127"] = id
			for _, p := range []string{"/dev/a", "/dev/b"} {
				f.memberMD[p] = id
				f.sigs[p] = []signature{{Type: "linux_raid_member", UUID: id}}
				f.devices[p].Children = []block{*f.devices["/dev/md127"]}
			}
		case "--stop":
			p := f.path(last)
			delete(f.devices, p)
			delete(f.md, p)
			for _, d := range f.devices {
				d.Children = slices.DeleteFunc(d.Children, func(c block) bool { return c.Name == p })
			}
		case "--zero-superblock":
			delete(f.memberMD, f.path(last))
			delete(f.sigs, f.path(last))
		}
	case "pvcreate":
		p := f.path(last)
		id := arg("--uuid")
		f.pvs = append(f.pvs, row{"pv_name": p, "pv_uuid": id, "vg_name": "", "vg_uuid": ""})
		f.sigs[p] = []signature{{Type: "LVM2_member", UUID: id}}
	case "vgcreate":
		vg := args[2]
		f.vgs = append(f.vgs, row{"vg_name": vg, "vg_uuid": "VG-uuid", "vg_tags": arg("--addtag"), "vg_extent_size": "4194304", "vg_free": "800000000"})
		for _, pv := range f.pvs {
			pv["vg_name"] = vg
			pv["vg_uuid"] = "VG-uuid"
		}
	case "lvcreate":
		size, _ := strconv.ParseUint(strings.TrimSuffix(arg("--size"), "B"), 10, 64)
		lv := arg("--name")
		vg := last
		path := "/dev/" + vg + "/" + lv
		f.aliases[path] = "/dev/dm-0"
		f.aliases["/dev/mapper/"+vg+"-"+lv] = "/dev/dm-0"
		f.devices["/dev/dm-0"] = &block{Name: "/dev/dm-0", Type: "lvm", MajorMinor: "253:0", Size: size}
		f.lvs = append(f.lvs, row{"vg_name": vg, "vg_uuid": "VG-uuid", "lv_name": lv, "lv_uuid": "LV-uuid", "lv_tags": arg("--addtag"), "lv_size": strconv.FormatUint(size, 10), "segtype": "linear", "lv_path": path, "lv_active": "active"})
		for _, pv := range f.pvs {
			if pv["vg_name"] == vg {
				f.devices[pv["pv_name"]].Children = []block{*f.devices["/dev/dm-0"]}
			}
		}
	case "mkfs.xfs":
		id := strings.TrimPrefix(arg("-m"), "uuid=")
		p := f.path(last)
		f.sigs[p] = []signature{{Type: "xfs", UUID: id}}
		f.devices[p].Filesystem = "xfs"
		f.devices[p].UUID = id
	case "lvremove":
		p := f.path("/dev/" + last)
		delete(f.devices, p)
		delete(f.sigs, p)
		f.lvs = nil
		for _, d := range f.devices {
			d.Children = slices.DeleteFunc(d.Children, func(c block) bool { return c.Name == p })
		}
	case "vgremove":
		f.vgs = nil
		for _, pv := range f.pvs {
			pv["vg_name"] = ""
			pv["vg_uuid"] = ""
		}
	case "pvremove":
		p := f.path(last)
		f.pvs = slices.DeleteFunc(f.pvs, func(r row) bool { return r["pv_name"] == p })
		delete(f.sigs, p)
	case "lvchange":
		for _, lv := range f.lvs {
			lv["lv_active"] = "active"
			size, _ := strconv.ParseUint(lv["lv_size"], 10, 64)
			p := f.path(lv["lv_path"])
			f.devices[p] = &block{Name: p, Type: "lvm", MajorMinor: "253:0", Size: size}
		}
	default:
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	if f.failAfter == name || f.failAfter == name+" "+args[0] {
		f.failAfter = ""
		return nil, errors.New("interrupted after effect")
	}
	return nil, nil
}

func configs() []inventory.Config {
	return []inventory.Config{{Type: "mdadm", Array: "/dev/md/data", Level: "raid1", Metadata: "1.2", Members: []string{"/dev/a", "/dev/b"}}, {Type: "lvm-vg", VG: "vg_data", Devices: []string{"/dev/md/data"}}, {Type: "lvm-lv", VG: "vg_data", LV: "data", Size: "320MiB", Filesystem: "xfs"}}
}
func prepareChain(t *testing.T, b Backend, state string) []*plan.StorageChange {
	t.Helper()
	p := &Preparation{}
	cs := configs()
	if state == "absent" {
		slices.Reverse(cs)
	}
	var out []*plan.StorageChange
	for _, v := range cs {
		c, changed, err := b.Prepare(context.Background(), v, state, p)
		if err != nil {
			t.Fatalf("prepare %s: %v", v.Type, err)
		}
		if changed {
			out = append(out, c)
		}
	}
	return out
}
func execute(t *testing.T, b Backend, c *plan.StorageChange) error {
	t.Helper()
	actions, err := b.Resolve(plan.Task{Operation: "storage", Storage: c})
	if err != nil {
		t.Fatal(err)
	}
	return b.Execute(context.Background(), actions[0])
}

func TestPreparedChainCreatesAdoptsAndDeletesActualMetadata(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	chain := prepareChain(t, b, "present")
	if len(chain) != 3 || len(f.writes) != 0 {
		t.Fatalf("preparation must be complete and read-only: %d %#v", len(chain), f.writes)
	}
	if chain[2].Output.UUID == "" || !chain[2].Output.Fresh || chain[1].Devices[0].Producer == "" {
		t.Fatal("missing anticipated identity")
	}
	for _, c := range chain {
		if err := execute(t, b, c); err != nil {
			t.Fatalf("execute %s: %v", c.Type, err)
		}
	}
	if remaining := prepareChain(t, b, "present"); len(remaining) != 0 {
		t.Fatalf("compatible resources changed: %#v", remaining)
	}
	remove := prepareChain(t, b, "absent")
	if len(remove) != 3 || remove[0].Type != "lvm-lv" || remove[2].Type != "mdadm" {
		t.Fatal("wrong removal scope")
	}
	for _, c := range remove {
		if err := execute(t, b, c); err != nil {
			t.Fatalf("delete %s: %v", c.Type, err)
		}
	}
	if len(f.md)+len(f.memberMD)+len(f.pvs)+len(f.vgs)+len(f.lvs) != 0 {
		t.Fatal("resource metadata was not deleted")
	}
	if len(prepareChain(t, b, "absent")) != 0 {
		t.Fatal("already absent resources must be unchanged")
	}
}

func TestContinuationDoesNotReformatCreatedFilesystem(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	chain := prepareChain(t, b, "present")
	for _, c := range chain[:2] {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	f.failAfter = "mkfs.xfs"
	if err := execute(t, b, chain[2]); err == nil {
		t.Fatal("expected interruption")
	}
	before := len(f.writes)
	if err := execute(t, b, chain[2]); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != before {
		t.Fatalf("continuation repeated effects: %v", f.writes[before:])
	}
}

func TestInterruptedCreationRecognizesMDPVAndVGEffects(t *testing.T) {
	for _, fail := range []string{"mdadm", "pvcreate", "vgcreate", "lvcreate"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture()
			b := Backend{Run: f.run}
			chain := prepareChain(t, b, "present")
			hit := false
			for _, c := range chain {
				if !hit {
					f.failAfter = fail
				}
				err := execute(t, b, c)
				if err != nil {
					hit = true
					if err := execute(t, b, c); err != nil {
						t.Fatalf("continuation: %v", err)
					}
				}
			}
			if !hit {
				t.Fatal("interruption not exercised")
			}
		})
	}
}

func TestDeleteContinuationAndReplacementProtection(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(strconv.FormatBool(replace), func(t *testing.T) {
			f := newFixture()
			b := Backend{Run: f.run}
			for _, c := range prepareChain(t, b, "present") {
				if err := execute(t, b, c); err != nil {
					t.Fatal(err)
				}
			}
			remove := prepareChain(t, b, "absent")
			f.failAfter = "lvremove"
			if err := execute(t, b, remove[0]); err == nil {
				t.Fatal("expected interruption")
			}
			if replace {
				f.lvs = []row{{"vg_name": "vg_data", "vg_uuid": "VG-uuid", "lv_name": "data", "lv_uuid": "replacement", "segtype": "linear"}}
			}
			before := len(f.writes)
			err := execute(t, b, remove[0])
			if replace && err == nil || !replace && err != nil {
				t.Fatalf("replacement=%v err=%v", replace, err)
			}
			if len(f.writes) != before {
				t.Fatal("retry mutated replacement or repeated deletion")
			}
		})
	}
}

func TestVGDeletionAccountsOnlyExplicitEarlierLVDeletion(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	for _, c := range prepareChain(t, b, "present") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.Prepare(context.Background(), configs()[1], "absent", &Preparation{}); err == nil {
		t.Fatal("VG deletion accepted remaining LV")
	}
	remove := prepareChain(t, b, "absent")
	before := len(f.writes)
	if err := execute(t, b, remove[1]); err == nil {
		t.Fatal("execution ignored remaining LV")
	}
	if len(f.writes) != before {
		t.Fatal("VG deletion cascaded")
	}
}

func TestInterruptedDeletionCompletesOnlyPreparedMetadataCleanup(t *testing.T) {
	for _, fail := range []string{"vgremove", "pvremove", "mdadm --stop", "mdadm --zero-superblock"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture()
			b := Backend{Run: f.run}
			for _, c := range prepareChain(t, b, "present") {
				if err := execute(t, b, c); err != nil {
					t.Fatal(err)
				}
			}
			remove := prepareChain(t, b, "absent")
			hit := false
			for _, c := range remove {
				if !hit {
					f.failAfter = fail
				}
				if err := execute(t, b, c); err != nil {
					hit = true
					if err := execute(t, b, c); err != nil {
						t.Fatalf("continuation: %v", err)
					}
				}
			}
			if !hit {
				t.Fatal("interruption not exercised")
			}
			if len(f.md)+len(f.memberMD)+len(f.pvs)+len(f.vgs)+len(f.lvs) != 0 {
				t.Fatal("continuation left managed metadata")
			}
		})
	}
}

func TestContinuationNeverFormatsReplacementFilesystem(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	chain := prepareChain(t, b, "present")
	for _, c := range chain {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	p := f.path(chain[2].Output.Path)
	f.sigs[p] = []signature{{Type: "xfs", UUID: "11111111-2222-3333-4444-555555555555"}}
	before := len(f.writes)
	if err := execute(t, b, chain[2]); err == nil {
		t.Fatal("accepted replacement filesystem")
	}
	if len(f.writes) != before {
		t.Fatal("reformatted replacement filesystem")
	}
}

func TestInvalidDeclaredSequenceAndFilesystemBackedPVFailDuringPreparation(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	if _, _, err := b.Prepare(context.Background(), configs()[1], "present", &Preparation{}); err == nil {
		t.Fatal("silently reordered missing array")
	}
	p := &Preparation{}
	md := configs()[0]
	md.Filesystem = "xfs"
	if _, _, err := b.Prepare(context.Background(), md, "present", p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Prepare(context.Background(), configs()[1], "present", p); err == nil {
		t.Fatal("accepted future filesystem as PV input")
	}
	if len(f.writes) > 0 {
		t.Fatal("preparation mutated resources")
	}
}

func TestUnrelatedNonlinearLVDoesNotBlockSelectedLinearResource(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	for _, c := range prepareChain(t, b, "present") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	f.lvs = append(f.lvs, row{"vg_name": "other", "lv_name": "snapshot", "lv_uuid": "unrelated", "segtype": "thin"})
	if _, changed, err := b.Prepare(context.Background(), configs()[2], "present", &Preparation{}); err != nil || changed {
		t.Fatalf("unrelated layout rejected selected LV: %v %v", changed, err)
	}
	f.lvs = append(f.lvs, row{"vg_name": "vg_data", "lv_name": "data", "lv_uuid": "LV-uuid", "segtype": "striped"})
	if _, _, err := b.Prepare(context.Background(), configs()[2], "present", &Preparation{}); err == nil {
		t.Fatal("ignored incompatible later LV segment")
	}
}

func TestSafetyRejectsSignaturesAliasesAndSubstitution(t *testing.T) {
	t.Run("signature", func(t *testing.T) {
		f := newFixture()
		f.sigs["/dev/a"] = []signature{{Type: "ext4", UUID: "unrelated"}}
		b := Backend{Run: f.run}
		if _, _, err := b.Prepare(context.Background(), configs()[0], "present", &Preparation{}); err == nil {
			t.Fatal("accepted conflicting signature")
		}
		if len(f.writes) > 0 {
			t.Fatal("preparation wrote")
		}
	})
	t.Run("alias", func(t *testing.T) {
		f := newFixture()
		f.aliases["/dev/alias"] = "/dev/a"
		v := configs()[0]
		v.Members = []string{"/dev/a", "/dev/alias"}
		if _, _, err := (Backend{Run: f.run}).Prepare(context.Background(), v, "present", &Preparation{}); err == nil {
			t.Fatal("accepted alias duplicate")
		}
	})
	t.Run("hardware", func(t *testing.T) {
		f := newFixture()
		b := Backend{Run: f.run}
		chain := prepareChain(t, b, "present")
		f.devices["/dev/a"].Serial = "replacement"
		if err := execute(t, b, chain[0]); err == nil {
			t.Fatal("accepted substituted hardware")
		}
		if len(f.writes) > 0 {
			t.Fatal("mutated substituted hardware")
		}
	})
	t.Run("loop backing inode", func(t *testing.T) {
		f := newFixture()
		f.devices["/dev/a"].Type = "loop"
		b := Backend{Run: f.run}
		d, err := b.ProbeDevice(context.Background(), "/dev/a")
		if err != nil {
			t.Fatal(err)
		}
		f.backing = "/tmp/disk\n1:101"
		if err := b.CheckDevice(context.Background(), d); err == nil {
			t.Fatal("accepted replaced loop backing file")
		}
	})
}

func TestFilesystemOmissionLeavesExistingStateUnmanaged(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	for _, c := range prepareChain(t, b, "present") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	p := f.path("/dev/vg_data/data")
	f.sigs[p] = []signature{{Type: "ext4", UUID: "unmanaged"}}
	f.devices[p].Filesystem = "ext4"
	f.devices[p].UUID = "unmanaged"
	v := configs()[2]
	v.Filesystem = ""
	if _, changed, err := b.Prepare(context.Background(), v, "present", &Preparation{}); err != nil || changed {
		t.Fatalf("omitted filesystem managed state: changed=%v err=%v", changed, err)
	}
	v.Filesystem = "xfs"
	if _, _, err := b.Prepare(context.Background(), v, "present", &Preparation{}); err == nil {
		t.Fatal("accepted incompatible filesystem")
	}
}

func TestParseSize(t *testing.T) {
	for _, s := range []string{"0B", "-1G", "20", "1.5G", "18446744073709551615EiB", "2GB"} {
		if _, err := ParseSize(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for s, want := range map[string]uint64{"1B": 1, "2KiB": 2048, "3G": 3 << 30} {
		if got, err := ParseSize(s); err != nil || got != want {
			t.Errorf("%s = %d,%v", s, got, err)
		}
	}
}

func TestMDMemberMetadataAndRoleFallback(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	for _, c := range prepareChain(t, b, "present") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	f.memberMetadataMissing = true
	if remaining := prepareChain(t, b, "present"); len(remaining) != 0 {
		t.Fatal("metadata export omission produced a change")
	}
	for _, c := range prepareChain(t, b, "absent") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInactiveExistingResourcesPrepareExplicitActivation(t *testing.T) {
	t.Run("MD", func(t *testing.T) {
		f := newFixture()
		b := Backend{Run: f.run}
		v := configs()[0]
		c, _, err := b.Prepare(context.Background(), v, "present", &Preparation{})
		if err != nil {
			t.Fatal(err)
		}
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
		if _, err := f.run(context.Background(), "mdadm", "--stop", v.Array); err != nil {
			t.Fatal(err)
		}
		c, changed, err := b.Prepare(context.Background(), v, "present", &Preparation{})
		if err != nil || !changed || !c.Assemble || c.Create {
			t.Fatalf("inactive array: %#v %v", c, err)
		}
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("LV", func(t *testing.T) {
		f := newFixture()
		b := Backend{Run: f.run}
		for _, c := range prepareChain(t, b, "present") {
			if err := execute(t, b, c); err != nil {
				t.Fatal(err)
			}
		}
		f.lvs[0]["lv_active"] = "inactive"
		delete(f.devices, "/dev/dm-0")
		v := configs()[2]
		if _, _, err := b.Prepare(context.Background(), v, "present", &Preparation{}); err == nil {
			t.Fatal("observed filesystem on an inactive resource")
		}
		v.Filesystem = ""
		c, changed, err := b.Prepare(context.Background(), v, "present", &Preparation{})
		if err != nil || !changed || !c.Activate || c.Create {
			t.Fatalf("inactive LV: %#v %v", c, err)
		}
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRawStorageMutationRequiresPersistentDeviceIdentity(t *testing.T) {
	f := newFixture()
	f.devices["/dev/a"].Serial = ""
	b := Backend{Run: f.run}
	if _, _, err := b.Prepare(context.Background(), configs()[0], "present", &Preparation{}); err == nil {
		t.Fatal("accepted a raw input identifiable only by size and major/minor")
	}
	if len(f.writes) > 0 {
		t.Fatal("mutated an input lacking persistent identity")
	}
}

func TestAlreadyAbsentArrayWithMissingMembersIsNoop(t *testing.T) {
	f := newFixture()
	delete(f.devices, "/dev/a")
	delete(f.devices, "/dev/b")
	if _, changed, err := (Backend{Run: f.run}).Prepare(context.Background(), configs()[0], "absent", &Preparation{}); err != nil || changed {
		t.Fatalf("already absent array: %v %v", changed, err)
	}
}

func TestExternalManagedDevicesExposePersistentProducer(t *testing.T) {
	f := newFixture()
	b := Backend{Run: f.run}
	for _, c := range prepareChain(t, b, "present") {
		if err := execute(t, b, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/dev/md/data", "/dev/vg_data/data"} {
		d, err := b.ProbeDevice(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if d.Producer == "" {
			t.Fatalf("external source %s lacks a stable managed identity", path)
		}
	}
}
