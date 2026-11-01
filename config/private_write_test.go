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

func TestSensitiveExplicitModeRestrictsInodeBeforeWrite(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, finalMode := range []uint32{0640, 0400, 0000} {
			t.Run(fmt.Sprintf("existing-%t-mode-%04o", existing, finalMode), func(t *testing.T) {
				path := filepath.Join(directory(t), "target")
				var before os.FileInfo
				if existing {
					if err := os.WriteFile(path, []byte("old synthetic content"), 0666); err != nil {
						t.Fatal("could not create fixture")
					}
					if err := os.Chmod(path, 0666); err != nil {
						t.Fatal("could not set fixture mode")
					}
					before, _ = os.Stat(path)
				}
				f := &plan.FileChange{Path: path, State: "present", Content: "synthetic private content", Sensitive: true, Mode: &finalMode, Write: true}
				e := config.Local{}
				if err := e.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
					t.Fatal("private overwrite failed")
				}
				written, err := os.Stat(path)
				if err != nil || written.Mode().Perm() != 0600 {
					t.Fatal("file was not private immediately after overwrite")
				}
				if existing && !os.SameFile(before, written) {
					t.Fatal("overwrite replaced the existing inode")
				}
				content, err := os.ReadFile(path)
				if err != nil || string(content) != f.Content {
					t.Fatal("overwrite did not retain prepared content")
				}
				if err := e.Execute(context.Background(), plan.Action{Kind: "set-file-attributes", File: f}); err != nil {
					t.Fatal("final attributes failed")
				}
				final, err := os.Stat(path)
				if err != nil || final.Mode().Perm() != os.FileMode(finalMode) || !os.SameFile(written, final) {
					t.Fatal("final attributes did not restore the requested mode on the same inode")
				}
			})
		}
	}
}

func TestSensitiveWithoutModePreservesExistingPermissions(t *testing.T) {
	path := filepath.Join(directory(t), "target")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal("could not create fixture")
	}
	f := &plan.FileChange{Path: path, State: "present", Sensitive: true, Content: "synthetic private content"}
	if err := (config.Local{}).Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
		t.Fatal("overwrite failed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("sensitivity without explicit mode changed permissions")
	}
}
