package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"golang.org/x/sys/unix"
)

type Store struct {
	Plan *Plan
	path string
	lock *flock.Flock
}

// Open holds a stable sidecar lock while progress replaces the plan atomically.
func Open(path string) (*Store, error) {
	path, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errors.New("plan must be a regular file, not a symlink")
	}
	l := flock.New(path+".lock", flock.SetPermissions(0600), flock.SetFlag(os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW))
	ok, err := l.TryLock()
	if err != nil {
		l.Close()
		return nil, err
	}
	if !ok {
		l.Close()
		return nil, errors.New("plan is already being applied")
	}
	s := &Store{path: path, lock: l}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		s.Close()
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		s.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink > 1 {
		s.Close()
		return nil, errors.New("plan must be a regular file without hardlink aliases")
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var p Plan
	if err = d.Decode(&p); err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = errors.New("plan contains trailing JSON")
		}
	} else {
		// JSON diagnostics can quote unknown fields, unexpected values, and
		// field paths. Executable plans contain secrets even when malformed.
		err = errors.New("invalid plan JSON")
	}
	if err == nil {
		err = p.Validate()
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("read plan: %w", err)
	}
	s.Plan = &p
	return s, nil
}
func (s *Store) Close() error       { return s.lock.Close() }
func (s *Store) Save(p *Plan) error { return save(s.path, p) }

// Save refuses to race an active apply of the same plan file.
func Save(path string, p *Plan) error {
	path, err := canonicalPath(path)
	if err != nil {
		return err
	}
	l := flock.New(path+".lock", flock.SetPermissions(0600), flock.SetFlag(os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW))
	ok, err := l.TryLock()
	if err != nil {
		l.Close()
		return err
	}
	defer l.Close()
	if !ok {
		return errors.New("plan is already being applied")
	}
	return save(path, p)
}
func save(path string, p *Plan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("plan must be a regular file, not a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".richt-plan-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}
