package inventoryyaml

import (
	"fmt"
	"strings"
	"testing"
)

func TestTypedConfigsLoadWithoutObservingTargets(t *testing.T) {
	configs := []string{
		`{path: /not-present/template, template: {file: missing-template}}`,
		`{type: template, path: /not-present/template2, template: {inline: text}}`,
		`{type: file, path: /not-present/file, owner: not-a-user}`,
		`{type: directory, path: /not-present/directory, mode: "0750"}`,
		`{type: mdadm, array: /dev/md/data, level: raid1, metadata: "1.2", members: [/dev/not-a-disk, /dev/not-b-disk]}`,
		`{type: lvm-vg, vg: vg_data, devices: [/dev/md/data]}`,
		`{type: lvm-lv, vg: vg_data, lv: data, size: 200GiB, filesystem: xfs}`,
		`{type: mount, source: /dev/vg_data/data, target: /srv/data, filesystem: xfs, options: [defaults, noatime]}`,
	}
	for _, external := range []bool{false, true} {
		for _, named := range []bool{false, true} {
			service := "configs:\n"
			for i, c := range configs {
				if named {
					c = strings.Replace(c, "{", fmt.Sprintf("{name: item%d, ", i), 1)
				}
				service += "  - " + c + "\n"
			}
			in, err := loadServiceFixture(t, service, external)
			if err != nil {
				t.Fatal(err)
			}
			if len(in.Services["app"].Configs) != len(configs) {
				t.Fatal("configs lost")
			}
		}
	}
}

func TestTypedConfigStrictFieldPresenceAndSubset(t *testing.T) {
	for _, config := range []string{
		`{type: file, path: /tmp/file, template: null}`,
		`{type: directory, path: /tmp/dir, sensitive: false}`,
		`{type: file, path: /tmp/file, members: []}`,
		`{type: mdadm, array: /dev/md/a, level: raid1, metadata: "1.2", members: [/dev/a, /dev/b], owner: ""}`,
		`{type: lvm-vg, vg: data, devices: [/dev/a], filesystem: ""}`,
		`{type: lvm-lv, vg: data, lv: data, size: 1GiB, options: []}`,
		`{type: lvm-lv, vg: data, lv: data, size: "100"}`,
		`{type: mount, source: /dev/a, target: /srv/a, filesystem: xfs, path: ""}`,
		`{type: mount, source: /dev/a, target: /srv/a, filesystem: {type: xfs}}`,
		`{type: mount, source: /dev/a, target: /srv/a, filesystem: ext4}`,
		`{type: mdadm, array: /dev/md/a, level: raid5, metadata: "1.2", members: [/dev/a, /dev/b]}`,
		`{type: mdadm, array: /dev/md/a, level: raid1, metadata: "1.2", members: [/dev/a, /dev/./a]}`,
	} {
		for _, external := range []bool{false, true} {
			if _, err := loadServiceFixture(t, "configs: ["+config+"]\n", external); err == nil {
				t.Fatalf("invalid config accepted: %s", config)
			}
		}
	}
	for _, field := range []string{"array: ''", "members: []", "filesystem: ''", "type: directory"} {
		service := "processes: [{type: systemd, unit: app.service, unit_file: {" + field + ", template: {inline: text}}}]\n"
		if _, err := loadServiceFixture(t, service, false); err == nil {
			t.Fatalf("systemd accepted storage fields: %s", field)
		}
	}
}
