package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

// Exercise the actual target shell scripts on every development platform. Only
// GNU-specific metadata/account/sync commands are replaced; real Linux coverage
// belongs to the existing acceptance harness.
func shellTarget(t *testing.T) (Target, string) {
	t.Helper()
	dir := t.TempDir()
	commands := map[string]string{
		"stat": `printf '640\n417\n418\n'`,
		"getent": `case "$1:$3" in
passwd:target-user) printf 'target-user:x:417:418::/:/bin/sh\n';;
group:target-group) printf 'target-group:x:418:\n';;
*) exit 1;; esac`,
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
	if err != nil || changed || f.UID != nil || f.GID != nil {
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

func TestTargetCreateFollowLinksReadGlobAndRemove(t *testing.T) {
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
	f.Path, f.Content = link, "updated through link"
	if err := target.Execute(ctx, plan.Action{Kind: "overwrite-file", File: f}); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != f.Content {
		t.Fatal("ordinary symlink write did not follow target")
	}
	paths, err := target.Glob(ctx, filepath.Join(dir, "*.conf"))
	if err != nil || !reflect.DeepEqual(paths, []string{link}) {
		t.Fatalf("Glob = %q %v", paths, err)
	}
	f.State = "absent"
	if changed, err := target.Observe(ctx, f); err != nil || !changed || f.Patch != "" {
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
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ owner, group, ownership string }{
		{"target-user", "", "+417"},
		{"", "target-group", ":+418"},
		{"target-user", "target-group", "+417:+418"},
	} {
		f := &plan.FileChange{Path: path, State: "present", Owner: tt.owner, Group: tt.group}
		if err := target.Execute(context.Background(), plan.Action{Kind: "set-file-attributes", File: f}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "chown-args"))
		want := "--\n" + tt.ownership + "\n" + path + "\n"
		if err != nil || string(got) != want {
			t.Fatalf("ownership command = %q %v, want %q", got, err, want)
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
