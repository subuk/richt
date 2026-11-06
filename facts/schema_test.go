package facts

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"
)

func completeSchemaSnapshot() Snapshot {
	s := Snapshot{SchemaVersion: SchemaVersion, CollectedAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)}
	s.Facts.Kernel.Release = "target-kernel"
	s.Facts.Architecture = "aarch64"
	s.Facts.Hostname = "target"
	s.Facts.CPU.LogicalCount = 4
	s.Facts.Memory.TotalBytes = 1024
	s.Normalize()
	for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like"} {
		s.Availability[path] = Availability{Status: "unavailable", Reason: "attribute_absent"}
	}
	for _, path := range []string{"kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes", "network.interfaces", "network.default_ipv4", "network.default_ipv6"} {
		s.Availability[path] = Availability{Status: "observed"}
	}
	return s
}

func TestSnapshotDefaultsAndTypedLookup(t *testing.T) {
	s := completeSchemaSnapshot()
	for path, want := range map[string]any{"os.id": "", "os.version_id": "", "os.name": "", "os.id_like": []string{}, "cpu.logical_count": int64(4), "memory.total_bytes": int64(1024), "network.interfaces": map[string]any{}, "network.default_ipv4.status": "absent", "network.default_ipv4.interface": ""} {
		got, err := s.Lookup(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %#v,%v want %#v", path, got, err, want)
		}
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") || !strings.Contains(string(b), `"id_like":[]`) {
		t.Fatal(string(b))
	}
	for _, path := range []string{"", "cpu.nope", "network.interfaces.eth0", "network.interfaces.eth0.20.mtu", "architecture.x", "network.default_ipv7", "OS.id", "os..id"} {
		if err := ValidatePath(path); err == nil {
			t.Fatal("unknown path accepted", path)
		}
	}
}

func TestInterfaceLiteralKeysAndStrictSchemaRecords(t *testing.T) {
	s := completeSchemaSnapshot()
	s.Facts.Network.Interfaces["eth0.20"] = Interface{Name: "eth0.20", MTU: 1500, Addresses: []Address{{Family: "inet", Address: "192.0.2.2", PrefixLength: 24}}}
	fn := func(path string) (any, error) { return s.Lookup(path) }
	for _, tt := range []struct {
		text, want string
		fail       bool
	}{
		{`{{ with index (fact "network.interfaces") "eth0.20" }}{{ .name }} {{ .mtu }}{{ range .addresses }} {{ .address }}/{{ .prefix_length }}{{ end }}{{ end }}`, "eth0.20 1500 192.0.2.2/24", false},
		{`{{ with index (fact "network.interfaces") "missing" }}unexpected{{ end }}`, "", false},
		{`{{ with index (fact "network.interfaces") "eth0.20" }}{{ .mty }}{{ end }}`, "", true},
		{`{{ printf "%v" (fact "cpu.typo") }}`, "", true},
	} {
		tmpl, err := template.New("test").Option("missingkey=error").Funcs(template.FuncMap{"fact": fn}).Parse(tt.text)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		err = tmpl.Execute(&b, nil)
		if (err != nil) != tt.fail || !tt.fail && b.String() != tt.want {
			t.Fatalf("%s: %q %v", tt.text, b.String(), err)
		}
	}
	value, _ := s.Lookup("network.interfaces")
	if _, ok := value.(map[string]any)["eth0.20"].(Record); !ok {
		t.Fatal("schema record type lost")
	}
}

func TestLookupDoesNotMutateSnapshot(t *testing.T) {
	s := Snapshot{Facts: Values{Network: Network{Interfaces: map[string]Interface{"empty": {Name: "empty"}}}}}
	if _, err := s.Lookup("network.interfaces"); err != nil {
		t.Fatal(err)
	}
	if s.Facts.Network.Interfaces["empty"].Addresses != nil || s.Facts.OS.IDLike != nil || s.Facts.Network.DefaultIPv4.Status != "" {
		t.Fatal("lookup mutated snapshot")
	}
}

func TestInvalidSnapshotIntegrity(t *testing.T) {
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.SchemaVersion = "future" }, func(s *Snapshot) { s.CollectedAt = time.Time{} }, func(s *Snapshot) { s.Availability = nil }, func(s *Snapshot) { s.Availability["unknown"] = Availability{Status: "observed"} }, func(s *Snapshot) { s.Availability["os.id"] = Availability{Status: "unavailable"} }, func(s *Snapshot) { s.Availability["hostname"] = Availability{Status: "unavailable", Reason: "missing"} }, func(s *Snapshot) { s.Facts.Memory.TotalBytes = 0 }, func(s *Snapshot) { s.Facts.CPU.LogicalCount = -1 }, func(s *Snapshot) {
			s.Facts.Network.Interfaces["wrong"] = Interface{Name: "different", Addresses: []Address{}}
		}, func(s *Snapshot) {
			s.Facts.Network.DefaultIPv4 = DefaultRoute{Status: "selected", Interface: "missing"}
		}, func(s *Snapshot) { s.Facts.Network.DefaultIPv4 = DefaultRoute{Status: "ambiguous", Interface: "eth0"} }, func(s *Snapshot) {
			s.Facts.Network.Interfaces["eth0"] = Interface{Name: "eth0", Addresses: []Address{{Family: "inet", Address: "2001:db8::1", PrefixLength: 24}}}
		},
	} {
		s := completeSchemaSnapshot()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
