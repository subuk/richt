package inventory

import "testing"

func TestTemplateWriteOptionsCannotBeUsedByOtherConfigs(t *testing.T) {
	no := false
	for _, typ := range []string{"file", "directory", "mdadm", "lvm-vg", "lvm-lv", "mount", "user", "group"} {
		for _, c := range []Config{{Type: typ, AtomicWrites: &no}, {Type: typ, Backup: true}} {
			if err := validateConfig(c); err == nil {
				t.Fatalf("%s accepted template write options", typ)
			}
		}
	}
}

func TestTypedConfigOwnership(t *testing.T) {
	for _, configs := range [][]Config{
		{{Type: "file", Path: "/etc/fstab"}, {Type: "mount", Source: "/dev/a", Target: "/srv/a", Filesystem: "xfs"}},
		{{Type: "mount", Source: "/dev/a", Target: "/srv/a", Filesystem: "xfs"}, {Type: "mount", Source: "/dev/b", Target: "/srv/./a", Filesystem: "xfs"}},
		{{Type: "mdadm", Array: "/dev/md/a", Level: "raid1", Metadata: "1.2", Members: []string{"/dev/a", "/dev/b"}}, {Type: "lvm-vg", VG: "data", Devices: []string{"/dev/a"}}},
		{{Type: "lvm-lv", VG: "data", LV: "lv", Size: "1GiB"}, {Type: "lvm-lv", VG: "data", LV: "lv", Size: "1GiB"}},
		{{Type: "lvm-lv", VG: "data", LV: "lv", Size: "1GiB"}, {Type: "file", Path: "/dev/data/lv"}},
	} {
		in := fixture()
		setService(in, "app", Service{Configs: configs})
		if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}}); err == nil {
			t.Fatalf("conflicting ownership accepted: %#v", configs)
		}
	}
	in := fixture()
	setService(in, "app", Service{Configs: []Config{
		{Type: "mdadm", Array: "/dev/md/a", Level: "raid1", Metadata: "1.2", Members: []string{"/dev/a", "/dev/b"}},
		{Type: "lvm-vg", VG: "data", Devices: []string{"/dev/md/a"}},
		{Type: "lvm-lv", VG: "data", LV: "lv", Size: "1GiB", Filesystem: "xfs"},
		{Type: "mount", Source: "/dev/data/lv", Target: "/srv/a", Filesystem: "xfs"},
		{Type: "mount", Source: "/dev/other", Target: "/srv/b", Filesystem: "xfs"},
	}})
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}}); err != nil {
		t.Fatalf("composed ownership rejected: %v", err)
	}
}

func TestDirectoryDeletionCannotClaimOtherServiceChildren(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Configs: []Config{{Type: "directory", Path: "/srv/data"}}})
	setService(in, "repo", Service{Configs: []Config{{Type: "file", Path: "/srv/data/other-service"}}})
	if _, err := in.Resolve(Selectors{Hosts: []string{"lanst"}, Services: []string{"app"}}); err == nil {
		t.Fatal("directory deletion overlaps another assigned service")
	}
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}}); err != nil {
		t.Fatalf("present parent directory rejected: %v", err)
	}
}
