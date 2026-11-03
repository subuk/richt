package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

func token() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func uuid() (string, error) {
	s, err := token()
	if err != nil {
		return "", err
	}
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func pvUUID() (string, error) {
	s, err := token()
	if err != nil {
		return "", err
	}
	return s[:6] + "-" + s[6:10] + "-" + s[10:14] + "-" + s[14:18] + "-" + s[18:22] + "-" + s[22:26] + "-" + s[26:], nil
}

var sizePattern = regexp.MustCompile(`^([1-9][0-9]*)(B|[KMGTPE]iB|[KMGTPE])$`)

// ParseSize accepts explicit integral binary units, matching LVM's size units.
func ParseSize(size string) (uint64, error) {
	m := sizePattern.FindStringSubmatch(size)
	if m == nil {
		return 0, errors.New("size requires a positive integer and B, KiB, MiB, GiB, TiB, PiB, EiB (or K, M, G, T, P, E)")
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, errors.New("size is too large")
	}
	power := 0
	if m[2] != "B" {
		power = strings.IndexByte("KMGTPE", m[2][0]) + 1
	}
	for i := 0; i < power; i++ {
		if n > math.MaxUint64/1024 {
			return 0, errors.New("size is too large")
		}
		n *= 1024
	}
	return n, nil
}

func (b Backend) input(ctx context.Context, path string, p *Preparation) (plan.DeviceIdentity, error) {
	if d, ok := p.Lookup(path); ok {
		return d, nil
	}
	d, err := b.ProbeDevice(ctx, path)
	if errors.Is(err, ErrDeviceAbsent) {
		return d, fmt.Errorf("block device %s is absent and no earlier config produces it", path)
	}
	return d, err
}

func (b Backend) Prepare(ctx context.Context, v inventory.Config, state string, p *Preparation) (*plan.StorageChange, bool, error) {
	if p == nil {
		p = &Preparation{}
	}
	p.init()
	if state == "" {
		state = "present"
	}
	c := &plan.StorageChange{Type: v.Type, State: state, Array: v.Array, Level: v.Level, Metadata: v.Metadata, VG: v.VG, LV: v.LV, Filesystem: v.Filesystem}
	var changed bool
	var err error
	switch v.Type {
	case "mdadm":
		changed, err = b.prepareMD(ctx, v, c, p)
	case "lvm-vg":
		changed, err = b.prepareVG(ctx, v, c, p)
	case "lvm-lv":
		changed, err = b.prepareLV(ctx, v, c, p)
	default:
		return nil, false, errors.New("unsupported storage config type")
	}
	if err != nil {
		return nil, false, err
	}
	if !changed && c.Identity == "" {
		return c, false, nil
	}
	if err := Validate(c); err != nil {
		return nil, false, err
	}
	return c, changed, nil
}

func (b Backend) filesystem(ctx context.Context, c *plan.StorageChange) error {
	if c.Filesystem == "" {
		return nil
	}
	if c.Filesystem != "xfs" {
		return errors.New("only XFS filesystems are supported")
	}
	if !c.Create {
		if c.Activate || c.Assemble {
			return errors.New("activate the existing storage resource before preparing managed filesystem state")
		}
		sigs, err := b.signatures(ctx, c.Output.Path)
		if err != nil {
			return err
		}
		if len(sigs) > 0 {
			if len(sigs) != 1 || sigs[0].Type != "xfs" || sigs[0].UUID == "" {
				return errors.New("conflicting filesystem/device signature; refusing formatting")
			}
			c.FilesystemUUID = sigs[0].UUID
			c.Output.UUID = c.FilesystemUUID
			c.Output.Filesystem = "xfs"
			return nil
		}
		_, observed, err := b.device(ctx, c.Output.Path)
		if err != nil {
			return err
		}
		if err := consumers(observed, nil); err != nil {
			return err
		}
	}
	c.Format = true
	c.Output.Fresh = true
	var err error
	c.FilesystemUUID, err = uuid()
	if err != nil {
		return err
	}
	c.Output.Filesystem = "xfs"
	c.Output.UUID = c.FilesystemUUID
	return nil
}

func (b Backend) prepareMD(ctx context.Context, v inventory.Config, c *plan.StorageChange, p *Preparation) (bool, error) {
	if c.Level != "raid1" || c.Metadata != "1.2" || len(v.Members) < 2 {
		return false, errors.New("mdadm requires RAID1, metadata 1.2, and at least two members")
	}
	if c.State == "absent" {
		if _, err := b.ProbeDevice(ctx, c.Array); errors.Is(err, ErrDeviceAbsent) {
			allAbsent := true
			for _, path := range v.Members {
				if _, err := b.ProbeDevice(ctx, path); !errors.Is(err, ErrDeviceAbsent) {
					allAbsent = false
					break
				}
			}
			if allAbsent {
				return false, nil
			}
		}
	}
	for _, path := range v.Members {
		d, err := b.input(ctx, path, p)
		if err != nil {
			return false, err
		}
		if d.Fresh && d.Filesystem != "" {
			return false, errors.New("RAID member input is produced with a filesystem")
		}
		if err := p.claim(d, "mdadm "+v.Array); err != nil {
			return false, err
		}
		d.Filesystem, d.UUID = "", ""
		c.Members = append(c.Members, d)
	}
	d, s, err := b.device(ctx, c.Array)
	if err != nil && !errors.Is(err, ErrDeviceAbsent) {
		return false, err
	}
	exists := err == nil
	var md map[string]string
	if exists {
		md, err = b.mdDetail(ctx, d.Path)
		if err != nil {
			return false, err
		}
		if md["MD_LEVEL"] != c.Level || md["MD_METADATA"] != c.Metadata || md["MD_DEVICES"] != strconv.Itoa(len(c.Members)) {
			return false, errors.New("existing MD layout differs; reshaping is unsupported")
		}
		c.Identity = md["MD_UUID"]
	}
	memberCount := 0
	roles := map[int]bool{}
	for _, member := range c.Members {
		if member.Fresh {
			continue
		}
		r, err := b.mdMember(ctx, member.Path)
		if err != nil {
			return false, err
		}
		if r == nil {
			continue
		}
		memberCount++
		if c.Identity == "" {
			c.Identity = r["MD_UUID"]
		}
		if mdUUID(r["MD_UUID"]) != mdUUID(c.Identity) || r["MD_LEVEL"] != c.Level || r["MD_METADATA"] != c.Metadata || r["MD_DEVICES"] != strconv.Itoa(len(c.Members)) {
			return false, errors.New("MD members belong to an incompatible array")
		}
		role, err := strconv.Atoi(r["MD_DEVICE_ROLE"])
		if err != nil || role < 0 || role >= len(c.Members) || roles[role] {
			return false, errors.New("MD member roles differ; spares, duplicates, and degraded arrays are unsupported")
		}
		roles[role] = true
	}
	if c.Identity != "" && memberCount != len(c.Members) {
		return false, errors.New("MD member set differs; partial or degraded arrays are unsupported")
	}
	if c.State == "absent" {
		if c.Identity == "" {
			return false, nil
		}
		for _, d := range c.Members {
			if err := persistentInput(d); err != nil {
				return false, err
			}
		}
		if exists {
			if err := consumers(s, p); err != nil {
				return false, err
			}
			p.Removed[d.Path] = true
		}
		c.Output = d
		c.Output.Path = v.Array
		c.Output.Producer = "md:" + c.Identity
		p.Removed[v.Array] = true
		return true, nil
	}
	if c.Identity == "" {
		for _, member := range c.Members {
			if err := persistentInput(member); err != nil {
				return false, err
			}
			if !member.Fresh {
				if err := b.blank(ctx, member, p); err != nil {
					return false, err
				}
			}
		}
		id, err := token()
		if err != nil {
			return false, err
		}
		c.Identity = id[:8] + ":" + id[8:16] + ":" + id[16:24] + ":" + id[24:]
		c.Create = true
	} else if !exists {
		c.Assemble = true
	}
	if exists {
		c.Output = d
	} else {
		c.Output = plan.DeviceIdentity{Path: v.Array, Fresh: c.Create}
	}
	c.Output.Path = v.Array
	c.Output.Producer = "md:" + c.Identity
	if err := b.filesystem(ctx, c); err != nil {
		return false, err
	}
	if c.Create || c.Assemble || c.Format {
		for _, d := range c.Members {
			if err := persistentInput(d); err != nil {
				return false, err
			}
		}
	}
	p.publish(v.Array, c.Output)
	return c.Create || c.Assemble || c.Format, nil
}

func (b Backend) prepareVG(ctx context.Context, v inventory.Config, c *plan.StorageChange, p *Preparation) (bool, error) {
	s, err := b.lvm(ctx)
	if err != nil {
		return false, err
	}
	vg, err := s.vg(v.VG)
	if err != nil {
		return false, err
	}
	if vg == nil && c.State == "absent" {
		return false, nil
	}
	for _, path := range v.Devices {
		d, err := b.input(ctx, path, p)
		if err != nil {
			return false, err
		}
		if d.Fresh && d.Filesystem != "" {
			return false, errors.New("VG backing device is produced with a filesystem")
		}
		if err := p.claim(d, "lvm-vg "+v.VG); err != nil {
			return false, err
		}
		d.Filesystem, d.UUID = "", ""
		c.Devices = append(c.Devices, d)
	}
	if len(c.Devices) == 0 {
		return false, errors.New("VG requires backing devices")
	}
	if vg == nil {
		if c.State == "absent" {
			return false, nil
		}
		t, err := token()
		if err != nil {
			return false, err
		}
		c.Identity = "tag:richt_" + t
		c.Create = true
	} else {
		c.Identity = vg["vg_uuid"]
	}
	usedPVs := 0
	for _, d := range c.Devices {
		var pv row
		for _, candidate := range s.pvs {
			actual, err := b.ProbeDevice(ctx, candidate["pv_name"])
			if err != nil {
				return false, err
			}
			if actual.MajorMinor != "" && actual.MajorMinor == d.MajorMinor {
				pv = candidate
				break
			}
		}
		if pv != nil {
			if vg != nil && pv["vg_uuid"] != c.Identity || vg == nil && pv["vg_name"] != "" {
				return false, errors.New("backing PV belongs to another volume group")
			}
			usedPVs++
			c.PVs = append(c.PVs, plan.PVIdentity{Device: d, UUID: pv["pv_uuid"]})
		} else {
			if vg != nil {
				return false, errors.New("existing VG backing device set differs")
			}
			if !d.Fresh {
				if err := b.blank(ctx, d, p); err != nil {
					return false, err
				}
			}
			id, err := pvUUID()
			if err != nil {
				return false, err
			}
			c.PVs = append(c.PVs, plan.PVIdentity{Device: d, UUID: id, Create: true})
		}
	}
	if vg != nil {
		actualCount := 0
		for _, pv := range s.pvs {
			if pv["vg_uuid"] == c.Identity {
				actualCount++
			}
		}
		if actualCount != usedPVs {
			return false, errors.New("existing VG backing device set differs")
		}
	}
	if c.State == "absent" {
		for _, d := range c.Devices {
			if err := persistentInput(d); err != nil {
				return false, err
			}
		}
		for _, lv := range s.lvs {
			if lv["vg_uuid"] == c.Identity && !p.Removed[lv["lv_uuid"]] {
				return false, fmt.Errorf("VG %s still contains LV %s; explicit earlier deletion is required", c.VG, lv["lv_name"])
			}
		}
		for _, pv := range c.PVs {
			_, block, err := b.device(ctx, pv.Device.Path)
			if err != nil {
				return false, err
			}
			if err := consumers(block, p); err != nil {
				return false, err
			}
		}
		p.Removed[c.Identity] = true
		return true, nil
	}
	if vg == nil {
		vg = row{"vg_name": v.VG, "vg_uuid": c.Identity, "vg_extent_size": "4194304"}
	}
	if c.Create {
		for _, d := range c.Devices {
			if err := persistentInput(d); err != nil {
				return false, err
			}
		}
	}
	p.groups[v.VG] = vg
	return c.Create, nil
}

func (b Backend) prepareLV(ctx context.Context, v inventory.Config, c *plan.StorageChange, p *Preparation) (bool, error) {
	size, err := ParseSize(v.Size)
	if err != nil {
		return false, err
	}
	s, err := b.lvm(ctx)
	if err != nil {
		return false, err
	}
	vg, err := s.vg(v.VG)
	if err != nil {
		return false, err
	}
	if vg == nil {
		vg = p.groups[v.VG]
	}
	if vg == nil {
		if c.State == "absent" {
			return false, nil
		}
		return false, fmt.Errorf("VG %s is absent and no earlier config produces it", v.VG)
	}
	c.ParentIdentity = vg["vg_uuid"]
	extent, err := byteField(vg, "vg_extent_size")
	if err != nil {
		return false, err
	}
	if size > math.MaxUint64-(extent-1) {
		return false, errors.New("LV size is too large")
	}
	c.SizeBytes = (size + extent - 1) / extent * extent
	lv, err := s.lv(v.VG, v.LV)
	if err != nil {
		return false, err
	}
	path := "/dev/" + v.VG + "/" + v.LV
	if lv == nil {
		if c.State == "absent" {
			return false, nil
		}
		if _, ok := p.Lookup(path); ok {
			return false, errors.New("LV resource is already owned")
		}
		t, err := token()
		if err != nil {
			return false, err
		}
		c.Identity = "tag:richt_" + t
		c.Create = true
		c.Output = plan.DeviceIdentity{Path: path, SizeBytes: c.SizeBytes, Fresh: true}
	} else {
		if lv["segtype"] != "linear" {
			return false, errors.New("only ordinary linear LVs are supported")
		}
		actualSize, err := byteField(lv, "lv_size")
		if err != nil {
			return false, err
		}
		if c.State == "present" && actualSize != c.SizeBytes {
			return false, errors.New("existing LV size differs; resizing is unsupported")
		}
		c.Identity = lv["lv_uuid"]
		d, block, err := b.device(ctx, path)
		if err != nil && !errors.Is(err, ErrDeviceAbsent) {
			return false, err
		}
		if errors.Is(err, ErrDeviceAbsent) {
			if lv["lv_active"] != "inactive" {
				return false, errors.New("active LV device is unavailable")
			}
			c.Activate = c.State == "present"
			d = plan.DeviceIdentity{Path: path, SizeBytes: actualSize}
		}
		c.Output = d
		c.Output.Path = path
		if c.State == "absent" {
			if err == nil {
				if err := consumers(block, p); err != nil {
					return false, err
				}
			}
			p.Removed[d.Path] = true
			p.Removed["dev:"+d.MajorMinor] = true
			p.Removed[path] = true
			p.Removed[c.Identity] = true
			c.Output.Producer = "lv:" + c.Identity
			return true, nil
		}
	}
	c.Output.Producer = "lv:" + c.Identity
	if err := b.filesystem(ctx, c); err != nil {
		return false, err
	}
	p.publish(path, c.Output)
	// Device-mapper's escaped canonical alias is also an anticipated output.
	p.publish(filepath.Join("/dev/mapper", strings.ReplaceAll(v.VG, "-", "--")+"-"+strings.ReplaceAll(v.LV, "-", "--")), c.Output)
	return c.Create || c.Activate || c.Format, nil
}
