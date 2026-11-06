package facts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/subuk/richt/source"
	"golang.org/x/sys/unix"
)

const DefaultMaxAge = 72 * time.Hour
const maxCacheBytes = 4 << 20

// ErrCacheIO identifies persistent cache failures, including publication errors.
var ErrCacheIO = errors.New("fact cache I/O failed; use --no-facts-cache to disable persistent caching")

// Options controls persistent caching. A zero MaxAge always requests collection.
type Options struct {
	CacheDir   string
	MaxAge     time.Duration
	Refresh    bool
	CachedOnly bool
	NoCache    bool
}

func (o Options) Validate() error {
	if o.MaxAge < 0 {
		return errors.New("facts max age cannot be negative")
	}
	if o.CachedOnly && (o.Refresh || o.NoCache) {
		return errors.New("cached facts cannot be combined with refresh or no-facts-cache")
	}
	return nil
}

// Identity contains configured inputs, rather than resolved SSH or DNS state.
// Inventory URLs are sanitized before hashing and are never stored in records.
type Identity struct {
	Inventory string
	Host      string
	Transport string
	Target    string
	User      string
	Port      int
	Sudo      bool
}

func (i Identity) Validate() error {
	_, err := canonicalIdentity(i)
	return err
}

func canonicalIdentity(i Identity) (Identity, error) {
	if i.Inventory == "" || i.Host == "" {
		return i, errors.New("fact cache identity requires inventory and host")
	}
	if i.Transport == "" {
		i.Transport = "local"
	}
	if i.Transport != "local" && i.Transport != "ssh" {
		return i, errors.New("invalid fact cache transport")
	}
	if i.Port < 0 || i.Port > 65535 {
		return i, errors.New("invalid fact cache port")
	}
	if i.Transport == "ssh" && i.Target == "" {
		return i, errors.New("fact cache SSH identity requires target")
	}
	if source.IsURL(i.Inventory) {
		u, err := url.Parse(i.Inventory)
		if err != nil {
			return i, errors.New("invalid fact inventory URL")
		}
		u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
		if err := source.ValidateURL(u.String()); err != nil {
			return i, errors.New("invalid fact inventory URL")
		}
		canonical, err := source.Resolve(u.String(), "")
		if err != nil {
			return i, err
		}
		i.Inventory = canonical
	} else {
		absolute, err := filepath.Abs(i.Inventory)
		if err != nil {
			return i, errors.New("invalid fact inventory path")
		}
		i.Inventory = absolute
	}
	return i, nil
}

type Result struct {
	Snapshot Snapshot
	Source   string
	Age      time.Duration
	Stale    bool
}

// Provider lazily fixes one logical snapshot (or error) for its invocation.
type Provider struct {
	options Options
	key     string
	collect func(context.Context) (Snapshot, error)
	mu      sync.Mutex
	done    chan struct{}
	result  Result
	err     error
	now     func() time.Time
}

func NewProvider(identity Identity, options Options, collect func(context.Context) (Snapshot, error)) (*Provider, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if collect == nil && !options.CachedOnly {
		return nil, errors.New("fact collector is required")
	}
	if options.NoCache {
		return &Provider{options: options, collect: collect, now: time.Now}, nil
	}
	i, err := canonicalIdentity(identity)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(struct {
		Identity          Identity
		Schema, Collector string
	}{i, SchemaVersion, CollectorVersion})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return &Provider{options: options, key: hex.EncodeToString(hash[:]), collect: collect, now: time.Now}, nil
}

func (p *Provider) Lookup(ctx context.Context, path string) (any, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	result, err := p.Get(ctx)
	if err != nil {
		return nil, err
	}
	return result.Snapshot.Lookup(path)
}

func (p *Provider) Get(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	p.mu.Lock()
	done := p.done
	if done == nil {
		p.done = make(chan struct{})
		done = p.done
		p.mu.Unlock()
		p.result, p.err = p.get(ctx)
		close(done)
	} else {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-done:
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if p.err != nil {
		return Result{}, p.err
	}
	// Callers cannot mutate the snapshot shared by other templates/components.
	result := p.result
	result.Snapshot = cloneSnapshot(result.Snapshot)
	return result, nil
}

func cloneSnapshot(s Snapshot) Snapshot {
	data, _ := json.Marshal(s)
	var copy Snapshot
	_ = json.Unmarshal(data, &copy)
	return copy
}

type cacheRecord struct {
	SchemaVersion    string   `json:"schema_version"`
	CollectorVersion string   `json:"collector_version"`
	Identity         string   `json:"identity"`
	Snapshot         Snapshot `json:"snapshot"`
}

// An invalid record is disposable; filesystem errors are always fatal.
var errInvalidRecord = errors.New("missing or invalid cached facts")

func cacheIO(err error) error { return fmt.Errorf("%w: %w", ErrCacheIO, err) }

func (p *Provider) cacheDir() (string, error) {
	if p.options.CacheDir != "" {
		return filepath.Abs(p.options.CacheDir)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "richt", "facts"), nil
}

func (p *Provider) get(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var dir string
	if !p.options.NoCache {
		var err error
		dir, err = p.cacheDir()
		if err != nil {
			return Result{}, cacheIO(err)
		}
		if !p.options.Refresh {
			s, err := p.read(dir)
			if err == nil {
				age := p.now().Sub(s.CollectedAt)
				stale := p.options.MaxAge == 0 || age >= p.options.MaxAge
				if p.options.CachedOnly || !stale {
					return Result{Snapshot: s, Source: "cache", Age: age, Stale: stale}, nil
				}
			} else if !errors.Is(err, errInvalidRecord) {
				return Result{}, cacheIO(err)
			}
			if p.options.CachedOnly {
				return Result{}, errInvalidRecord
			}
		}
	}
	s, err := p.collect(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	s.CollectedAt = s.CollectedAt.UTC()
	s = cloneSnapshot(s)
	s.Normalize()
	if err := s.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid collected facts: %w", err)
	}
	if s.CollectedAt.After(p.now()) {
		return Result{}, errors.New("collected facts have a future timestamp")
	}
	s = cloneSnapshot(s)
	if !p.options.NoCache {
		if err := p.publish(ctx, dir, s); err != nil {
			return Result{}, cacheIO(err)
		}
	}
	return Result{Snapshot: s, Source: "live", Age: p.now().Sub(s.CollectedAt)}, nil
}

func openCacheDir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (p *Provider) read(dir string) (Snapshot, error) {
	d, err := openCacheDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, errInvalidRecord
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer d.Close()
	info, err := d.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return Snapshot{}, errors.New("fact cache directory permissions must be private (0700)")
	}
	return p.readAt(d)
}

func (p *Provider) readAt(dir *os.File) (Snapshot, error) {
	fd, err := unix.Openat(int(dir.Fd()), p.key+".json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, errInvalidRecord
	}
	if err != nil {
		return Snapshot{}, err
	}
	f := os.NewFile(uintptr(fd), "facts record")
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Snapshot{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return Snapshot{}, errors.New("fact cache record must be a regular file without hardlink aliases")
	}
	if st.Mode&0077 != 0 {
		return Snapshot{}, errors.New("fact cache record permissions must be private (0600)")
	}
	if st.Size > maxCacheBytes {
		return Snapshot{}, errInvalidRecord
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxCacheBytes {
		return Snapshot{}, errInvalidRecord
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var record cacheRecord
	if d.Decode(&record) != nil {
		return Snapshot{}, errInvalidRecord
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Snapshot{}, errInvalidRecord
	}
	if record.SchemaVersion != SchemaVersion || record.CollectorVersion != CollectorVersion || record.Identity != p.key {
		return Snapshot{}, errInvalidRecord
	}
	record.Snapshot.CollectedAt = record.Snapshot.CollectedAt.UTC()
	if record.Snapshot.Validate() != nil || record.Snapshot.CollectedAt.After(p.now()) {
		return Snapshot{}, errInvalidRecord
	}
	return record.Snapshot, nil
}

func (p *Provider) publish(ctx context.Context, dir string, s Snapshot) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	d, err := openCacheDir(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	info, err := d.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("fact cache directory permissions must be private (0700)")
	}
	// A stable sidecar coordinates atomic replacement across invocations.
	lockPath := filepath.Join(dir, p.key+".lock")
	var lockStat unix.Stat_t
	if err := unix.Lstat(lockPath, &lockStat); err == nil && (lockStat.Mode&unix.S_IFMT != unix.S_IFREG || lockStat.Mode&0077 != 0 || lockStat.Nlink != 1) {
		return errors.New("fact cache lock must be a private regular file without hardlink aliases")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l := flock.New(lockPath, flock.SetPermissions(0600), flock.SetFlag(os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW))
	defer l.Close()
	locked, err := l.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	previous, err := p.readAt(d)
	if err == nil && previous.CollectedAt.After(s.CollectedAt) {
		return nil
	}
	if err != nil && !errors.Is(err, errInvalidRecord) {
		return err
	}
	record := cacheRecord{SchemaVersion, CollectorVersion, p.key, s}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data)+1 > maxCacheBytes {
		return errors.New("fact snapshot exceeds cache record size limit")
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := ".facts-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(int(d.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer unix.Unlinkat(int(d.Fd()), name, 0)
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat(int(d.Fd()), name, int(d.Fd()), p.key+".json"); err != nil {
		return err
	}
	return d.Sync()
}
