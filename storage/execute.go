package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/subuk/richt/plan"
)

func Validate(c *plan.StorageChange) error { return c.Validate() }

func (b Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "storage" || t.Artifact != nil || t.File != nil || t.Path != nil || t.Mount != nil || t.RPM != nil || t.Process != nil || t.Oneshot != nil {
		return nil, errors.New("invalid storage task")
	}
	c := t.Storage
	if err := Validate(c); err != nil {
		return nil, err
	}
	lines := []string{"check resource UUID/creation tag, input device identities, signatures, and unrelated consumers immediately before mutation"}
	inputs := c.Members
	if c.Type == "lvm-vg" {
		inputs = c.Devices
	}
	for _, d := range inputs {
		lines = append(lines, fmt.Sprintf("input %s: producer=%q WWN=%q serial=%q loop-backing=%q size=%d bytes", d.Path, d.Producer, d.WWN, d.Serial, d.BackingFile, d.SizeBytes))
	}
	if c.State == "present" && c.Type != "lvm-vg" && c.Filesystem == "" {
		lines = append(lines, "filesystem: unmanaged; no formatter or filesystem-type requirement")
	}
	if c.State == "absent" {
		lines = append(lines, "destructive: delete the resource and all data it contains; preserve unrelated consumers")
		switch c.Type {
		case "mdadm":
			lines = append(lines, "check MD UUID "+c.Identity, "mdadm --stop "+c.Array)
			for _, d := range c.Members {
				lines = append(lines, "mdadm --zero-superblock "+d.Path+" (only metadata with MD UUID "+c.Identity+")")
			}
		case "lvm-vg":
			lines = append(lines, "check VG UUID "+c.Identity+" and require no remaining logical volumes", "vgremove --yes "+c.VG)
			for _, pv := range c.PVs {
				lines = append(lines, "pvremove --yes "+pv.Device.Path+" (only PV UUID "+pv.UUID+")")
			}
		case "lvm-lv":
			lines = append(lines, "check LV UUID "+c.Identity+" in VG identity "+c.ParentIdentity, "lvremove --yes "+c.VG+"/"+c.LV)
		}
	} else {
		switch c.Type {
		case "mdadm":
			if c.Create {
				lines = append(lines, strings.Join(mdCreateArgs(c), " "))
			}
			if c.Assemble {
				lines = append(lines, "mdadm --assemble --run "+c.Array+" "+strings.Join(devicePaths(c.Members), " "))
			}
			lines = append(lines, "verify RAID1 metadata 1.2, exact members, and MD UUID "+c.Identity)
		case "lvm-vg":
			for _, pv := range c.PVs {
				if pv.Create {
					lines = append(lines, "pvcreate --uuid "+pv.UUID+" --norestorefile --yes "+pv.Device.Path)
				}
			}
			if c.Create {
				lines = append(lines, "vgcreate --addtag "+strings.TrimPrefix(c.Identity, "tag:")+" "+c.VG+" "+strings.Join(devicePaths(c.Devices), " "))
			}
			lines = append(lines, "verify VG identity "+c.Identity+" and exact prepared PV UUIDs")
		case "lvm-lv":
			if c.Create {
				lines = append(lines, strings.Join(lvCreateArgs(c), " "))
			}
			if c.Activate {
				lines = append(lines, "lvchange --activate y "+c.VG+"/"+c.LV)
			}
			lines = append(lines, "verify linear LV size "+strconv.FormatUint(c.SizeBytes, 10)+" bytes and identity "+c.Identity+" in VG "+c.ParentIdentity)
		}
		if c.Format {
			lines = append(lines, "if XFS UUID "+c.FilesystemUUID+" is already present, preserve it; otherwise require no signatures or consumers", "mkfs.xfs -m uuid="+c.FilesystemUUID+" "+c.Output.Path)
		}
		if c.Filesystem != "" {
			lines = append(lines, "verify XFS UUID "+c.FilesystemUUID)
		}
	}
	lines = append(lines, "verify the prepared result; continuation accepts only this operation's identities")
	return []plan.Action{{Kind: "storage", Storage: c, Description: lines}}, nil
}

func devicePaths(ds []plan.DeviceIdentity) []string {
	paths := make([]string, len(ds))
	for i, d := range ds {
		paths[i] = d.Path
	}
	return paths
}
func mdCreateArgs(c *plan.StorageChange) []string {
	args := []string{"mdadm", "--create", c.Array, "--run", "--level=1", "--metadata=" + c.Metadata, "--raid-devices=" + strconv.Itoa(len(c.Members)), "--uuid=" + c.Identity}
	if strings.HasPrefix(c.Array, "/dev/md/") {
		args = append(args, "--name="+strings.TrimPrefix(c.Array, "/dev/md/"))
	}
	return append(args, devicePaths(c.Members)...)
}
func lvCreateArgs(c *plan.StorageChange) []string {
	return []string{"lvcreate", "--type", "linear", "--size", strconv.FormatUint(c.SizeBytes, 10) + "B", "--name", c.LV, "--addtag", strings.TrimPrefix(c.Identity, "tag:"), "--wipesignatures", "n", "--zero", "y", c.VG}
}
func (b Backend) command(ctx context.Context, args []string) error {
	_, err := b.run(ctx, args[0], args[1:]...)
	return err
}

func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind != "storage" {
		return errors.New("unsupported storage action")
	}
	c := a.Storage
	if err := Validate(c); err != nil {
		return err
	}
	var err error
	switch c.Type {
	case "mdadm":
		err = b.executeMD(ctx, c)
	case "lvm-vg":
		err = b.executeVG(ctx, c)
	case "lvm-lv":
		err = b.executeLV(ctx, c)
	}
	if err != nil {
		return err
	}
	if c.State == "present" && c.Filesystem != "" {
		return b.executeFilesystem(ctx, c)
	}
	return nil
}

func (b Backend) executeMD(ctx context.Context, c *plan.StorageChange) error {
	for _, d := range c.Members {
		if err := b.CheckDevice(ctx, d); err != nil {
			return err
		}
	}
	d, s, err := b.device(ctx, c.Array)
	if err != nil && !errors.Is(err, ErrDeviceAbsent) {
		return err
	}
	exists := err == nil
	if exists {
		r, err := b.mdDetail(ctx, d.Path)
		if err != nil {
			return err
		}
		if mdUUID(r["MD_UUID"]) != mdUUID(c.Identity) || r["MD_LEVEL"] != c.Level || r["MD_METADATA"] != c.Metadata || r["MD_DEVICES"] != strconv.Itoa(len(c.Members)) {
			return errors.New("MD array identity/layout changed")
		}
		if c.State == "absent" {
			if err := consumers(s, nil); err != nil {
				return err
			}
		}
	}
	matching := 0
	roles := map[int]bool{}
	for _, d := range c.Members {
		r, err := b.mdMember(ctx, d.Path)
		if err != nil {
			return err
		}
		if r == nil {
			continue
		}
		if mdUUID(r["MD_UUID"]) != mdUUID(c.Identity) {
			return errors.New("MD member was replaced by unrelated RAID metadata")
		}
		role, err := strconv.Atoi(r["MD_DEVICE_ROLE"])
		if err != nil || role < 0 || role >= len(c.Members) || roles[role] || r["MD_LEVEL"] != c.Level || r["MD_METADATA"] != c.Metadata || r["MD_DEVICES"] != strconv.Itoa(len(c.Members)) {
			return errors.New("MD member identity/layout/roles changed")
		}
		roles[role] = true
		matching++
	}
	if c.State == "absent" {
		if exists {
			if _, err := b.run(ctx, "mdadm", "--stop", c.Array); err != nil {
				return err
			}
		}
		for _, d := range c.Members {
			if err := b.CheckDevice(ctx, d); err != nil {
				return err
			}
			if _, s, err := b.device(ctx, d.Path); err != nil {
				return err
			} else if err := consumers(s, nil); err != nil {
				return err
			}
			r, err := b.mdMember(ctx, d.Path)
			if err != nil {
				return err
			}
			if r == nil {
				continue
			}
			if mdUUID(r["MD_UUID"]) != mdUUID(c.Identity) {
				return errors.New("MD member metadata identity changed")
			}
			if _, err := b.run(ctx, "mdadm", "--zero-superblock", d.Path); err != nil {
				return err
			}
			if r, err := b.mdMember(ctx, d.Path); err != nil {
				return err
			} else if r != nil {
				return errors.New("MD metadata removal verification failed")
			}
		}
		if _, err := b.ProbeDevice(ctx, c.Array); !errors.Is(err, ErrDeviceAbsent) {
			if err != nil {
				return err
			}
			return errors.New("MD array still exists after deletion")
		}
		return nil
	}
	if !exists {
		if matching == len(c.Members) {
			args := append([]string{"--assemble", "--run", c.Array}, devicePaths(c.Members)...)
			if _, err := b.run(ctx, "mdadm", args...); err != nil {
				return err
			}
		} else if matching != 0 {
			return errors.New("incomplete MD creation metadata; refusing replacement")
		} else if c.Create {
			for _, d := range c.Members {
				if err := b.blank(ctx, d, nil); err != nil {
					return err
				}
			}
			if err := b.command(ctx, mdCreateArgs(c)); err != nil {
				return err
			}
		} else {
			return errors.New("prepared MD array is absent")
		}
	}
	r, err := b.mdDetail(ctx, c.Array)
	if err != nil {
		return err
	}
	if mdUUID(r["MD_UUID"]) != mdUUID(c.Identity) || r["MD_LEVEL"] != c.Level || r["MD_METADATA"] != c.Metadata || r["MD_DEVICES"] != strconv.Itoa(len(c.Members)) {
		return errors.New("MD array result verification failed")
	}
	return nil
}

func (b Backend) verifyVG(c *plan.StorageChange, s lvmState) error {
	vg, err := s.vg(c.VG)
	if err != nil {
		return err
	}
	if vg == nil || !sameIdentity(vg, "vg", c.Identity) {
		return errors.New("VG identity changed")
	}
	actual := map[string]bool{}
	for _, pv := range s.pvs {
		if pv["vg_uuid"] == vg["vg_uuid"] {
			actual[pv["pv_uuid"]] = true
		}
	}
	if len(actual) != len(c.PVs) {
		return errors.New("VG backing PV set changed")
	}
	for _, pv := range c.PVs {
		if !actual[pv.UUID] {
			return errors.New("VG backing PV identity changed")
		}
	}
	return nil
}

func (b Backend) pvAt(ctx context.Context, s lvmState, d plan.DeviceIdentity) (row, error) {
	want, err := b.ProbeDevice(ctx, d.Path)
	if err != nil {
		return nil, err
	}
	for _, pv := range s.pvs {
		actual, err := b.ProbeDevice(ctx, pv["pv_name"])
		if err != nil {
			return nil, err
		}
		if actual.MajorMinor == want.MajorMinor {
			return pv, nil
		}
	}
	return nil, nil
}

func (b Backend) executeVG(ctx context.Context, c *plan.StorageChange) error {
	for _, d := range c.Devices {
		if err := b.CheckDevice(ctx, d); err != nil {
			return err
		}
	}
	s, err := b.lvm(ctx)
	if err != nil {
		return err
	}
	vg, err := s.vg(c.VG)
	if err != nil {
		return err
	}
	if vg != nil {
		if err := b.verifyVG(c, s); err != nil {
			return err
		}
	}
	if c.State == "absent" {
		if vg != nil {
			for _, lv := range s.lvs {
				if lv["vg_uuid"] == vg["vg_uuid"] {
					return errors.New("VG deletion is blocked by a remaining logical volume")
				}
			}
			for _, pv := range c.PVs {
				_, dev, err := b.device(ctx, pv.Device.Path)
				if err != nil {
					return err
				}
				if err := consumers(dev, nil); err != nil {
					return err
				}
			}
			if _, err := b.run(ctx, "vgremove", "--yes", c.VG); err != nil {
				return err
			}
		}
		for _, expected := range c.PVs {
			if err := b.CheckDevice(ctx, expected.Device); err != nil {
				return err
			}
			s, err = b.lvm(ctx)
			if err != nil {
				return err
			}
			if current, err := s.vg(c.VG); err != nil {
				return err
			} else if current != nil {
				return errors.New("VG still exists or was replaced during deletion")
			}
			pv, err := b.pvAt(ctx, s, expected.Device)
			if err != nil {
				return err
			}
			if pv == nil {
				sigs, err := b.signatures(ctx, expected.Device.Path)
				if err != nil {
					return err
				}
				if len(sigs) > 0 {
					return errors.New("PV was replaced by unrelated signatures")
				}
				continue
			}
			if pv["pv_uuid"] != expected.UUID || pv["vg_name"] != "" {
				return errors.New("PV metadata identity/ownership changed")
			}
			_, dev, err := b.device(ctx, expected.Device.Path)
			if err != nil {
				return err
			}
			if err := consumers(dev, nil); err != nil {
				return err
			}
			if _, err := b.run(ctx, "pvremove", "--yes", expected.Device.Path); err != nil {
				return err
			}
			s, err = b.lvm(ctx)
			if err != nil {
				return err
			}
			if pv, err := b.pvAt(ctx, s, expected.Device); err != nil {
				return err
			} else if pv != nil {
				return errors.New("PV cleanup verification failed")
			}
		}
		return nil
	}
	if vg == nil {
		if !c.Create {
			return errors.New("prepared VG is absent")
		}
		for _, expected := range c.PVs {
			s, err = b.lvm(ctx)
			if err != nil {
				return err
			}
			pv, err := b.pvAt(ctx, s, expected.Device)
			if err != nil {
				return err
			}
			if pv != nil {
				if pv["pv_uuid"] != expected.UUID || pv["vg_name"] != "" {
					return errors.New("PV identity/ownership changed before VG creation")
				}
				continue
			}
			if !expected.Create {
				return errors.New("prepared existing PV is absent")
			}
			if err := b.blank(ctx, expected.Device, nil); err != nil {
				return err
			}
			if _, err := b.run(ctx, "pvcreate", "--uuid", expected.UUID, "--norestorefile", "--yes", expected.Device.Path); err != nil {
				return err
			}
		}
		args := append([]string{"--addtag", strings.TrimPrefix(c.Identity, "tag:"), c.VG}, devicePaths(c.Devices)...)
		if _, err := b.run(ctx, "vgcreate", args...); err != nil {
			return err
		}
	}
	s, err = b.lvm(ctx)
	if err != nil {
		return err
	}
	return b.verifyVG(c, s)
}

func (b Backend) executeLV(ctx context.Context, c *plan.StorageChange) error {
	s, err := b.lvm(ctx)
	if err != nil {
		return err
	}
	vg, err := s.vg(c.VG)
	if err != nil {
		return err
	}
	lv, err := s.lv(c.VG, c.LV)
	if err != nil {
		return err
	}
	if vg != nil && !sameIdentity(vg, "vg", c.ParentIdentity) {
		return errors.New("parent VG identity changed")
	}
	if lv != nil {
		if !sameIdentity(lv, "lv", c.Identity) || lv["segtype"] != "linear" {
			return errors.New("LV identity/layout changed")
		}
		if c.State == "present" {
			size, err := byteField(lv, "lv_size")
			if err != nil {
				return err
			}
			if size != c.SizeBytes {
				return errors.New("LV size changed")
			}
		}
	}
	path := c.VG + "/" + c.LV
	if c.State == "absent" {
		if lv == nil {
			return nil
		}
		_, dev, err := b.device(ctx, "/dev/"+path)
		if err != nil && !errors.Is(err, ErrDeviceAbsent) {
			return err
		}
		if err == nil {
			if err := consumers(dev, nil); err != nil {
				return err
			}
		}
		if _, err := b.run(ctx, "lvremove", "--yes", path); err != nil {
			return err
		}
		s, err = b.lvm(ctx)
		if err != nil {
			return err
		}
		if lv, err := s.lv(c.VG, c.LV); err != nil {
			return err
		} else if lv != nil {
			return errors.New("LV removal verification failed")
		}
		return nil
	}
	if vg == nil {
		return errors.New("prepared parent VG is absent")
	}
	if lv == nil {
		if !c.Create {
			return errors.New("prepared LV is absent")
		}
		if err := b.command(ctx, lvCreateArgs(c)); err != nil {
			return err
		}
	} else if lv["lv_active"] == "inactive" {
		if _, err := b.run(ctx, "lvchange", "--activate", "y", path); err != nil {
			return err
		}
	}
	s, err = b.lvm(ctx)
	if err != nil {
		return err
	}
	lv, err = s.lv(c.VG, c.LV)
	if err != nil {
		return err
	}
	if lv == nil || !sameIdentity(lv, "lv", c.Identity) || lv["segtype"] != "linear" {
		return errors.New("LV result identity verification failed")
	}
	size, err := byteField(lv, "lv_size")
	if err != nil {
		return err
	}
	if size != c.SizeBytes {
		return errors.New("LV result size verification failed")
	}
	if lv["lv_active"] != "active" {
		return errors.New("LV activation verification failed")
	}
	output, err := b.ProbeDevice(ctx, c.Output.Path)
	if err != nil {
		return err
	}
	if output.Producer != "lv:"+lv["lv_uuid"] {
		return errors.New("LV output block device identity verification failed")
	}
	return nil
}

func (b Backend) executeFilesystem(ctx context.Context, c *plan.StorageChange) error {
	expected := c.Output
	expected.Filesystem, expected.UUID = "", ""
	if err := b.CheckDevice(ctx, expected); err != nil {
		return err
	}
	sigs, err := b.signatures(ctx, c.Output.Path)
	if err != nil {
		return err
	}
	if len(sigs) == 1 && sigs[0].Type == "xfs" && sigs[0].UUID == c.FilesystemUUID {
		return nil
	}
	if len(sigs) != 0 {
		return errors.New("filesystem signature/identity changed; refusing reformatting")
	}
	if !c.Format {
		return errors.New("prepared filesystem is absent")
	}
	_, dev, err := b.device(ctx, c.Output.Path)
	if err != nil {
		return err
	}
	if err := consumers(dev, nil); err != nil {
		return err
	}
	if _, err := b.run(ctx, "mkfs.xfs", "-m", "uuid="+c.FilesystemUUID, c.Output.Path); err != nil {
		return err
	}
	sigs, err = b.signatures(ctx, c.Output.Path)
	if err != nil {
		return err
	}
	if len(sigs) != 1 || sigs[0].Type != "xfs" || sigs[0].UUID != c.FilesystemUUID {
		return fmt.Errorf("XFS result verification failed for %s", c.Output.Path)
	}
	return nil
}
