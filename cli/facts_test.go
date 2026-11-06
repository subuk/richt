package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
)

func factsInventory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	data := `tags:
  production: {}
  testing: {}
services: {}
hosts:
  - {name: z, tags: [production], transport: {type: ssh, host: z.invalid}}
  - {name: b, tags: [testing], transport: {type: ssh, host: b.invalid}}
  - {name: a, tags: [production], transport: {type: ssh, host: a.invalid}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func fakeSnapshot() facts.Snapshot {
	s := facts.Snapshot{SchemaVersion: facts.SchemaVersion, CollectedAt: time.Now().UTC()}
	s.Normalize()
	s.Facts.Kernel.Release = "6.1"
	s.Facts.Architecture = "x86_64"
	s.Facts.Hostname = "example"
	s.Facts.CPU.LogicalCount = 2
	s.Facts.Memory.TotalBytes = 1024
	for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like", "kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes", "network.interfaces", "network.default_ipv4", "network.default_ipv6"} {
		s.Availability[path] = facts.Availability{Status: "observed"}
	}
	return s
}
func TestFactsSelectionFailuresAndConcurrency(t *testing.T) {
	path := factsInventory(t)
	var active, maximum atomic.Int32
	collector := func(ctx context.Context, c inventory.Transport) (facts.Snapshot, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		if c.Host == "b.invalid" {
			return facts.Snapshot{}, errors.New("collection failed")
		}
		s := fakeSnapshot()
		s.Facts.Hostname = c.Host
		return s, nil
	}
	f := factsArguments{factsSelection: factsSelection{Inventory: path}, FactsMaxAge: facts.DefaultMaxAge, NoFactsCache: true, Parallelism: 2, Progress: "plain"}
	var out, stderr bytes.Buffer
	err := executeFacts(context.Background(), f, &out, &stderr, collector)
	if err == nil || !strings.Contains(err.Error(), "1 host(s)") {
		t.Fatalf("aggregate failure: %v", err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("parallelism: %d", maximum.Load())
	}
	var result factsOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON stdout: %v: %s", err, out.String())
	}
	if len(result.Hosts) != 3 || result.Hosts[0].Host != "a" || result.Hosts[1].Host != "b" || result.Hosts[2].Host != "z" || result.Hosts[0].Snapshot == nil || result.Hosts[1].Error == "" || result.Hosts[2].Snapshot == nil {
		t.Fatalf("independent sorted hosts: %+v", result.Hosts)
	}
	if !strings.Contains(stderr.String(), "Failed facts host=b") {
		t.Fatal(stderr.String())
	}
	f.Hosts, f.Tags = []string{"b"}, []string{"production"}
	out.Reset()
	if err := executeFacts(context.Background(), f, &out, &stderr, collector); err == nil {
		t.Fatal("host/tag union lost failed host")
	}
	f.Hosts, f.Tags = nil, []string{"production"}
	out.Reset()
	if err := executeFacts(context.Background(), f, &out, &stderr, collector); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Hosts) != 2 {
		t.Fatalf("tag selection: %v %s", err, out.String())
	}
}
func TestFactsCachedAndInvalidModesDoNoTargetWork(t *testing.T) {
	path := factsInventory(t)
	var calls atomic.Int32
	collector := func(context.Context, inventory.Transport) (facts.Snapshot, error) {
		calls.Add(1)
		return fakeSnapshot(), nil
	}
	f := factsArguments{factsSelection: factsSelection{Inventory: path, Hosts: []string{"a"}}, FactsCacheDir: filepath.Join(t.TempDir(), "facts"), FactsMaxAge: facts.DefaultMaxAge, Parallelism: 1, Progress: "off"}
	var out, stderr bytes.Buffer
	if err := executeFacts(context.Background(), f, &out, &stderr, collector); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	f.Cached = true
	f.FactsMaxAge = 0
	out.Reset()
	if err := executeFacts(context.Background(), f, &out, &stderr, collector); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(out.String(), `"source": "cache"`) || !strings.Contains(out.String(), `"stale": true`) {
		t.Fatalf("cached inspection: calls=%d %s", calls.Load(), out.String())
	}
	f.Hosts = []string{"z"}
	out.Reset()
	if err := executeFacts(context.Background(), f, &out, &stderr, collector); err == nil || calls.Load() != 1 {
		t.Fatalf("missing cache contacted target: %v calls=%d", err, calls.Load())
	}
	for _, variant := range []struct {
		refresh, noCache bool
		age              time.Duration
		parallel         int
	}{{true, false, time.Hour, 1}, {false, true, time.Hour, 1}, {false, false, -time.Second, 1}, {false, false, time.Hour, 0}} {
		f.Refresh, f.NoFactsCache, f.FactsMaxAge, f.Parallelism = variant.refresh, variant.noCache, variant.age, variant.parallel
		f.Inventory = "/does/not/exist"
		out.Reset()
		err := executeFacts(context.Background(), f, &out, &stderr, collector)
		if err == nil || strings.Contains(err.Error(), "does/not/exist") || calls.Load() != 1 {
			t.Fatalf("validation did not precede inventory/target: %v", err)
		}
	}
}
func TestFactsCLIFlagsAndSavedPlanRejection(t *testing.T) {
	path := factsInventory(t)
	code, out, stderr := run(t, "facts", "-i", path, "--cached", "--host", "a", "--facts-cache-dir", t.TempDir(), "--progress=off")
	if code == 0 || !json.Valid([]byte(out)) || !strings.Contains(stderr, "facts failed") {
		t.Fatalf("facts flags: %d %s %s", code, out, stderr)
	}
	if code, _, _ := run(t, "facts", "-i", path, "--service", "unused"); code != 2 {
		t.Fatalf("facts accepted service selector: %d", code)
	}
	for _, flags := range [][]string{{"--facts-max-age=72h"}, {"--facts-max-age=0"}, {"--refresh-facts"}, {"--refresh-facts=false"}, {"--no-facts-cache"}, {"--no-facts-cache=false"}, {"--facts-cache-dir", t.TempDir()}} {
		args := append([]string{"apply", "/missing-plan.json", "-y"}, flags...)
		code, _, stderr := run(t, args...)
		if code == 0 || !strings.Contains(stderr, "facts preparation options cannot") {
			t.Fatalf("saved plan accepted %v: %d %s", flags, code, stderr)
		}
	}
	for _, command := range []string{"diff", "apply"} {
		code, _, stderr := run(t, command, "-i", "/missing-inventory.yaml", "--facts-max-age=-1s")
		if code == 0 || strings.Contains(stderr, "missing-inventory") {
			t.Fatalf("invalid age not rejected before load: %d %s", code, stderr)
		}
	}
}
func TestFactsCanceledRunReportsHostsWithoutCollecting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := factsArguments{factsSelection: factsSelection{Inventory: factsInventory(t)}, FactsMaxAge: facts.DefaultMaxAge, NoFactsCache: true, Parallelism: 2, Progress: "off"}
	var out, stderr bytes.Buffer
	var calls atomic.Int32
	err := executeFacts(ctx, f, &out, &stderr, func(context.Context, inventory.Transport) (facts.Snapshot, error) {
		calls.Add(1)
		return facts.Snapshot{}, fmt.Errorf("unexpected collection")
	})
	if err == nil || calls.Load() != 0 {
		t.Fatal("canceled facts succeeded or collected target facts")
	}
}
