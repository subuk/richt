package plan_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/plan"
)

func prepareFileFixture(c *plan.FileChange) {
	c.AtomicWrites = true
	c.OperationID = strings.Repeat("a", 32)
	c.State = "present"
	c.Write = true
	c.Expected = &plan.FileState{Kind: "absent"}
	digest := sha256.Sum256([]byte(c.Content))
	c.Desired = &plan.FileState{Kind: "regular", SHA256: hex.EncodeToString(digest[:])}
}

func filePlan() *plan.Plan {
	c := &plan.FileChange{Path: "/etc/app.conf", Content: "new\n"}
	prepareFileFixture(c)
	return &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "local", Transport: "local", Tasks: []plan.Task{{ID: "file", Operation: "file", File: c, Progress: plan.Progress{Status: "pending"}}}}}}
}

func TestPreparedFileStatesAndBackupValidation(t *testing.T) {
	for _, mutate := range []func(*plan.FileChange){
		func(c *plan.FileChange) { c.OperationID = "bad" },
		func(c *plan.FileChange) { c.Expected = nil },
		func(c *plan.FileChange) { c.Desired = nil },
		func(c *plan.FileChange) { c.Expected.Kind = "directory" },
		func(c *plan.FileChange) { c.Expected.SHA256 = strings.Repeat("a", 64) },
		func(c *plan.FileChange) { c.Desired.SHA256 = strings.Repeat("a", 64) },
		func(c *plan.FileChange) { c.Desired.Metadata = &plan.FileMetadata{} },
		func(c *plan.FileChange) { c.BackupPath = "/tmp/foreign" },
		func(c *plan.FileChange) { c.Write = false },
		func(c *plan.FileChange) { c.State = "absent" },
		func(c *plan.FileChange) {
			c.Expected = &plan.FileState{Kind: "regular", SHA256: strings.Repeat("a", 64)}
		},
		func(c *plan.FileChange) {
			c.Expected = &plan.FileState{Kind: "symlink", Link: "target", Metadata: &plan.FileMetadata{Xattrs: map[string]string{"user.test": "not base64"}}}
		},
	} {
		p := filePlan()
		mutate(p.Hosts[0].Tasks[0].File)
		if err := p.Validate(); err == nil {
			t.Fatal("invalid prepared file accepted")
		}
	}
	p := filePlan()
	c := p.Hosts[0].Tasks[0].File
	c.Expected = &plan.FileState{Kind: "symlink", Link: "missing-target", Metadata: &plan.FileMetadata{Mode: 0777, Xattrs: map[string]string{}}}
	c.Backup = true
	if err := p.Validate(); err == nil {
		t.Fatal("missing required backup path accepted")
	}
	c.BackupPath = c.Path + ".20261106T120000.123456789Z." + c.OperationID + "~"
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	c.BackupPath = c.Path + ".not-UTC." + c.OperationID + "~"
	if err := p.Validate(); err == nil {
		t.Fatal("invalid backup timestamp accepted")
	}
	c.BackupPath = c.Path + ".20269999T999999Z." + c.OperationID + "~"
	if err := p.Validate(); err == nil {
		t.Fatal("impossible backup timestamp accepted")
	}
}

func TestFileMetadataJSONRequiresCompleteSnapshot(t *testing.T) {
	for _, data := range []string{
		`{"gid":0,"mode":420,"xattrs":{}}`,
		`{"uid":0,"mode":420,"xattrs":{}}`,
		`{"uid":0,"gid":0,"xattrs":{}}`,
		`{"uid":0,"gid":0,"mode":420}`,
		`{"uid":0,"gid":0,"mode":420,"xattrs":null}`,
		`{"uid":0,"gid":0,"mode":420,"xattrs":{"user.test":null}}`,
		`{"uid":0,"gid":0,"mode":420,"xattrs":{},"unknown":true}`,
	} {
		var metadata plan.FileMetadata
		if err := json.Unmarshal([]byte(data), &metadata); err == nil {
			t.Fatalf("incomplete metadata accepted: %s", data)
		}
	}
}

func TestFileJSONRequiresReviewedInputs(t *testing.T) {
	p := filePlan()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"atomic_writes", "backup", "operation_id", "expected", "desired"} {
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		file := wire["hosts"].([]any)[0].(map[string]any)["tasks"].([]any)[0].(map[string]any)["file"].(map[string]any)
		delete(file, field)
		invalid, _ := json.Marshal(wire)
		name := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(name, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if store, err := plan.Open(name); err == nil {
			store.Close()
			t.Fatalf("missing %s accepted", field)
		}
	}
	var restored plan.Plan
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFileDisplayShowsWriteModeAndBackup(t *testing.T) {
	for _, atomic := range []bool{true, false} {
		p := filePlan()
		c := p.Hosts[0].Tasks[0].File
		c.AtomicWrites, c.Backup = atomic, true
		c.Expected = &plan.FileState{Kind: "regular", SHA256: strings.Repeat("a", 64), Metadata: &plan.FileMetadata{Mode: 0644, Xattrs: map[string]string{}}}
		c.BackupPath = c.Path + ".20261106T120000Z." + c.OperationID + "~"
		var out bytes.Buffer
		if err := plan.Display(&out, p, config.Local{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "overwrite-file "+c.Path) || !strings.Contains(out.String(), "backup="+c.BackupPath) {
			t.Fatal(out.String())
		}
		mode := " atomic"
		if !atomic {
			mode = " non-atomic"
		}
		if !strings.Contains(out.String(), mode) {
			t.Fatal(out.String())
		}
	}
}

func TestFileDisplayExplainsSymlinkReplacement(t *testing.T) {
	p := filePlan()
	c := p.Hosts[0].Tasks[0].File
	c.Sensitive = true
	c.Expected = &plan.FileState{Kind: "symlink", Link: "PRIVATE_LINK", Metadata: &plan.FileMetadata{Mode: 0777, Xattrs: map[string]string{}}}
	var out bytes.Buffer
	if err := plan.Display(&out, p, config.Local{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace final symlink with regular file") || strings.Contains(out.String(), "PRIVATE_LINK") {
		t.Fatal(out.String())
	}
}
