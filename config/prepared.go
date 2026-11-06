package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/aymanbagabas/go-udiff"
	"github.com/subuk/richt/plan"
)

//go:embed file_helper.py
var fileHelper string

type fileRequest struct {
	Action string           `json:"action"`
	File   *plan.FileChange `json:"file"`
	Resume bool             `json:"resume"`
	UID    int              `json:"uid"`
	GID    int              `json:"gid"`
}

type fileResponse struct {
	State   *plan.FileState `json:"state"`
	Content string          `json:"content"`
	Error   string          `json:"error"`
}

func localFileInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = bytes.NewReader(input)
	return command.Output()
}

func fileCommand(ctx context.Context, run func(context.Context, []byte, string, ...string) ([]byte, error), request fileRequest) (fileResponse, error) {
	var response fileResponse
	if run == nil {
		return response, errors.New("missing target input runner")
	}
	input, err := json.Marshal(request)
	if err != nil {
		return response, errors.New("cannot encode prepared file inputs")
	}
	out, err := run(ctx, input, "python3", "-I", "-c", fileHelper)
	if err != nil {
		return response, errors.New("prepared file helper failed; target requires Python 3 with filesystem metadata support")
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return response, errors.New("invalid prepared file helper response")
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func freezeFile(f *plan.FileChange, state *plan.FileState, old []byte) error {
	if f.OperationID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return errors.New("file operation identifier generation failed")
		}
		f.OperationID = hex.EncodeToString(id[:])
	}
	f.Expected = state
	f.Desired = &plan.FileState{Kind: "absent"}
	if f.State != "absent" {
		f.Desired = &plan.FileState{Kind: "regular", SHA256: hashContent([]byte(f.Content))}
	}
	f.BackupPath = ""
	if f.Backup && state.Kind != "absent" && (f.Write || f.State == "absent") {
		f.BackupPath = f.Path + "." + time.Now().UTC().Format("20060102T150405.000000000Z") + "." + f.OperationID + "~"
	}
	f.Patch = ""
	if f.State == "absent" {
		f.Patch = udiff.Unified(f.Path, "/dev/null", string(old), "")
	} else if f.Write {
		from := f.Path
		if state.Kind == "absent" {
			from = "/dev/null"
		}
		f.Patch = udiff.Unified(from, f.Path, string(old), f.Content)
	}
	return nil
}

func observeEntry(ctx context.Context, run func(context.Context, []byte, string, ...string) ([]byte, error), f *plan.FileChange) (*plan.FileState, []byte, error) {
	response, err := fileCommand(ctx, run, fileRequest{Action: "observe", File: f})
	if err != nil {
		return nil, nil, err
	}
	if response.State == nil {
		return nil, nil, errors.New("target returned incomplete file metadata")
	}
	old, err := base64.StdEncoding.DecodeString(response.Content)
	if err != nil {
		return nil, nil, errors.New("target returned invalid file content")
	}
	return response.State, old, nil
}

func entryChanged(f *plan.FileChange, state *plan.FileState, old []byte, uid, gid int, pending bool) (bool, error) {
	f.Write = f.State != "absent" && (state.Kind != "regular" || !bytes.Equal(old, []byte(f.Content)))
	if err := freezeFile(f, state, old); err != nil {
		return false, err
	}
	if f.State == "absent" {
		return state.Kind != "absent", nil
	}
	if state.Kind != "regular" {
		return true, nil
	}
	m := state.Metadata
	changed := pending || f.Write || f.Mode != nil && m.Mode != *f.Mode || uid != -1 && int(m.UID) != uid || gid != -1 && int(m.GID) != gid
	if f.SELinux != "" {
		label, err := base64.StdEncoding.DecodeString(m.Xattrs["security.selinux"])
		if err != nil {
			return false, errors.New("target returned invalid SELinux metadata")
		}
		changed = changed || string(bytes.TrimRight(label, "\x00")) != f.SELinux
	}
	return changed, nil
}
