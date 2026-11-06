package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

// Exercise target command execution on development platforms. File metadata
// comes from the actual helper; account fixtures resolve to this process's IDs.
// Plain-path tests retain GNU-specific stat/sync stand-ins.
func shellTarget(t *testing.T) (Target, string) {
	t.Helper()
	dir := t.TempDir()
	commands := map[string]string{
		"stat": `printf '640\n417\n418\n'`,
		"getent": fmt.Sprintf(`case "$1:$3" in
passwd:target-user) printf 'target-user:x:%d:%d::/:/bin/sh\n';;
group:target-group) printf 'target-group:x:%d:\n';;
*) exit 1;; esac`, os.Getuid(), os.Getgid(), os.Getgid()),
		"sync":  `exit 0`,
		"chown": `printf '%s\n' "$@" > "$TARGET_CHOWN"`,
	}
	for name, script := range commands {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TARGET_CHOWN", filepath.Join(dir, "chown-args"))
	c, err := transport.New(inventory.Transport{Type: "local"})
	if err != nil {
		t.Fatal(err)
	}
	return Target{Run: c.Run, RunInput: c.RunInput}, dir
}

func TestTargetObservationUsesTargetAccountsAndContent(t *testing.T) {
	target, dir := shellTarget(t)
	path := filepath.Join(dir, "managed ' $(literal)")
	if err := os.WriteFile(path, []byte("old\x00content\n"), 0640); err != nil {
		t.Fatal(err)
	}
	mode := uint32(0640)
	f := &plan.FileChange{Path: path, State: "present", Owner: "target-user", Group: "target-group", Mode: &mode, Content: "old\x00content\n"}
	changed, err := target.Observe(context.Background(), f)
	if err != nil || changed || f.Owner != "target-user" || f.Group != "target-group" {
		t.Fatalf("unchanged target with named ownership = %v %v, %#v", changed, err, f)
	}
	f.Content = "prepared\ncontent\n"
	changed, err = target.Observe(context.Background(), f)
	if err != nil || !changed || !f.Write || !strings.Contains(f.Patch, "+prepared") {
		t.Fatalf("changed target = %v %v, %#v", changed, err, f)
	}
	f.Owner = "missing-target-user"
	if _, err := target.Observe(context.Background(), f); err == nil || !strings.Contains(err.Error(), "owner does not exist on target") {
		t.Fatalf("missing target owner = %v", err)
	}
}

func TestTargetCreateReplaceFinalLinksReadGlobAndRemove(t *testing.T) {
	target, dir := shellTarget(t)
	ctx := context.Background()
	path := filepath.Join(dir, "parents", "managed.conf")
	f := &plan.FileChange{Path: path, State: "present", Content: "secret\x00\n'$(literal)\n"}
	changed, err := target.Observe(ctx, f)
	if err != nil || !changed || !f.Write {
		t.Fatalf("missing target = %v %v", changed, err)
	}
	actions, err := target.Resolve(plan.Task{Operation: "file", File: f})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if err := target.Execute(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	content, err := target.ReadFile(ctx, path)
	if err != nil || string(content) != f.Content {
		t.Fatalf("read content = %q %v", content, err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	f.Path, f.Content, f.AtomicWrites = link, "replacement of link", true
	if _, err := target.Observe(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := target.Execute(ctx, plan.Action{Kind: "overwrite-file", File: f}); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	entry, statErr := os.Lstat(link)
	if string(content) == f.Content || statErr != nil || !entry.Mode().IsRegular() {
		t.Fatal("target write followed the final symlink instead of replacing it")
	}
	paths, err := target.Glob(ctx, filepath.Join(dir, "*.conf"))
	if err != nil || !reflect.DeepEqual(paths, []string{link}) {
		t.Fatalf("Glob = %q %v", paths, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	f.State, f.Content = "absent", ""
	if changed, err := target.Observe(ctx, f); err != nil || !changed || f.Expected.Kind != "symlink" {
		t.Fatalf("symlink absence = %v %v patch=%q", changed, err, f.Patch)
	}
	if err := target.Execute(ctx, plan.Action{Kind: "remove-file", File: f}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link still exists: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("remove followed symlink: %v", err)
	}
}

func TestTargetOwnershipPreservesUnspecifiedSide(t *testing.T) {
	target, dir := shellTarget(t)
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	for _, tt := range []struct{ owner, group string }{
		{"target-user", ""}, {"", "target-group"}, {"target-user", "target-group"},
	} {
		f := &plan.FileChange{Path: path, State: "present", Content: "unchanged", Owner: tt.owner, Group: tt.group}
		if _, err := target.Observe(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		if err := target.Execute(context.Background(), plan.Action{Kind: "set-file-attributes", File: f}); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(path)
		oldIDs, newIDs := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
		if oldIDs.Uid != newIDs.Uid || oldIDs.Gid != newIDs.Gid || after.Mode().Perm() != 0600 || !os.SameFile(before, after) {
			t.Fatal("explicit ownership changed the unspecified owner/group, mode or inode")
		}
	}
}

func TestTargetRejectsDirectoryAndChecksMissingOwnerBeforeCreate(t *testing.T) {
	target, dir := shellTarget(t)
	f := &plan.FileChange{Path: dir, State: "present"}
	if _, err := target.Observe(context.Background(), f); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory observation = %v", err)
	}
	f.Path, f.Owner = filepath.Join(dir, "missing"), "missing-target-user"
	if _, err := target.Observe(context.Background(), f); err == nil || !strings.Contains(err.Error(), "owner does not exist") {
		t.Fatalf("missing owner = %v", err)
	}
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Fatal("observation created target")
	}
}

func TestSensitiveTargetErrorsAndInput(t *testing.T) {
	marker := "synthetic-confidential-marker\nsecond-line"
	f := &plan.FileChange{Path: "/example", State: "present", Content: marker, Sensitive: true, Write: true}
	target := Target{
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
		RunInput: func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
			var request fileRequest
			if err := json.Unmarshal(input, &request); err != nil || request.File == nil || request.File.Content != marker {
				t.Fatal("prepared file content was not sent through stdin", err)
			}
			if strings.Contains(name+strings.Join(args, " "), marker) {
				t.Fatal("file content was included in target command arguments")
			}
			return nil, errors.New(marker)
		},
	}
	if _, err := target.Observe(context.Background(), f); err == nil || err.Error() != "sensitive file observation failed" {
		t.Fatal("sensitive target observation error was not protected")
	}
	if err := freezeFile(f, &plan.FileState{Kind: "absent"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"overwrite-file", "set-file-attributes", "remove-file"} {
		if err := target.Execute(context.Background(), plan.Action{Kind: kind, File: f}); err == nil || err.Error() != "sensitive file operation failed" {
			t.Fatal("sensitive target command error was not protected")
		}
	}
}

func TestSensitiveTargetPublishesFinalMode(t *testing.T) {
	target, dir := shellTarget(t)
	for _, atomic := range []bool{false, true} {
		for _, finalMode := range []uint32{0640, 0400, 0000} {
			t.Run(fmt.Sprintf("atomic-%t-mode-%04o", atomic, finalMode), func(t *testing.T) {
				path := filepath.Join(dir, fmt.Sprintf("target-%t-%04o", atomic, finalMode))
				if err := os.WriteFile(path, []byte("old synthetic content"), 0666); err != nil {
					t.Fatal(err)
				}
				before, _ := os.Stat(path)
				f := &plan.FileChange{Path: path, State: "present", Content: "synthetic private content", Sensitive: true, Mode: &finalMode, AtomicWrites: atomic}
				if _, err := target.Observe(context.Background(), f); err != nil {
					t.Fatal(err)
				}
				if err := target.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err != nil {
					t.Fatal("private target write failed:", err)
				}
				written, err := os.Stat(path)
				if err != nil || written.Mode().Perm() != os.FileMode(finalMode) || os.SameFile(before, written) == atomic {
					t.Fatal("target did not publish final mode with requested inode behavior", err)
				}
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(path)
				if err != nil || string(content) != f.Content {
					t.Fatal("target lost prepared content", err)
				}
			})
		}
	}
}

func TestSensitiveTargetConflictPrecedesContentReplacement(t *testing.T) {
	target, dir := shellTarget(t)
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("old synthetic content"), 0644); err != nil {
		t.Fatal(err)
	}
	mode := uint32(0600)
	f := &plan.FileChange{Path: path, State: "present", Content: "synthetic-confidential-marker", Sensitive: true, Mode: &mode, AtomicWrites: true}
	if _, err := target.Observe(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := target.Execute(context.Background(), plan.Action{Kind: "overwrite-file", File: f}); err == nil || err.Error() != "sensitive file operation failed" {
		t.Fatal("conflict diagnostic was not protected", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "external content" {
		t.Fatal("content changed before the prepared precondition succeeded")
	}
}
