package inventoryyaml

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountConfigLoadRetainsOmission(t *testing.T) {
	for _, external := range []bool{false, true} {
		in, err := loadServiceFixture(t, `configs:
  - {type: group, group: app, gid: 1001, system: false}
  - {type: user, user: app, group: app}
  - {type: user, user: worker, group: app, uid: 1002, home: /nonexistent, shell: /usr/sbin/nologin, comment: "", supplementary_groups: [], append_groups: false}
  - {type: user, user: operator, group: app, supplementary_groups: [wheel], append_groups: true}
`, external)
		if err != nil {
			t.Fatal(err)
		}
		configs := in.Services["app"].Configs
		if configs[0].System == nil || *configs[0].System || configs[0].GID == nil || *configs[0].GID != 1001 {
			t.Fatal("group presence lost")
		}
		if configs[1].UID != nil || configs[1].System != nil || configs[1].Comment != nil || configs[1].SupplementaryGroups != nil || configs[1].AppendGroups != nil {
			t.Fatal("omission lost")
		}
		if configs[2].Comment == nil || *configs[2].Comment != "" || configs[2].SupplementaryGroups == nil || len(*configs[2].SupplementaryGroups) != 0 {
			t.Fatal("explicit empty values lost")
		}
		if configs[2].AppendGroups == nil || *configs[2].AppendGroups || configs[3].AppendGroups == nil || !*configs[3].AppendGroups {
			t.Fatal("explicit membership modes lost")
		}
		data, err := json.Marshal(configs[2])
		if err != nil || !strings.Contains(string(data), `"supplementary_groups":[]`) || !strings.Contains(string(data), `"comment":""`) || !strings.Contains(string(data), `"append_groups":false`) {
			t.Fatalf("empty intent lost in JSON: %s: %v", data, err)
		}
	}
}

func TestAccountConfigStrictSchema(t *testing.T) {
	for _, config := range []string{
		`{type: user, user: app, group: app, append_groups: false}`,
		`{type: user, user: app, group: app, append_groups: true}`,
		`{type: user, user: app, group: app, supplementary_groups: [], append_groups: null}`,
		`{type: user, user: app, group: app, supplementary_groups: [], append_groups: "true"}`,
		`{type: user, user: app, group: app, supplementary_groups: [], append_groups: 1}`,
		`{type: group, group: app, append_groups: false}`,
		`{type: file, path: /tmp/file, append_groups: false}`,
		`{type: directory, path: /tmp/dir, append_groups: true}`,
		`{type: mount, source: /dev/sda, target: /mnt/data, append_groups: false}`,
		`{type: user, user: app, group: app, gid: null}`,
		`{type: user, user: app, group: app, path: ""}`,
		`{type: user, user: app, group: app, sensitive: false}`,
		`{type: user, user: app, group: app, template: null}`,
		`{type: group, group: app, user: ""}`,
		`{type: group, group: app, uid: null}`,
		`{type: group, group: app, members: []}`,
		`{type: group, group: app, supplementary_groups: null}`,
		`{type: group, group: app, comment: ""}`,
		`{type: user, user: app, group: app, uid: "1001"}`,
		`{type: user, user: app, group: app, uid: null}`,
		`{type: user, user: app, group: app, uid: -1}`,
		`{type: user, user: app, group: app, uid: 4294967295}`,
		`{type: user, user: app, group: app, uid: 4294967296}`,
		`{type: user, user: app, group: app, system: "false"}`,
		`{type: user, user: app, group: app, system: null}`,
		`{type: user, user: app, group: app, home: ""}`,
		`{type: user, user: app, group: app, shell: null}`,
		`{type: user, user: app, group: app, comment: null}`,
		`{type: user, user: app, group: app, supplementary_groups: null}`,
		`{type: user, user: app, group: app, supplementary_groups: [123]}`,
		`{type: file, path: /tmp/file, uid: null}`,
		`{type: directory, path: /tmp/dir, supplementary_groups: []}`,
		`{type: user, user: app, group: 123}`,
	} {
		for _, external := range []bool{false, true} {
			if _, err := loadServiceFixture(t, "configs: ["+config+"]\n", external); err == nil {
				t.Fatalf("invalid config accepted: %s", config)
			}
		}
	}
}
