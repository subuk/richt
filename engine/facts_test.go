package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

type testFactTransport struct {
	transport.Transport
	collect func(context.Context) (facts.Snapshot, error)
}

func (f testFactTransport) Facts(ctx context.Context) (facts.Snapshot, error) { return f.collect(ctx) }

func testSnapshot(name string) facts.Snapshot {
	s := facts.Snapshot{SchemaVersion: facts.SchemaVersion, CollectedAt: time.Now().UTC(), Availability: map[string]facts.Availability{}}
	s.Normalize()
	s.Facts.Hostname = name
	s.Facts.Architecture = "x86_64"
	s.Facts.Kernel.Release = "6.12-test"
	s.Facts.CPU.LogicalCount = 2
	s.Facts.Memory.TotalBytes = 1024 * 1024
	for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like", "kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes", "network.interfaces", "network.default_ipv4", "network.default_ipv6"} {
		s.Availability[path] = facts.Availability{Status: "observed"}
	}
	return s
}

func TestFactsOneSnapshotPerHostAcrossConfigsUnitsAndDropIns(t *testing.T) {
	template := inventory.Template{Inline: inline("[Service]\nEnvironment=HOST={{fact \"hostname\"}}\n")}
	r := resolved(inventory.Service{
		Configs:   []inventory.Config{{Path: "/tmp/richt-facts", Template: template}},
		Processes: []inventory.Process{{Unit: "richt-facts.service", State: "stopped", UnitFile: &inventory.Config{Path: "/etc/systemd/system/richt-facts.service", Template: template}, Overrides: map[string]inventory.Config{"20-facts.conf": {Path: "/etc/systemd/system/richt-facts.service.d/20-facts.conf", Template: template}}}},
	})
	r.Hosts = append(r.Hosts, r.Hosts[0])
	r.Hosts[1].Name = "second"
	for i := range r.Hosts {
		r.Hosts[i].Transport.Host = r.Hosts[i].Name
	}
	calls := map[string]int{}
	e := Local{Files: &config.Target{
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
		RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
			return fileObservation("absent", ""), nil
		},
	}, Systemd: fakeSystemd("inactive", false)}
	e.Systemd.Glob = func(context.Context, string) ([]string, error) { return nil, nil }
	p, err := e.Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{NoCache: true}, FactTransport: func(c inventory.Transport) (transport.Transport, error) {
		return testFactTransport{collect: func(context.Context) (facts.Snapshot, error) { calls[c.Host]++; return testSnapshot(c.Host), nil }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range p.Hosts {
		files := 0
		for _, task := range h.Tasks {
			if task.File != nil {
				files++
				if !strings.Contains(task.File.Content, "HOST="+h.Name+"\n") {
					t.Fatal("cross-host fact leakage")
				}
			}
		}
		if files != 3 || calls[h.Name] != 1 {
			t.Fatalf("host %s: %d files, %d collections", h.Name, files, calls[h.Name])
		}
	}
}

func TestFactsSkippedTemplatesDoNotTouchCacheOrFactTransport(t *testing.T) {
	for _, tc := range []struct{ text, state string }{
		{"plain", "present"}, {`{{if false}}{{fact "hostname"}}{{end}}`, "present"}, {`{{fact "hostname"}}`, "absent"},
	} {
		r := resolved(inventory.Service{Configs: []inventory.Config{{Path: filepath.Join(tempDir(t), "missing"), Template: inventory.Template{Inline: inline(tc.text)}}}})
		r.Hosts[0].Services[0].State = tc.state
		_, err := (Local{}).Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{CacheDir: "/missing/invalid/fact-cache", MaxAge: time.Hour}, FactTransport: func(inventory.Transport) (transport.Transport, error) {
			t.Fatal("unexpected fact transport binding")
			return nil, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestFactCollectionFailurePrecedesResourceObservation(t *testing.T) {
	r := resolved(inventory.Service{Configs: []inventory.Config{{Path: "/tmp/richt-facts", Template: inventory.Template{Inline: inline(`{{fact "hostname"}}`)}}}})
	r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "unused"}
	bound := 0
	e := Local{Connect: func(inventory.Transport) (config.Target, error) { bound++; return config.Target{}, nil }}
	_, err := e.Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{NoCache: true}, FactTransport: func(inventory.Transport) (transport.Transport, error) {
		return testFactTransport{collect: func(context.Context) (facts.Snapshot, error) { return facts.Snapshot{}, errors.New("failure") }}, nil
	}})
	if err == nil || bound != 0 {
		t.Fatalf("collection failure must precede resource observation: %v, %d", err, bound)
	}
}

func TestFactCachePublicationFailureStopsPreparation(t *testing.T) {
	dir := tempDir(t)
	blocked := filepath.Join(dir, "cache")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	r := resolved(inventory.Service{Configs: []inventory.Config{{Path: filepath.Join(dir, "target"), Template: inventory.Template{Inline: inline(`{{fact "hostname"}}`)}}}})
	r.SourceIdentity = filepath.Join(dir, "inventory.yaml")
	calls := 0
	e := Local{Files: &config.Target{RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
		t.Fatal("cache publication failure reached file observation")
		return nil, nil
	}}}
	p, err := e.Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{CacheDir: blocked, Refresh: true, MaxAge: facts.DefaultMaxAge}, FactTransport: func(inventory.Transport) (transport.Transport, error) {
		return testFactTransport{collect: func(context.Context) (facts.Snapshot, error) { calls++; return testSnapshot("observed"), nil }}, nil
	}})
	if p != nil || err == nil || !strings.Contains(err.Error(), "--no-facts-cache") || calls != 1 {
		t.Fatalf("failed publication: plan=%v err=%v calls=%d", p, err, calls)
	}
}

func TestExcludedFactServiceDoesNotInitializeProvider(t *testing.T) {
	first, second := 10, 20
	dir := tempDir(t)
	in := &inventory.Inventory{
		Hosts: []inventory.Host{{Name: "local", Tags: []string{"all"}, Transport: inventory.Transport{Type: "local"}}},
		Tags:  map[string]inventory.Tag{"all": {Services: map[string]inventory.ServiceAssignment{"selected": {}, "excluded": {}}}},
		Services: map[string]inventory.ServiceDefinition{
			"selected": {Order: &first, Service: inventory.Service{Configs: []inventory.Config{{Path: filepath.Join(dir, "plain"), Template: inventory.Template{Inline: inline("plain")}}}}},
			"excluded": {Order: &second, Service: inventory.Service{Configs: []inventory.Config{{Path: filepath.Join(dir, "facts"), Template: inventory.Template{Inline: inline(`{{fact "hostname"}}`)}}}}},
		},
	}
	r, err := in.Resolve(inventory.Selectors{Services: []string{"selected"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Local{}).Diff(context.Background(), r, DiffOptions{FactTransport: func(inventory.Transport) (transport.Transport, error) {
		t.Fatal("excluded service initialized fact transport")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSavedFactPlanAndContinuationIgnoreChangedFactsAndDeletedCache(t *testing.T) {
	dir := tempDir(t)
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	r := resolved(inventory.Service{Configs: []inventory.Config{
		{Path: first, Template: inventory.Template{Inline: inline(`{{fact "hostname"}}`)}},
		{Path: second, Template: inventory.Template{Inline: inline(`{{fact "hostname"}}`)}},
	}})
	r.SourceIdentity = filepath.Join(dir, "inventory.yaml")
	cache := filepath.Join(dir, "cache")
	calls := 0
	p, err := (Local{}).Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{CacheDir: cache, MaxAge: facts.DefaultMaxAge}, FactTransport: func(inventory.Transport) (transport.Transport, error) {
		return testFactTransport{collect: func(context.Context) (facts.Snapshot, error) { calls++; return testSnapshot("frozen"), nil }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A cache hit must not even bind a transport to verify its target identity.
	if _, err := (Local{}).Diff(context.Background(), r, DiffOptions{Facts: &facts.Options{CacheDir: cache, MaxAge: facts.DefaultMaxAge}, FactTransport: func(inventory.Transport) (transport.Transport, error) {
		t.Fatal("cache hit bound fact transport")
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "plan.json")
	if err := plan.Save(saved, p); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	r.Hosts[0].Services[0].Definition.Configs[0].Template.Inline = inline("changed")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := plan.Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := plan.Apply(context.Background(), store.Plan, Local{}, store.Save); err == nil {
		t.Fatal("expected second file conflict")
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("manual after success"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), store.Plan, Local{}, store.Save); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil || string(b) != "frozen" {
		t.Fatalf("saved bytes changed: %q, %v", b, err)
	}
	b, err = os.ReadFile(first)
	if err != nil || string(b) != "manual after success" {
		t.Fatal("successful task was repeated")
	}
	if calls != 1 {
		t.Fatalf("collected %d times", calls)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("apply recreated facts cache")
	}
}
