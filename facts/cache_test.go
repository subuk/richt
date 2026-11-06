package facts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var cacheTestNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func cacheSnapshot(at time.Time) Snapshot {
	s := Snapshot{SchemaVersion: SchemaVersion, CollectedAt: at, Facts: Values{Hostname: "target", OS: OS{ID: "debian"}, Kernel: Kernel{Release: "6.8"}, Architecture: "x86_64", CPU: CPU{LogicalCount: 2}, Memory: Memory{TotalBytes: 1024}}}
	s.Normalize()
	for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like", "kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes", "network.interfaces", "network.default_ipv4", "network.default_ipv6"} {
		s.Availability[path] = Availability{Status: "observed"}
	}
	return s
}

func privateCacheDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "facts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func cacheIdentity() Identity {
	return Identity{Inventory: "inventory.yaml", Host: "host-a", Transport: "ssh", Target: "target", User: "deploy", Port: 22}
}

func cacheProvider(t *testing.T, dir string, age time.Duration, collect func(context.Context) (Snapshot, error)) *Provider {
	t.Helper()
	p, err := NewProvider(cacheIdentity(), Options{CacheDir: dir, MaxAge: age}, collect)
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return cacheTestNow }
	return p
}

func seedCache(t *testing.T, dir string, at time.Time) *Provider {
	t.Helper()
	p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { return cacheSnapshot(at), nil })
	if _, err := p.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCacheFreshnessModes(t *testing.T) {
	if DefaultMaxAge != 72*time.Hour {
		t.Fatalf("default max age = %s", DefaultMaxAge)
	}
	for _, tt := range []struct {
		name                     string
		age, timeToLive          time.Duration
		refresh, cached, noCache bool
		wantSource               string
		stale                    bool
	}{
		{"hit", time.Hour, DefaultMaxAge, false, false, false, "cache", false},
		{"before-boundary", DefaultMaxAge - time.Nanosecond, DefaultMaxAge, false, false, false, "cache", false},
		{"boundary", DefaultMaxAge, DefaultMaxAge, false, false, false, "live", false},
		{"expired", DefaultMaxAge + time.Hour, DefaultMaxAge, false, false, false, "live", false},
		{"zero", time.Hour, 0, false, false, false, "live", false},
		{"refresh", time.Hour, DefaultMaxAge, true, false, false, "live", false},
		{"disabled", time.Hour, DefaultMaxAge, false, false, true, "live", false},
		{"cached-stale", DefaultMaxAge + time.Hour, DefaultMaxAge, false, true, false, "cache", true},
		{"cached-fresh", time.Hour, DefaultMaxAge, false, true, false, "cache", false},
		{"cached-zero", time.Hour, 0, false, true, false, "cache", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := privateCacheDir(t)
			seedCache(t, dir, cacheTestNow.Add(-tt.age))
			calls := 0
			p := cacheProvider(t, dir, tt.timeToLive, func(context.Context) (Snapshot, error) { calls++; return cacheSnapshot(cacheTestNow), nil })
			p.options.Refresh, p.options.CachedOnly, p.options.NoCache = tt.refresh, tt.cached, tt.noCache
			r, err := p.Get(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if r.Source != tt.wantSource || r.Stale != tt.stale {
				t.Fatalf("result=%+v", r)
			}
			wantCalls := 0
			if tt.wantSource == "live" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d", calls)
			}
			if r.Source == "cache" && r.Age != tt.age {
				t.Fatalf("age=%s", r.Age)
			}
		})
	}
}

func TestCacheInvalidRecordsRefreshOrFailOffline(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "trailing", "unknown", "schema", "collector", "identity", "snapshot-version", "future", "defaults", "availability", "oversized"} {
		for _, cached := range []bool{false, true} {
			t.Run(kind+"/cached="+map[bool]string{true: "true", false: "false"}[cached], func(t *testing.T) {
				dir := privateCacheDir(t)
				seed := seedCache(t, dir, cacheTestNow.Add(-time.Hour))
				path := filepath.Join(dir, seed.key+".json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var record cacheRecord
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "missing":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "corrupt":
					data = []byte("{")
				case "trailing":
					data = append(data, []byte("{}")...)
				case "unknown":
					data = []byte(`{"unknown":true}`)
				case "schema":
					record.SchemaVersion = "future"
				case "collector":
					record.CollectorVersion = "future"
				case "identity":
					record.Identity = "other"
				case "snapshot-version":
					record.Snapshot.SchemaVersion = "future"
				case "future":
					record.Snapshot.CollectedAt = cacheTestNow.Add(time.Second)
				case "defaults":
					record.Snapshot.Facts.OS.IDLike = nil
				case "availability":
					record.Snapshot.Availability["not.a.fact"] = Availability{Status: "observed"}
				case "oversized":
					data = []byte(strings.Repeat(" ", maxCacheBytes+1))
				}
				if kind != "missing" {
					if kind != "corrupt" && kind != "trailing" && kind != "unknown" && kind != "oversized" {
						data, err = json.Marshal(record)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { calls++; return cacheSnapshot(cacheTestNow), nil })
				p.options.CachedOnly = cached
				r, err := p.Get(context.Background())
				if cached {
					if err == nil || calls != 0 {
						t.Fatalf("err=%v calls=%d", err, calls)
					}
				} else if err != nil || r.Source != "live" || calls != 1 {
					t.Fatalf("err=%v result=%+v calls=%d", err, r, calls)
				}
			})
		}
	}
}

func TestCacheIdentityIsolationAndSanitization(t *testing.T) {
	base := cacheIdentity()
	dir := privateCacheDir(t)
	p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { return cacheSnapshot(cacheTestNow), nil })
	seen := map[string]bool{p.key: true}
	for _, mutate := range []func(*Identity){
		func(i *Identity) { i.Inventory = "other.yaml" }, func(i *Identity) { i.Host = "host-b" }, func(i *Identity) { i.Transport = "local" }, func(i *Identity) { i.Target = "other" }, func(i *Identity) { i.User = "root" }, func(i *Identity) { i.Port = 2222 }, func(i *Identity) { i.Sudo = true },
	} {
		i := base
		mutate(&i)
		other, err := NewProvider(i, Options{}, p.collect)
		if err != nil {
			t.Fatal(err)
		}
		if seen[other.key] {
			t.Fatalf("identity collision: %+v", i)
		}
		seen[other.key] = true
	}
	i := base
	i.Inventory = "./inventory.yaml"
	other, err := NewProvider(i, Options{}, p.collect)
	if err != nil || other.key != p.key {
		t.Fatalf("canonical local identity: %v", err)
	}
	i.Inventory = "https://secret:password@EXAMPLE.COM:443/inventory.yaml?token=private#fragment"
	u, err := NewProvider(i, Options{CacheDir: dir}, p.collect)
	if err != nil {
		t.Fatal(err)
	}
	i.Inventory = "https://example.com/inventory.yaml"
	v, err := NewProvider(i, Options{}, p.collect)
	if err != nil || u.key != v.key {
		t.Fatalf("canonical URL identity: %v", err)
	}
	u.now = p.now
	if _, err := u.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, u.key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"secret", "password", "private", "example.com"} {
		if strings.Contains(string(data), s) {
			t.Fatalf("record contains inventory data %s", s)
		}
	}
}

func TestCacheLazyValidationAndDisabledPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	calls := 0
	p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { calls++; return cacheSnapshot(cacheTestNow), nil })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor touched cache: %v", err)
	}
	if _, err := p.Lookup(context.Background(), "unknown.path"); err == nil {
		t.Fatal("unknown path accepted")
	}
	if calls != 0 {
		t.Fatal("invalid path collected")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid lookup touched cache: %v", err)
	}
	p.options.NoCache = true
	if _, err := p.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-cache touched cache: %v", err)
	}
	// Disabled mode also ignores a persistent path which cannot be a directory.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p = cacheProvider(t, filepath.Join(file, "child"), DefaultMaxAge, p.collect)
	p.options.NoCache = true
	if _, err := p.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProvider(Identity{}, Options{NoCache: true}, p.collect); err != nil {
		t.Fatalf("disabled cache needs no identity: %v", err)
	}
}

func TestCacheOfflineMissingDoesNotCreateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	p, err := NewProvider(cacheIdentity(), Options{CacheDir: dir, MaxAge: DefaultMaxAge, CachedOnly: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(context.Background()); !errors.Is(err, errInvalidRecord) {
		t.Fatalf("offline missing error=%v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline lookup touched cache: %v", err)
	}
}

func TestCacheConcurrentWaiterCanCancel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := cacheProvider(t, privateCacheDir(t), DefaultMaxAge, func(context.Context) (Snapshot, error) {
		close(started)
		<-release
		return cacheSnapshot(cacheTestNow), nil
	})
	first := make(chan error, 1)
	go func() { _, err := p.Get(context.Background()); first <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { _, err := p.Get(ctx); waiter <- err }()
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error=%v", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("canceled waiter remained blocked")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("memoized result ignored cancellation: %v", err)
	}
	if _, err := p.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCacheFailedPublicationPreservesSuccessfulRecord(t *testing.T) {
	dir := privateCacheDir(t)
	seed := seedCache(t, dir, cacheTestNow.Add(-time.Hour))
	path := filepath.Join(dir, seed.key+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cacheSnapshot(cacheTestNow)
	s.Facts.Hostname = strings.Repeat("x", maxCacheBytes)
	p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { return s, nil })
	p.options.Refresh = true
	if _, err := p.Get(context.Background()); !errors.Is(err, ErrCacheIO) {
		t.Fatalf("publication failure=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed publication changed cache")
	}
}

func TestCacheMemoizesConcurrentSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "failure", false: "success"}[fail], func(t *testing.T) {
			var calls atomic.Int32
			failure := errors.New("collection failed")
			p := cacheProvider(t, privateCacheDir(t), DefaultMaxAge, func(context.Context) (Snapshot, error) {
				calls.Add(1)
				if fail {
					return Snapshot{}, failure
				}
				return cacheSnapshot(cacheTestNow), nil
			})
			var wg sync.WaitGroup
			for n := 0; n < 30; n++ {
				wg.Go(func() {
					r, err := p.Get(context.Background())
					if fail {
						if !errors.Is(err, failure) {
							t.Errorf("err=%v", err)
						}
						return
					}
					if err != nil {
						t.Error(err)
						return
					}
					r.Snapshot.Facts.OS.IDLike = append(r.Snapshot.Facts.OS.IDLike, "mutated")
					r.Snapshot.Availability["hostname"] = Availability{Status: "unavailable"}
				})
			}
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if !fail {
				r, err := p.Get(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if r.Snapshot.Availability["hostname"].Status != "observed" || len(r.Snapshot.Facts.OS.IDLike) != 0 {
					t.Fatal("callers mutated shared snapshot")
				}
			}
		})
	}
}

func TestCacheFailedRefreshPreservesSuccessfulRecord(t *testing.T) {
	dir := privateCacheDir(t)
	seed := seedCache(t, dir, cacheTestNow.Add(-time.Hour))
	path := filepath.Join(dir, seed.key+".json")
	before, _ := os.ReadFile(path)
	p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("unreachable") })
	p.options.Refresh = true
	if _, err := p.Get(context.Background()); err == nil {
		t.Fatal("failed refresh succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed refresh changed record")
	}
}

func TestCacheAtomicConcurrentPublicationKeepsNewest(t *testing.T) {
	dir := privateCacheDir(t)
	seed := seedCache(t, dir, cacheTestNow.Add(-time.Hour))
	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readErr <- nil
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(dir, seed.key+".json"))
			if err != nil {
				readErr <- err
				return
			}
			var r cacheRecord
			if err := json.Unmarshal(data, &r); err != nil {
				readErr <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for n := 0; n < 25; n++ {
		n := n
		wg.Go(func() {
			p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) {
				return cacheSnapshot(cacheTestNow.Add(-time.Duration(n) * time.Second)), nil
			})
			p.options.Refresh = true
			if _, err := p.Get(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(stop)
	if err := <-readErr; err != nil {
		t.Fatalf("torn read: %v", err)
	}
	s, err := seed.read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CollectedAt.Equal(cacheTestNow) {
		t.Fatalf("older publication replaced newest: %s", s.CollectedAt)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".facts-") {
			t.Fatalf("temporary file remains: %s", e.Name())
		}
	}
}

func TestCachePrivatePermissionsAndUnsafePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	p := seedCache(t, dir, cacheTestNow)
	for _, path := range []string{dir, filepath.Join(dir, p.key+".json"), filepath.Join(dir, p.key+".lock")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if path == dir {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s permissions=%o", path, info.Mode().Perm())
		}
	}
	for _, kind := range []string{"directory-symlink", "record-symlink", "record-hardlink", "record-directory", "lock-symlink", "lock-hardlink", "nonprivate-record", "nonprivate-directory", "unreadable-record", "write-parent-file"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateCacheDir(t)
			p := seedCache(t, dir, cacheTestNow)
			path := filepath.Join(dir, p.key+".json")
			refresh := false
			switch kind {
			case "directory-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				dir = alias
			case "record-symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Fatal(err)
				}
			case "record-hardlink":
				if err := os.Link(path, filepath.Join(dir, "alias")); err != nil {
					t.Fatal(err)
				}
			case "record-directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "lock-symlink":
				refresh = true
				lock := filepath.Join(dir, p.key+".lock")
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, lock); err != nil {
					t.Fatal(err)
				}
			case "nonprivate-record":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "nonprivate-directory":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "lock-hardlink":
				refresh = true
				if err := os.Link(filepath.Join(dir, p.key+".lock"), filepath.Join(dir, "alias")); err != nil {
					t.Fatal(err)
				}
			case "unreadable-record":
				if os.Geteuid() == 0 {
					t.Skip("root can read mode 0000")
				}
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
			case "write-parent-file":
				refresh = true
				parent := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(parent, nil, 0600); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(parent, "cache")
			}
			calls := 0
			p = cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { calls++; return cacheSnapshot(cacheTestNow), nil })
			p.options.Refresh = refresh
			if _, err := p.Get(context.Background()); !errors.Is(err, ErrCacheIO) {
				t.Fatalf("expected cache I/O error, got %v", err)
			}
			if refresh && calls != 1 {
				t.Fatalf("publication failure must follow successful collection, calls=%d", calls)
			}
		})
	}
}

func TestCacheInvalidOptionsAndSnapshots(t *testing.T) {
	for _, o := range []Options{{MaxAge: -1}, {CachedOnly: true, Refresh: true}, {CachedOnly: true, NoCache: true}} {
		if _, err := NewProvider(cacheIdentity(), o, nil); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	for _, i := range []Identity{{}, {Inventory: "a", Host: "a", Transport: "invalid"}, {Inventory: "a", Host: "a", Transport: "ssh"}, {Inventory: "a", Host: "a", Port: -1}, {Inventory: "http://example.com/a", Host: "a"}} {
		if err := i.Validate(); err == nil {
			t.Fatalf("accepted %+v", i)
		}
	}
	for _, kind := range []string{"version", "timestamp", "quantity", "future"} {
		t.Run(kind, func(t *testing.T) {
			s := cacheSnapshot(cacheTestNow)
			switch kind {
			case "version":
				s.SchemaVersion = ""
			case "timestamp":
				s.CollectedAt = time.Time{}
			case "quantity":
				s.Facts.Memory.TotalBytes = -1
			case "future":
				s.CollectedAt = cacheTestNow.Add(time.Hour)
			}
			dir := filepath.Join(t.TempDir(), "absent")
			p := cacheProvider(t, dir, DefaultMaxAge, func(context.Context) (Snapshot, error) { return s, nil })
			if _, err := p.Get(context.Background()); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid snapshot was persisted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	p := cacheProvider(t, privateCacheDir(t), DefaultMaxAge, func(context.Context) (Snapshot, error) { calls++; return Snapshot{}, nil })
	if _, err := p.Get(ctx); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancellation err=%v calls=%d", err, calls)
	}
}

func TestCacheTypedLookupReusesSnapshot(t *testing.T) {
	calls := 0
	p := cacheProvider(t, privateCacheDir(t), DefaultMaxAge, func(context.Context) (Snapshot, error) { calls++; return cacheSnapshot(cacheTestNow), nil })
	for path, want := range map[string]any{"os.id": "debian", "cpu.logical_count": int64(2), "network.interfaces": map[string]any{}} {
		got, err := p.Lookup(context.Background(), path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got=%#v err=%v", path, got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
