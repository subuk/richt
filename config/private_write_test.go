package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/plan"
)

func TestSensitiveWritePublishesFinalAttributes(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			for _, finalMode := range []uint32{0640, 0400, 0000} {
				t.Run(fmt.Sprintf("atomic-%t-existing-%t-mode-%04o", atomic, existing, finalMode), func(t *testing.T) {
					path := filepath.Join(directory(t), "target")
					var before os.FileInfo
					if existing {
						if err := os.WriteFile(path, []byte("old synthetic content"), 0666); err != nil {
							t.Fatal(err)
						}
						before, _ = os.Stat(path)
					}
					f := &plan.FileChange{Path: path, State: "present", Content: "synthetic private content", Sensitive: true, Mode: &finalMode, AtomicWrites: atomic}
					if _, err := config.Observe(f); err != nil {
						t.Fatal(err)
					}
					if err := (config.Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
						t.Fatal("private write failed:", err)
					}
					written, err := os.Stat(path)
					if err != nil || written.Mode().Perm() != os.FileMode(finalMode) {
						t.Fatal("published file lacks final requested mode", err)
					}
					if existing && os.SameFile(before, written) == atomic {
						t.Fatal("inode replacement does not match atomic_writes")
					}
					// Mode000 verifies publication and durability do not reopen the
					// file for reading after applying final permissions.
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					content, err := os.ReadFile(path)
					if err != nil || string(content) != f.Content {
						t.Fatal("write did not retain prepared content", err)
					}
				})
			}
		}
	}
}

func TestSensitiveWithoutModePreservesExistingPermissions(t *testing.T) {
	path := filepath.Join(directory(t), "target")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	f := &plan.FileChange{Path: path, State: "present", AtomicWrites: true, Sensitive: true, Content: "synthetic private content"}
	if _, err := config.Observe(f); err != nil {
		t.Fatal(err)
	}
	if err := (config.Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
		t.Fatal("overwrite failed:", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("sensitivity without explicit mode changed permissions", err)
	}
}
