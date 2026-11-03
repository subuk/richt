package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/plan"
)

func TestLocalObservationUsesPrecedingAccountIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings")
	if err := os.WriteFile(path, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	f := &plan.FileChange{Path: path, State: "present", Content: "same", Owner: "richt-not-created-yet", Group: "richt-group-not-created-yet"}
	if _, err := Observe(f); err == nil {
		t.Fatal("unmanaged missing owner was accepted")
	}
	if changed, err := Observe(f, Ownership{OwnerID: &uid, GroupID: &gid}); err != nil || changed {
		t.Fatalf("projected unchanged identity: %v %v", changed, err)
	}
	if changed, err := Observe(f, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || f.Write {
		t.Fatalf("pending ownership must schedule attributes only: %v %v %#v", changed, err, f)
	}
	other := uid + 1
	if changed, err := Observe(f, Ownership{OwnerID: &other, GroupID: &gid}); err != nil || !changed {
		t.Fatalf("projected explicit identity: %v %v", changed, err)
	}
	if f.Owner != "richt-not-created-yet" || f.Group != "richt-group-not-created-yet" {
		t.Fatal("observation changed prepared names")
	}
	f.Path = filepath.Join(filepath.Dir(path), "missing")
	if changed, err := ObserveMissing(f, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || !f.Write {
		t.Fatal(changed, err)
	}
	for _, typ := range []string{"file", "directory"} {
		path := path
		if typ == "directory" {
			path = filepath.Dir(path)
		}
		p := &plan.PathChange{Path: path, Type: typ, State: "present", Owner: f.Owner, Group: f.Group}
		if changed, err := ObservePath(p, Ownership{OwnerID: &uid, GroupID: &gid}); err != nil || changed {
			t.Fatalf("%s unchanged: %v %v", typ, changed, err)
		}
		if changed, err := ObservePath(p, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || p.Identity == nil {
			t.Fatalf("%s pending attributes: %v %v %#v", typ, changed, err, p)
		}
		if changed, err := (Local{}).ObservePathMissing(context.Background(), p, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || p.Identity != nil {
			t.Fatal(changed, err, p)
		}
	}
}

func TestTargetPendingOwnershipIsResolvedAgainAtApply(t *testing.T) {
	target, dir := shellTarget(t)
	ctx := context.Background()
	path := filepath.Join(dir, "managed")
	if err := os.WriteFile(path, []byte("same"), 0640); err != nil {
		t.Fatal(err)
	}
	f := &plan.FileChange{Path: path, State: "present", Content: "same", Owner: "new-user", Group: "new-group"}
	projection := Ownership{OwnerPending: true, GroupPending: true}
	if changed, err := target.Observe(ctx, f, projection); err != nil || !changed || f.Write {
		t.Fatal(changed, err, f)
	}
	uid, gid := uint32(417), uint32(418)
	if changed, err := target.Observe(ctx, f, Ownership{OwnerID: &uid, GroupID: &gid}); err != nil || changed {
		t.Fatal(changed, err)
	}
	if _, err := target.Observe(ctx, f); err == nil {
		t.Fatal("missing unplanned account was accepted")
	}
	if err := target.Execute(ctx, plan.Action{Kind: "set-file-attributes", File: f}); err == nil {
		t.Fatal("apply skipped named identity lookup")
	}
	if err := os.WriteFile(filepath.Join(dir, "getent"), []byte("#!/bin/sh\ncase \"$1:$3\" in\npasswd:new-user) printf 'new-user:x:419:420::/:/bin/sh\\n';;\ngroup:new-group) printf 'new-group:x:420:\\n';;\n*) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := target.Execute(ctx, plan.Action{Kind: "set-file-attributes", File: f}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "chown-args"))
	if err != nil || !strings.Contains(string(got), "+419:+420") {
		t.Fatalf("apply did not resolve current target IDs: %s %v", got, err)
	}
	if changed, err := target.ObserveMissing(ctx, f, projection); err != nil || !changed || !f.Write {
		t.Fatal(changed, err)
	}
}

func TestTargetPathObservationUsesPrecedingAccountIdentities(t *testing.T) {
	target, dir := shellTarget(t)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("#!/bin/sh\nprintf '42\\n43\\n640\\n417\\n418\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(417), uint32(418)
	for _, typ := range []string{"file", "directory"} {
		path := filepath.Join(dir, "path-"+typ)
		if typ == "file" {
			if err := os.WriteFile(path, nil, 0640); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		p := &plan.PathChange{Path: path, Type: typ, State: "present", Owner: "new-user", Group: "new-group"}
		if changed, err := target.ObservePath(context.Background(), p, Ownership{OwnerID: &uid, GroupID: &gid}); err != nil || changed {
			t.Fatal(changed, err)
		}
		if changed, err := target.ObservePath(context.Background(), p, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || p.Identity == nil {
			t.Fatal(changed, err, p)
		}
		if changed, err := target.ObservePathMissing(context.Background(), p, Ownership{OwnerPending: true, GroupPending: true}); err != nil || !changed || p.Identity != nil {
			t.Fatal(changed, err, p)
		}
	}
}

func TestAbsentPathsDoNotRequireOwnershipAccounts(t *testing.T) {
	target, dir := shellTarget(t)
	path := filepath.Join(dir, "remove")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []bool{false, true} {
		f := &plan.FileChange{Path: path, State: "absent", Owner: "missing-user", Group: "missing-group"}
		var changed bool
		var err error
		if remote {
			changed, err = target.Observe(context.Background(), f)
		} else {
			changed, err = Observe(f)
		}
		if err != nil || !changed {
			t.Fatal(remote, changed, err)
		}
	}
	p := &plan.PathChange{Path: path, Type: "file", State: "absent", Owner: "missing-user", Group: "missing-group"}
	if changed, err := ObservePath(p); err != nil || !changed {
		t.Fatal(changed, err)
	}
}
