// Package storage implements the RAID1, linear LVM, and optional XFS config
// resources using the same command boundary for local and SSH execution.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/rpm"
)

type Backend struct{ Run rpm.Runner }

func (b Backend) run(ctx context.Context, command string, args ...string) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, command, args...)
	}
	return rpm.Run(ctx, command, args...)
}

var ErrDeviceAbsent = errors.New("block device is absent")

// Preparation carries only the effects of preceding configs, in their declared
// execution order. It never selects or reorders prerequisite resources.
type Preparation struct {
	Outputs   map[string]plan.DeviceIdentity
	Removed   map[string]bool
	Unmounted map[string]bool
	claims    map[string]string
	groups    map[string]row
}

func (p *Preparation) init() {
	if p.Outputs == nil {
		p.Outputs = map[string]plan.DeviceIdentity{}
	}
	if p.Removed == nil {
		p.Removed = map[string]bool{}
	}
	if p.Unmounted == nil {
		p.Unmounted = map[string]bool{}
	}
	if p.claims == nil {
		p.claims = map[string]string{}
	}
	if p.groups == nil {
		p.groups = map[string]row{}
	}
}

func (p *Preparation) Lookup(path string) (plan.DeviceIdentity, bool) {
	if p == nil {
		return plan.DeviceIdentity{}, false
	}
	d, ok := p.Outputs[filepath.Clean(path)]
	return d, ok
}

func (p *Preparation) publish(path string, d plan.DeviceIdentity) {
	p.init()
	p.Outputs[filepath.Clean(path)] = d
	p.Outputs[d.Path] = d
}

func (p *Preparation) claim(d plan.DeviceIdentity, owner string) error {
	p.init()
	key := d.MajorMinor
	if key == "" {
		key = d.Path
	}
	if old, ok := p.claims[key]; ok {
		return fmt.Errorf("block device %s is already claimed by %s", d.Path, old)
	}
	p.claims[key] = owner
	return nil
}

type block struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	MajorMinor string    `json:"maj:min"`
	Size       uint64    `json:"size"`
	Serial     string    `json:"serial"`
	WWN        string    `json:"wwn"`
	Filesystem string    `json:"fstype"`
	UUID       string    `json:"uuid"`
	Mounts     []*string `json:"mountpoints"`
	Children   []block   `json:"children"`
}

const canonicalScript = `if [ -b "$1" ]; then readlink -f -- "$1"; elif [ -e "$1" ] || [ -L "$1" ]; then exit 64; fi`

func (b Backend) device(ctx context.Context, path string) (plan.DeviceIdentity, block, error) {
	out, err := b.run(ctx, "sh", "-c", canonicalScript, "richt-storage", path)
	if err != nil {
		return plan.DeviceIdentity{}, block{}, fmt.Errorf("inspect block device %s: %w", path, err)
	}
	canonical := strings.TrimSpace(string(out))
	if canonical == "" {
		return plan.DeviceIdentity{}, block{}, ErrDeviceAbsent
	}
	out, err = b.run(ctx, "lsblk", "--json", "--bytes", "--paths", "--output", "NAME,TYPE,MAJ:MIN,SIZE,SERIAL,WWN,FSTYPE,UUID,MOUNTPOINTS", "--", canonical)
	if err != nil {
		return plan.DeviceIdentity{}, block{}, fmt.Errorf("inspect block device %s: %w", path, err)
	}
	var data struct {
		Devices []block `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &data); err != nil || len(data.Devices) != 1 {
		return plan.DeviceIdentity{}, block{}, errors.New("incomplete lsblk device observation")
	}
	s := data.Devices[0]
	if s.Name == "" || s.MajorMinor == "" || s.Size == 0 {
		return plan.DeviceIdentity{}, block{}, errors.New("incomplete block device identity")
	}
	d := plan.DeviceIdentity{Path: canonical, MajorMinor: s.MajorMinor, SizeBytes: s.Size, Serial: s.Serial, WWN: s.WWN, Filesystem: s.Filesystem, UUID: s.UUID}
	// lsblk's udev filesystem cache may lag a preceding formatter. Read the
	// actual device signatures so the next config observes the produced state.
	sigs, err := b.signatures(ctx, canonical)
	if err != nil {
		return d, s, err
	}
	d.Filesystem, d.UUID = "", ""
	if len(sigs) == 1 {
		d.Filesystem, d.UUID = sigs[0].Type, sigs[0].UUID
	}
	if s.Type == "loop" {
		out, err = b.run(ctx, "sh", "-c", `IFS= read -r p < "/sys/dev/block/$1/loop/backing_file" || exit; printf '%s\n' "$p"; stat -Lc '%d:%i' -- "$p"`, "richt-storage", s.MajorMinor)
		if err != nil || len(strings.Fields(string(out))) < 2 {
			return d, s, errors.New("cannot establish loop backing-file identity")
		}
		d.BackingFile = strings.TrimSpace(string(out))
	}
	if s.Type == "raid1" {
		r, err := b.mdDetail(ctx, canonical)
		if err != nil {
			return d, s, err
		}
		d.Producer = "md:" + r["MD_UUID"]
	} else if s.Type == "lvm" {
		state, err := b.lvm(ctx)
		if err != nil {
			return d, s, err
		}
		for _, lv := range state.lvs {
			out, err := b.run(ctx, "sh", "-c", canonicalScript, "richt-storage", lv["lv_path"])
			if err != nil {
				return d, s, err
			}
			if strings.TrimSpace(string(out)) == canonical {
				d.Producer = "lv:" + lv["lv_uuid"]
				break
			}
		}
		if d.Producer == "" {
			return d, s, errors.New("cannot establish LVM block device identity")
		}
	}
	return d, s, nil
}

func (b Backend) ProbeDevice(ctx context.Context, path string) (plan.DeviceIdentity, error) {
	d, _, err := b.device(ctx, path)
	return d, err
}

func (b Backend) CheckDevice(ctx context.Context, expected plan.DeviceIdentity) error {
	d, _, err := b.device(ctx, expected.Path)
	if err != nil {
		return err
	}
	if expected.Producer != "" {
		if err := b.checkProducer(ctx, expected.Producer, d.Path); err != nil {
			return err
		}
	} else {
		if expected.WWN != "" && expected.WWN != d.WWN || expected.Serial != "" && expected.Serial != d.Serial || expected.BackingFile != "" && expected.BackingFile != d.BackingFile || expected.SizeBytes != d.SizeBytes {
			return fmt.Errorf("block device identity changed: %s", expected.Path)
		}
		// A filesystem UUID also provides identity for mounting an external
		// source. Raw initialization requires physical identity separately.
		if expected.WWN == "" && expected.Serial == "" && expected.BackingFile == "" && expected.UUID == "" {
			return fmt.Errorf("block device %s lacks stable WWN, serial, backing-file, or filesystem identity", expected.Path)
		}
	}
	if expected.Filesystem != "" && expected.Filesystem != d.Filesystem || expected.UUID != "" && expected.UUID != d.UUID {
		return fmt.Errorf("filesystem identity changed: %s", expected.Path)
	}
	return nil
}

func persistentInput(d plan.DeviceIdentity) error {
	if d.Producer == "" && d.WWN == "" && d.Serial == "" && d.BackingFile == "" {
		return fmt.Errorf("raw block device %s lacks a stable WWN, serial, or loop backing-file identity; refusing storage mutation", d.Path)
	}
	return nil
}

type signature struct {
	Type string `json:"type"`
	UUID string `json:"uuid"`
}

func (b Backend) signatures(ctx context.Context, path string) ([]signature, error) {
	out, err := b.run(ctx, "wipefs", "--json", "--output", "TYPE,UUID", "--", path)
	if err != nil {
		return nil, fmt.Errorf("inspect device signatures: %w", err)
	}
	var data struct {
		Signatures []signature `json:"signatures"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, errors.New("invalid device signature observation")
	}
	return data.Signatures, nil
}

func consumers(s block, p *Preparation) error {
	for _, m := range s.Mounts {
		if m != nil && *m != "" && (p == nil || !p.Unmounted[*m]) {
			return fmt.Errorf("block device %s is mounted at %s", s.Name, *m)
		}
	}
	for _, child := range s.Children {
		if p == nil || !p.Removed[child.Name] && !p.Removed["dev:"+child.MajorMinor] {
			return fmt.Errorf("block device %s has unrelated consumer %s", s.Name, child.Name)
		}
	}
	return nil
}

func (b Backend) blank(ctx context.Context, d plan.DeviceIdentity, p *Preparation) error {
	if err := b.CheckDevice(ctx, d); err != nil {
		return err
	}
	_, s, err := b.device(ctx, d.Path)
	if err != nil {
		return err
	}
	if err := consumers(s, p); err != nil {
		return err
	}
	sigs, err := b.signatures(ctx, d.Path)
	if err != nil {
		return err
	}
	if len(sigs) != 0 {
		return fmt.Errorf("device %s contains signatures; refusing initialization", d.Path)
	}
	return nil
}
