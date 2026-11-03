package plan

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// DeviceIdentity fixes the block device selected during preparation. Producer
// identifies a managed MD/LVM resource; hardware fields identify raw devices.
// UUID identifies the filesystem, independently of its containing resource.
type DeviceIdentity struct {
	Path        string `json:"path"`
	MajorMinor  string `json:"major_minor,omitempty"`
	SizeBytes   uint64 `json:"size_bytes,omitempty"`
	Serial      string `json:"serial,omitempty"`
	WWN         string `json:"wwn,omitempty"`
	BackingFile string `json:"backing_file,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	Filesystem  string `json:"filesystem,omitempty"`
	Producer    string `json:"producer,omitempty"`
	Fresh       bool   `json:"fresh,omitempty"`
}

// StorageChange retains the complete scope and safety identities of one
// declarative resource operation. Creation tags/UUIDs also identify effects of
// an interrupted operation, so continuation never reformats a replacement.
type StorageChange struct {
	Type           string           `json:"type"`
	State          string           `json:"state"`
	Array          string           `json:"array,omitempty"`
	Level          string           `json:"level,omitempty"`
	Metadata       string           `json:"metadata,omitempty"`
	Members        []DeviceIdentity `json:"members,omitempty"`
	VG             string           `json:"vg,omitempty"`
	Devices        []DeviceIdentity `json:"devices,omitempty"`
	LV             string           `json:"lv,omitempty"`
	SizeBytes      uint64           `json:"size_bytes,omitempty"`
	Filesystem     string           `json:"filesystem,omitempty"`
	FilesystemUUID string           `json:"filesystem_uuid,omitempty"`
	Identity       string           `json:"identity"`
	ParentIdentity string           `json:"parent_identity,omitempty"`
	Create         bool             `json:"create,omitempty"`
	Format         bool             `json:"format,omitempty"`
	Activate       bool             `json:"activate,omitempty"`
	Assemble       bool             `json:"assemble,omitempty"`
	Output         DeviceIdentity   `json:"output,omitempty"`
	PVs            []PVIdentity     `json:"pvs,omitempty"`
}

// PVIdentity fixes the PV metadata that a VG operation may initialize/remove.
type PVIdentity struct {
	Device DeviceIdentity `json:"device"`
	UUID   string         `json:"uuid"`
	Create bool           `json:"create,omitempty"`
}

var storageName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9_+.-]*$`)
var storageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:-]*$`)
var storageTag = regexp.MustCompile(`^tag:richt_[0-9a-f]{32}$`)
var filesystemID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func blockPath(path string) bool {
	return strings.HasPrefix(path, "/dev/") && filepath.Clean(path) == path && strings.IndexFunc(path, unicode.IsControl) < 0
}

// Validate checks fixed executable inputs without accessing a target host.
func (c *StorageChange) Validate() error {
	if c == nil || c.State != "present" && c.State != "absent" || c.Identity == "" || !storageID.MatchString(c.Identity) && !storageTag.MatchString(c.Identity) {
		return errors.New("invalid storage state or identity")
	}
	if c.State == "absent" && (c.Create || c.Format || c.Activate || c.Assemble) {
		return errors.New("destructive storage request contains creation operations")
	}
	if c.Filesystem != "" && c.Filesystem != "xfs" {
		return errors.New("only XFS filesystems are supported")
	}
	if c.State == "present" && c.Filesystem != "" && (!filesystemID.MatchString(c.FilesystemUUID) || c.Output.Filesystem != "xfs" || c.Output.UUID != c.FilesystemUUID) {
		return errors.New("invalid prepared XFS identity")
	}
	if c.Format && c.Filesystem != "xfs" {
		return errors.New("formatting requires an explicit XFS request")
	}
	validateDevices := func(ds []DeviceIdentity) error {
		seen := map[string]bool{}
		identities := map[string]bool{}
		for _, d := range ds {
			if !blockPath(d.Path) || seen[d.Path] {
				return errors.New("invalid or duplicate storage device path")
			}
			if d.Producer == "" && (d.MajorMinor == "" || d.SizeBytes == 0) {
				return errors.New("storage input lacks a device identity")
			}
			key := d.MajorMinor
			if key == "" {
				key = d.Producer
			}
			if identities[key] {
				return errors.New("duplicate storage device identity")
			}
			identities[key] = true
			seen[d.Path] = true
		}
		return nil
	}
	switch c.Type {
	case "mdadm":
		if !blockPath(c.Array) || c.Level != "raid1" || c.Metadata != "1.2" || len(c.Members) < 2 || c.VG != "" || c.LV != "" || len(c.Devices) > 0 || len(c.PVs) > 0 || c.Activate || c.ParentIdentity != "" {
			return errors.New("invalid prepared RAID1 request")
		}
		if err := validateDevices(c.Members); err != nil {
			return err
		}
		if c.Output.Path != c.Array || c.Output.Producer != "md:"+c.Identity {
			return errors.New("invalid prepared MD output identity")
		}
	case "lvm-vg":
		if !storageName.MatchString(c.VG) || c.VG == "." || c.VG == ".." || c.LV != "" || c.Array != "" || len(c.Members) > 0 || c.Filesystem != "" || c.SizeBytes != 0 || c.Activate || c.Assemble || c.ParentIdentity != "" || len(c.Devices) == 0 || len(c.PVs) != len(c.Devices) {
			return errors.New("invalid prepared VG request")
		}
		if c.Create && !storageTag.MatchString(c.Identity) {
			return errors.New("VG creation requires a prepared creation tag")
		}
		if err := validateDevices(c.Devices); err != nil {
			return err
		}
		seen := map[string]bool{}
		for i, pv := range c.PVs {
			if !storageID.MatchString(pv.UUID) || seen[pv.UUID] || pv.Device != c.Devices[i] {
				return errors.New("invalid prepared PV scope")
			}
			seen[pv.UUID] = true
		}
	case "lvm-lv":
		if !storageName.MatchString(c.VG) || !storageName.MatchString(c.LV) || c.VG == "." || c.VG == ".." || c.LV == "." || c.LV == ".." || c.Array != "" || len(c.Members) > 0 || len(c.Devices) > 0 || len(c.PVs) > 0 || c.SizeBytes == 0 || c.ParentIdentity == "" || !storageID.MatchString(c.ParentIdentity) && !storageTag.MatchString(c.ParentIdentity) || c.Assemble {
			return errors.New("invalid prepared linear LV request")
		}
		if c.Create && !storageTag.MatchString(c.Identity) {
			return errors.New("LV creation requires a prepared creation tag")
		}
		if c.Output.Path != "/dev/"+c.VG+"/"+c.LV || c.Output.Producer != "lv:"+c.Identity {
			return errors.New("invalid prepared LV output identity")
		}
	default:
		return errors.New("unsupported storage type")
	}
	return nil
}
