package inventory

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9_+.\-]*$`)
var arrayPathPattern = regexp.MustCompile(`^/dev/(md[0-9]+|md/[A-Za-z0-9_+][A-Za-z0-9_+.\-]*)$`)
var volumeSizePattern = regexp.MustCompile(`^[1-9][0-9]*(B|KiB|MiB|GiB|TiB|PiB|EiB|K|M|G|T|P|E)$`)

func validateStorageConfig(c Config) error {
	validPath := func(field, value string) error {
		if !path.IsAbs(value) || path.Clean(value) == "/" || strings.ContainsAny(value, "\x00\r\n\t ") {
			return fmt.Errorf("%s: expected an absolute resource path", field)
		}
		return nil
	}
	devices := func(field string, values []string, min int) error {
		if len(values) < min {
			return fmt.Errorf("%s: at least %d device(s) required", field, min)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if err := validPath(field, value); err != nil {
				return err
			}
			p := path.Clean(value)
			if !strings.HasPrefix(p, "/dev/") {
				return fmt.Errorf("%s: expected device paths under /dev", field)
			}
			if seen[p] {
				return fmt.Errorf("%s: duplicate device %q", field, value)
			}
			seen[p] = true
		}
		return nil
	}
	name := func(field, value string) error {
		if !volumeNamePattern.MatchString(value) || value == "." || value == ".." || strings.HasPrefix(value, "-") {
			return fmt.Errorf("%s: invalid LVM resource name", field)
		}
		return nil
	}
	forbidden := func(ok bool) error {
		if ok {
			return fmt.Errorf("%s config has incompatible fields", c.Type)
		}
		return nil
	}
	switch c.Type {
	case "mdadm":
		if err := forbidden(c.VG != "" || c.Devices != nil || c.LV != "" || c.Size != "" || c.Source != "" || c.Target != "" || c.Options != nil); err != nil {
			return err
		}
		if err := validPath("array", c.Array); err != nil {
			return err
		}
		if !arrayPathPattern.MatchString(path.Clean(c.Array)) {
			return fmt.Errorf("array: expected an MD device path")
		}
		if c.Level != "raid1" {
			return fmt.Errorf("level: expected raid1")
		}
		if c.Metadata != "1.2" {
			return fmt.Errorf("metadata: expected 1.2")
		}
		if err := devices("members", c.Members, 2); err != nil {
			return err
		}
		for _, member := range c.Members {
			if path.Clean(member) == path.Clean(c.Array) {
				return fmt.Errorf("members: array cannot contain itself")
			}
		}
	case "lvm-vg":
		if err := forbidden(c.Array != "" || c.Level != "" || c.Metadata != "" || c.Members != nil || c.LV != "" || c.Size != "" || c.Filesystem != "" || c.Source != "" || c.Target != "" || c.Options != nil); err != nil {
			return err
		}
		if err := name("vg", c.VG); err != nil {
			return err
		}
		return devices("devices", c.Devices, 1)
	case "lvm-lv":
		if err := forbidden(c.Array != "" || c.Level != "" || c.Metadata != "" || c.Members != nil || c.Devices != nil || c.Source != "" || c.Target != "" || c.Options != nil); err != nil {
			return err
		}
		if err := name("vg", c.VG); err != nil {
			return err
		}
		if err := name("lv", c.LV); err != nil {
			return err
		}
		if !volumeSizePattern.MatchString(c.Size) {
			return fmt.Errorf("size: expected a positive integral size such as 200GiB")
		}
	case "mount":
		if err := forbidden(c.Array != "" || c.Level != "" || c.Metadata != "" || c.Members != nil || c.VG != "" || c.Devices != nil || c.LV != "" || c.Size != ""); err != nil {
			return err
		}
		if err := validPath("source", c.Source); err != nil {
			return err
		}
		if err := validPath("target", c.Target); err != nil {
			return err
		}
		if c.Filesystem != "xfs" {
			return fmt.Errorf("filesystem: expected xfs")
		}
		seen := map[string]bool{}
		for _, option := range c.Options {
			if option == "" || strings.ContainsAny(option, " \t\r\n\x00,\\#") || seen[option] {
				return fmt.Errorf("options: expected unique nonempty mount options")
			}
			seen[option] = true
		}
	default:
		return fmt.Errorf("type: expected template, file, directory, mdadm, lvm-vg, lvm-lv or mount")
	}
	if c.Filesystem != "" && c.Filesystem != "xfs" {
		return fmt.Errorf("filesystem: expected xfs or omitted")
	}
	return nil
}
