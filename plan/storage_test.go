package plan

import "testing"

func storageFixture() *StorageChange {
	return &StorageChange{Type: "mdadm", State: "present", Array: "/dev/md/data", Level: "raid1", Metadata: "1.2", Identity: "11111111:22222222:33333333:44444444", Create: true, Members: []DeviceIdentity{{Path: "/dev/a", MajorMinor: "8:0", SizeBytes: 1 << 30, Serial: "a"}, {Path: "/dev/b", MajorMinor: "8:1", SizeBytes: 1 << 30, Serial: "b"}}, Output: DeviceIdentity{Path: "/dev/md/data", Producer: "md:11111111:22222222:33333333:44444444", Fresh: true}}
}

func TestStoragePreparedInputValidation(t *testing.T) {
	if err := storageFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*StorageChange){
		"missing identity":            func(c *StorageChange) { c.Identity = "" },
		"unknown type":                func(c *StorageChange) { c.Type = "zfs" },
		"destructive creation":        func(c *StorageChange) { c.State = "absent" },
		"unsupported level":           func(c *StorageChange) { c.Level = "raid5" },
		"missing member identity":     func(c *StorageChange) { c.Members[0].MajorMinor = "" },
		"alias duplicate":             func(c *StorageChange) { c.Members[1].MajorMinor = c.Members[0].MajorMinor },
		"relative device":             func(c *StorageChange) { c.Members[0].Path = "dev/a" },
		"unsafe output":               func(c *StorageChange) { c.Output.Path = "/dev/other" },
		"wrong output identity":       func(c *StorageChange) { c.Output.Producer = "md:replacement" },
		"implicit format":             func(c *StorageChange) { c.Format = true },
		"filesystem lacks fixed uuid": func(c *StorageChange) { c.Filesystem = "xfs" },
		"unrelated fields":            func(c *StorageChange) { c.VG = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			c := storageFixture()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("accepted invalid executable inputs")
			}
		})
	}
}

func TestVGCleanupScopeValidation(t *testing.T) {
	d := DeviceIdentity{Path: "/dev/md/data", Producer: "md:array"}
	c := &StorageChange{Type: "lvm-vg", State: "absent", VG: "vg_data", Identity: "VG-uuid", Devices: []DeviceIdentity{d}, PVs: []PVIdentity{{Device: d, UUID: "PV-uuid"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.PVs[0].Device.Path = "/dev/other"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted PV cleanup outside frozen backing device scope")
	}
}
