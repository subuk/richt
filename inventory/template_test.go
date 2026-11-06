package inventory

import (
	"strings"
	"testing"
)

func TestTemplateSourcesAreExclusiveAndPinned(t *testing.T) {
	text := ""
	pin := strings.Repeat("a", 64)
	for _, template := range []Template{{File: "template"}, {Inline: &text}, {URL: "https://example.invalid/template", SHA256: pin}} {
		if err := ValidateTemplate(template); err != nil {
			t.Fatalf("valid template rejected: %#v: %v", template, err)
		}
	}
	for _, template := range []Template{
		{}, {File: "file", Inline: &text}, {File: "file", URL: "https://example.invalid/template", SHA256: pin}, {Inline: &text, URL: "https://example.invalid/template", SHA256: pin},
		{File: "file", SHA256: pin}, {Inline: &text, SHA256: pin}, {URL: "https://example.invalid/template"}, {URL: "https://example.invalid/template", SHA256: "invalid"},
		{URL: "relative.gotmpl", SHA256: pin}, {URL: "http://example.invalid/template", SHA256: pin}, {URL: "https://user:password@example.invalid/template", SHA256: pin},
		{URL: "https://example.invalid/template?credential=value", SHA256: pin}, {URL: "https://example.invalid/template#fragment", SHA256: pin},
	} {
		if err := ValidateTemplate(template); err == nil {
			t.Fatalf("invalid template accepted: %#v", template)
		}
	}
}

func TestURLTemplateFieldsRemainExclusiveToTemplateConfigs(t *testing.T) {
	for _, typ := range []string{"file", "directory", "mdadm", "lvm-vg", "lvm-lv", "mount", "user", "group"} {
		for _, template := range []Template{{URL: "https://example.invalid/template", SHA256: strings.Repeat("a", 64)}, {SHA256: strings.Repeat("a", 64)}} {
			c := Config{Type: typ, Path: "/etc/app", User: "app", Group: "app", Template: template}
			if err := validateConfig(c); err == nil {
				t.Fatalf("%s accepted URL template fields", typ)
			}
		}
	}
	c := Config{Template: Template{URL: "https://example.invalid/unit", SHA256: strings.Repeat("A", 64)}, Path: "/etc/systemd/system/app.service"}
	if err := validateOwnedConfig(c); err != nil {
		t.Fatalf("owned unit URL template rejected: %v", err)
	}
}
