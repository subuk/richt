package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
)

func TestFactTemplateTypedDefaultsAndStrictLookup(t *testing.T) {
	mapKey := 5
	snapshot := facts.Snapshot{}
	snapshot.Normalize()
	snapshot.Facts.CPU.LogicalCount = 4
	snapshot.Facts.Network.Interfaces["eth0.20"] = facts.Interface{Name: "eth0.20", MTU: 1500, Addresses: []facts.Address{}}
	for _, tc := range []struct {
		name, text, want string
		fail             bool
	}{
		{"typed integer", `{{printf "%d" (fact "cpu.logical_count")}}`, "4", false},
		{"known defaults", `{{printf "%q/%d" (fact "os.version_id") (fact "memory.total_bytes")}}`, `""/0`, false},
		{"literal interface", `{{with index (fact "network.interfaces") "eth0.20"}}{{.name}}:{{.mtu}}:{{.mac}}{{end}}`, "eth0.20:1500:", false},
		{"missing interface", `{{with index (fact "network.interfaces") "absent"}}bad{{else}}missing{{end}}`, "missing", false},
		{"unknown path", `{{printf "%v" (fact "cpu.typo")}}`, "", true},
		{"dynamic dotted path", `{{fact "network.interfaces.eth0.20.mtu"}}`, "", true},
		{"unknown interface field", `{{with index (fact "network.interfaces") "eth0.20"}}{{printf "%v" .mty}}{{end}}`, "", true},
		{"unknown indexed interface field", `{{printf "%v" (index (fact "network.interfaces") "eth0.20" "mty")}}`, "", true},
		{"unknown indexed schema field", `{{index (fact "cpu") "typo"}}`, "", true},
		{"existing variables", `{{.fact}}/{{.facts}}/{{index .list 1}}/{{index .numbers 2}}`, "variable/other/b/two", false},
		{"index ordinary missing", `{{with index .numbers 3}}bad{{else}}missing{{end}}`, "missing", false},
		{"index string", `{{printf "%c" (index "ab" 1)}}`, "b", false},
		{"pointer map key", `{{index .pointers .key}}`, "pointer", false},
		{"index bounds", `{{index .list 3}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := inventory.Config{Path: "/tmp/facts", Template: inventory.Template{Inline: &tc.text}}
			vars := map[string]any{"fact": "variable", "facts": "other", "list": []string{"a", "b"}, "numbers": map[int64]string{2: "two"}, "pointers": map[*int]string{&mapKey: "pointer"}, "key": &mapKey}
			f, err := PrepareWithOptions(context.Background(), c, "present", vars, PrepareOptions{Fact: func(_ context.Context, path string) (any, error) { return snapshot.Lookup(path) }})
			if tc.fail {
				if err == nil || f != nil {
					t.Fatalf("expected error, got %#v, %v", f, err)
				}
			} else if err != nil || f.Content != tc.want {
				t.Fatalf("got %#v, %v; want %q", f, err, tc.want)
			}
		})
	}
}

func TestFactTemplateLazinessAndSafeErrors(t *testing.T) {
	for _, state := range []string{"present", "absent"} {
		for _, text := range []string{`plain`, `{{if false}}{{fact "hostname"}}{{end}}`} {
			_, err := PrepareWithOptions(context.Background(), inventory.Config{Path: "/tmp/facts", Template: inventory.Template{Inline: &text}}, state, nil,
				PrepareOptions{Fact: func(context.Context, string) (any, error) { t.Fatal("unexpected fact lookup"); return nil, nil }})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, text := range []string{`{{fact (printf "%s" (secret "key"))}}`, `{{secret "key"}}{{fact "hostname"}}`, `{{fact "hostname"}}{{secret "key"}}`} {
		_, err := PrepareWithOptions(context.Background(), inventory.Config{Path: "/tmp/facts", Template: inventory.Template{Inline: &text}}, "present", nil, PrepareOptions{
			Secrets: factTestSecret{}, Fact: func(context.Context, string) (any, error) { return nil, errors.New("PRIVATE_TEST_CANARY") },
		})
		if err == nil || strings.Contains(err.Error(), "PRIVATE_TEST_CANARY") || strings.Contains(err.Error(), text) {
			t.Fatalf("unsafe fact error: %v", err)
		}
	}
	text := `{{fact "hostname"}}`
	_, err := PrepareWithOptions(context.Background(), inventory.Config{Path: "/tmp/facts", Sensitive: true, Template: inventory.Template{Inline: &text}}, "present", nil,
		PrepareOptions{Fact: func(context.Context, string) (any, error) {
			return nil, fmt.Errorf("%w: PRIVATE_TEST_CANARY", facts.ErrCacheIO)
		}})
	if err == nil || !strings.Contains(err.Error(), "--no-facts-cache") || strings.Contains(err.Error(), "PRIVATE_TEST_CANARY") {
		t.Fatalf("cache error recovery or safety lost: %v", err)
	}
}

type factTestSecret struct{}

func (factTestSecret) Lookup(string, string) (string, error) { return "PRIVATE_TEST_CANARY", nil }
