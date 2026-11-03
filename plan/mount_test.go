package plan

import "testing"

func TestMountChangeValidation(t *testing.T) {
	c := MountChange{Source: "/dev/data", Target: "/srv/data", Filesystem: "xfs", State: "present", Identity: DeviceIdentity{Path: "/dev/data", Filesystem: "xfs", UUID: "fixed"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Options = []string{"noatime", "noatime"}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted duplicate mount option")
	}
	c.Options = []string{"foo\nbar"}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted multiline mount option")
	}
	c.Options = nil
	c.Identity.UUID = ""
	if err := c.Validate(); err == nil {
		t.Fatal("accepted missing filesystem identity")
	}
}
