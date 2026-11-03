package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

func executePath(t *testing.T, p *plan.PathChange) error {
	t.Helper()
	actions, err := (Local{}).ResolvePath(plan.Task{Operation: "path", Path: p})
	if err != nil {
		return err
	}
	return (Local{}).ExecutePath(context.Background(), actions[0])
}

func TestPlainFilePreservesContentsAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent", "marker")
	p, err := PreparePath(inventory.Config{Type: "file", Path: path}, "present")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ObservePath(p)
	if err != nil || !changed {
		t.Fatalf("missing observation: %v %v", changed, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("observation created parents: %v", err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatalf("new file mode: %v", info.Mode())
	}
	if err := os.WriteFile(path, []byte("unmanaged contents"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "unmanaged contents" {
		t.Fatalf("file truncated: %q", data)
	}
	unchanged, _ := PreparePath(inventory.Config{Type: "file", Path: path}, "present")
	if changed, err := ObservePath(unchanged); err != nil || changed {
		t.Fatalf("contents caused diff: %v %v", changed, err)
	}
	attrs, _ := PreparePath(inventory.Config{Type: "file", Path: path, Mode: "0600"}, "present")
	if changed, err := ObservePath(attrs); err != nil || !changed {
		t.Fatalf("attributes: %v %v", changed, err)
	}
	if err := executePath(t, attrs); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	data, _ = os.ReadFile(path)
	if info.Mode().Perm() != 0600 || string(data) != "unmanaged contents" {
		t.Fatal("attribute management altered content")
	}
}

func TestFileAppearingAfterPreparationIsNeverTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker")
	p, _ := PreparePath(inventory.Config{Type: "file", Path: path}, "present")
	if _, err := ObservePath(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("appeared later"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "appeared later" {
		t.Fatal("newly appeared file truncated")
	}
}

func TestDirectoryAttributesAreNotRecursiveAndAbsentDeletesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent", "data")
	p, _ := PreparePath(inventory.Config{Type: "directory", Path: path}, "present")
	if _, err := ObservePath(p); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0755 {
		t.Fatalf("directory default mode: %v", info.Mode())
	}
	child := filepath.Join(path, "child")
	if err := os.WriteFile(child, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: path, Mode: "0700"}, "present")
	if changed, err := ObservePath(p); err != nil || !changed {
		t.Fatalf("attrs: %v %v", changed, err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(child)
	if info.Mode().Perm() != 0600 {
		t.Fatal("child mode changed")
	}
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: path}, "present")
	if changed, err := ObservePath(p); err != nil || changed {
		t.Fatalf("directory contents caused diff: %v %v", changed, err)
	}
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: path}, "absent")
	if _, err := ObservePath(p); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("directory retained: %v", err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatalf("continuation after deletion: %v", err)
	}
}

func TestPathDeletionProtectsReplacementAndUnlinksNamedSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	original := filepath.Join(dir, "original")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := PreparePath(inventory.Config{Type: "file", Path: path}, "absent")
	if _, err := ObservePath(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err == nil {
		t.Fatal("replacement deleted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "replacement" {
		t.Fatal("replacement altered")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: link}, "absent")
	if _, err := ObservePath(p); err != nil {
		t.Fatal(err)
	}
	if err := executePath(t, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("named symlink retained")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("symlink target contents removed")
	}
}

func TestPathIncompatibleTypesAndMissingObservation(t *testing.T) {
	dir := t.TempDir()
	p, _ := PreparePath(inventory.Config{Type: "file", Path: dir}, "present")
	if _, err := ObservePath(p); err == nil {
		t.Fatal("file accepted directory")
	}
	if err := executePath(t, p); err == nil {
		t.Fatal("file replaced directory")
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: path}, "present")
	if _, err := ObservePath(p); err == nil {
		t.Fatal("directory accepted file")
	}
	if changed, err := (Local{}).ObservePathMissing(context.Background(), p); err != nil || !changed {
		t.Fatalf("hidden file observed: %v %v", changed, err)
	}
}

func TestTargetPathCreationPreservesAppearedContentsAndDirectoryChildren(t *testing.T) {
	target, dir := shellTarget(t)
	ctx := context.Background()
	path := filepath.Join(dir, "parents", "marker ' $(literal)")
	p, _ := PreparePath(inventory.Config{Type: "file", Path: path}, "present")
	if changed, err := target.ObservePath(ctx, p); err != nil || !changed {
		t.Fatalf("missing observation: %v %v", changed, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("appeared since diff"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := target.ExecutePath(ctx, plan.Action{Kind: "ensure-path", Path: p}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "appeared since diff" {
		t.Fatal("target truncated existing file")
	}
	directory := filepath.Join(dir, "data")
	p, _ = PreparePath(inventory.Config{Type: "directory", Path: directory, Mode: "0700"}, "present")
	if err := target.ExecutePath(ctx, plan.Action{Kind: "ensure-path", Path: p}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(directory, "child")
	if err := os.WriteFile(child, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := target.ExecutePath(ctx, plan.Action{Kind: "ensure-path", Path: p}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(child)
	if info.Mode().Perm() != 0644 {
		t.Fatal("target recursively changed child mode")
	}
}

func TestTargetPathExistingObservationUsesRealStatRecords(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "darwin" {
		// Adapt only BSD permission spelling, preserving the requested record
		// delimiters so the actual stat command verifies the output protocol.
		wrapper := `#!/bin/sh
format=$(printf '%s' "$2" | sed 's/%a/%OLp/g')
if [ "$1" = -Lc ]; then exec /usr/bin/stat -L -f "$format" "$4"; fi
exec /usr/bin/stat -f "$format" "$4"
`
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	client, err := transport.New(inventory.Transport{Type: "local"})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Run: client.Run, RunInput: client.RunInput}
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("unmanaged"), 0640); err != nil {
		t.Fatal(err)
	}
	p, _ := PreparePath(inventory.Config{Type: "file", Path: path, Mode: "0640"}, "present")
	if changed, err := target.ObservePath(context.Background(), p); err != nil || changed || p.Identity == nil {
		t.Fatalf("existing target metadata: %v %v %#v", changed, err, p)
	}
}
