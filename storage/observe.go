package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type row map[string]string

func (b Backend) rows(ctx context.Context, command, columns, section string) ([]row, error) {
	args := []string{"--reportformat", "json", "--units", "b", "--nosuffix", "--options", columns}
	if command == "lvs" {
		args = append(args, "--segments")
	}
	out, err := b.run(ctx, command, args...)
	if err != nil {
		return nil, fmt.Errorf("observe %s: %w", command, err)
	}
	var data struct {
		Report []map[string][]row `json:"report"`
	}
	if err := json.Unmarshal(out, &data); err != nil || len(data.Report) != 1 {
		return nil, fmt.Errorf("invalid %s observation", command)
	}
	rows, ok := data.Report[0][section]
	if !ok {
		return nil, fmt.Errorf("incomplete %s observation", command)
	}
	for _, r := range rows {
		for k, v := range r {
			r[k] = strings.TrimSpace(v)
		}
	}
	return rows, nil
}

type lvmState struct{ pvs, vgs, lvs []row }

func (b Backend) lvm(ctx context.Context) (lvmState, error) {
	var s lvmState
	var err error
	s.pvs, err = b.rows(ctx, "pvs", "pv_name,pv_uuid,vg_name,vg_uuid", "pv")
	if err != nil {
		return s, err
	}
	s.vgs, err = b.rows(ctx, "vgs", "vg_name,vg_uuid,vg_tags,vg_extent_size,vg_free", "vg")
	if err != nil {
		return s, err
	}
	s.lvs, err = b.rows(ctx, "lvs", "vg_name,vg_uuid,lv_name,lv_uuid,lv_size,segtype,lv_tags,lv_path,lv_active", "seg")
	if err == nil {
		seen := map[string]row{}
		var unique []row
		for _, r := range s.lvs {
			if previous := seen[r["lv_uuid"]]; previous != nil {
				if r["segtype"] != "linear" {
					previous["segtype"] = r["segtype"]
				}
				continue
			}
			unique = append(unique, r)
			seen[r["lv_uuid"]] = r
		}
		s.lvs = unique
	}
	return s, err
}

func named(rows []row, key, value string) (row, error) {
	var found row
	for _, r := range rows {
		if r[key] != value {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("ambiguous LVM %s %s", key, value)
		}
		found = r
	}
	return found, nil
}

func (s lvmState) vg(name string) (row, error) { return named(s.vgs, "vg_name", name) }
func (s lvmState) lv(vg, lv string) (row, error) {
	var candidates []row
	for _, r := range s.lvs {
		if r["vg_name"] == vg {
			candidates = append(candidates, r)
		}
	}
	return named(candidates, "lv_name", lv)
}

func sameIdentity(r row, prefix, identity string) bool {
	if strings.HasPrefix(identity, "tag:") {
		for _, tag := range strings.Split(r[prefix+"_tags"], ",") {
			if tag == strings.TrimPrefix(identity, "tag:") {
				return true
			}
		}
		return false
	}
	return r[prefix+"_uuid"] == identity
}

func byteField(r row, key string) (uint64, error) {
	n, err := strconv.ParseUint(r[key], 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid LVM %s", key)
	}
	return n, nil
}

func export(out []byte) map[string]string {
	r := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			r[k] = v
		}
	}
	return r
}

func mdUUID(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(s))
}

func (b Backend) mdDetail(ctx context.Context, path string) (map[string]string, error) {
	out, err := b.run(ctx, "mdadm", "--detail", "--export", path)
	if err != nil {
		return nil, fmt.Errorf("observe MD array: %w", err)
	}
	r := export(out)
	if r["MD_UUID"] == "" || r["MD_LEVEL"] == "" {
		return nil, errors.New("incomplete MD array identity")
	}
	return r, nil
}

func (b Backend) mdMember(ctx context.Context, path string) (map[string]string, error) {
	sigs, err := b.signatures(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(sigs) == 0 {
		return nil, nil
	}
	for _, s := range sigs {
		if s.Type != "linux_raid_member" {
			return nil, fmt.Errorf("conflicting member signature on %s", path)
		}
	}
	out, err := b.run(ctx, "mdadm", "--examine", "--export", path)
	if err != nil {
		return nil, fmt.Errorf("observe MD member: %w", err)
	}
	r := export(out)
	if r["MD_UUID"] == "" || r["MD_LEVEL"] == "" {
		return nil, errors.New("incomplete MD member identity")
	}
	// mdadm's export omits version and slot on supported releases. Fix the
	// locale for these two anchored fields rather than interpreting localized
	// free-form descriptions or assuming that every member is an active slot.
	if r["MD_METADATA"] == "" || r["MD_DEVICE_ROLE"] == "" {
		detail, err := b.run(ctx, "env", "LC_ALL=C", "mdadm", "--examine", path)
		if err != nil {
			return nil, fmt.Errorf("observe MD member metadata: %w", err)
		}
		version := mdVersion.FindSubmatch(detail)
		role := mdRole.FindSubmatch(detail)
		if len(version) == 2 {
			r["MD_METADATA"] = string(version[1])
		}
		if len(role) == 2 {
			r["MD_DEVICE_ROLE"] = string(role[1])
		}
		if r["MD_METADATA"] == "" || r["MD_DEVICE_ROLE"] == "" {
			return nil, errors.New("incomplete MD member metadata observation")
		}
	}
	return r, nil
}

var mdVersion = regexp.MustCompile(`(?m)^\s*Version\s*:\s*(\S+)\s*$`)
var mdRole = regexp.MustCompile(`(?m)^\s*Device Role\s*:\s*Active device ([0-9]+)\s*$`)

func (b Backend) checkProducer(ctx context.Context, producer, path string) error {
	kind, identity, ok := strings.Cut(producer, ":")
	if !ok {
		return errors.New("invalid block device producer identity")
	}
	if kind == "md" {
		r, err := b.mdDetail(ctx, path)
		if err != nil {
			return err
		}
		if mdUUID(r["MD_UUID"]) != mdUUID(identity) {
			return errors.New("MD array identity changed")
		}
		return nil
	}
	if kind != "lv" {
		return errors.New("unsupported block producer")
	}
	s, err := b.lvm(ctx)
	if err != nil {
		return err
	}
	for _, r := range s.lvs {
		if !sameIdentity(r, "lv", identity) {
			continue
		}
		d, err := b.ProbeDevice(ctx, r["lv_path"])
		if err != nil {
			return err
		}
		actual, err := b.ProbeDevice(ctx, path)
		if err != nil {
			return err
		}
		if d.MajorMinor != actual.MajorMinor {
			return errors.New("LV path identity changed")
		}
		return nil
	}
	return errors.New("LV identity changed")
}
