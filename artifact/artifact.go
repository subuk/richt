// Package artifact manages checksum-pinned HTTPS downloads and complete archive trees.
package artifact

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

//go:embed target.py
var targetHelper string

// Backend uses the same stdin transport locally and over SSH, including explicit sudo.
type Backend struct {
	RunInput func(context.Context, []byte, string, ...string) ([]byte, error)
}

func Prepare(b inventory.Binary, state string) (*plan.ArtifactChange, error) {
	if state == "" {
		state = "present"
	}
	if b.Timeout == "" {
		b.Timeout = "5m"
	}
	if err := inventory.ValidateArtifactBinary(b); err != nil {
		return nil, err
	}
	p := &plan.ArtifactChange{Type: b.Type, State: state, Path: filepath.Clean(b.Path), URL: b.URL, SHA256: strings.ToLower(b.SHA256), Owner: b.Owner, Group: b.Group, Timeout: b.Timeout, Format: b.Format, StripComponents: b.StripComponents}
	if b.Mode != "" {
		n, err := strconv.ParseUint(b.Mode, 8, 12)
		if err != nil {
			return nil, errors.New("invalid artifact mode")
		}
		mode := uint32(n)
		p.Mode = &mode
	} else if b.Type == "download" {
		mode := uint32(0755)
		p.Mode = &mode
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, errors.New("cannot create artifact provenance")
	}
	p.Token = hex.EncodeToString(token[:])
	return p, p.Validate()
}

type response struct {
	Error    string             `json:"error"`
	Changed  bool               `json:"changed"`
	Identity *plan.PathIdentity `json:"identity"`
	Token    string             `json:"token"`
}

func (b Backend) call(ctx context.Context, op string, p *plan.ArtifactChange) (response, error) {
	var r response
	seconds := float64(300)
	if p != nil {
		d, _ := time.ParseDuration(p.Timeout)
		seconds = d.Seconds()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	input, _ := json.Marshal(struct {
		Op      string               `json:"op"`
		Change  *plan.ArtifactChange `json:"change,omitempty"`
		Seconds float64              `json:"seconds"`
	}{op, p, seconds})
	run := b.RunInput
	if run == nil {
		client := &transport.Client{Connection: inventory.Transport{Type: "local"}}
		run = client.RunInput
	}
	out, err := run(ctx, input, "python3", "-I", "-c", targetHelper)
	if err != nil {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		return r, errors.New("artifact target helper failed; Linux and Python 3.8 or newer are required")
	}
	if len(out) > 8192 || json.Unmarshal(out, &r) != nil {
		return r, errors.New("artifact target helper returned invalid metadata")
	}
	if r.Error != "" {
		return r, fmt.Errorf("artifact: %s", r.Error)
	}
	return r, nil
}

// Check checks only the capability used by artifacts, without creating target data.
func (b Backend) Check(ctx context.Context) error { _, err := b.call(ctx, "check", nil); return err }

// Observe never fetches the URL or creates metadata, staging files, or locks.
func (b Backend) Observe(ctx context.Context, p *plan.ArtifactChange) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	r, err := b.call(ctx, "observe", p)
	if err != nil {
		return false, err
	}
	p.Identity = r.Identity
	p.ExpectedToken = r.Token
	return r.Changed, nil
}

func (Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "artifact" || t.Artifact == nil || t.Account != nil || t.File != nil || t.RPM != nil || t.Path != nil || t.Storage != nil || t.Mount != nil || t.Process != nil || t.Oneshot != nil {
		return nil, errors.New("invalid artifact task")
	}
	p := t.Artifact
	if err := p.Validate(); err != nil {
		return nil, err
	}
	kind := "ensure-artifact"
	d := []string{fmt.Sprintf("fetch HTTPS artifact %s on target; SHA256 %s; timeout %s", p.URL, p.SHA256, p.Timeout), "verify checksum before publishing; refuse unowned or replaced targets; serialize this resource with a protected target lock"}
	if p.Type == "archive" {
		d = append(d, fmt.Sprintf("extract tar.gz to %s; strip %d components; own the complete tree; preserve safe executable bits", p.Path, p.StripComponents), "publish the complete staged directory atomically; refuse unsafe members, mount crossings and unexpected extra entries")
	} else {
		d = append(d, "atomically publish file "+p.Path)
		if p.Mode != nil {
			d = append(d, fmt.Sprintf("set mode %04o", *p.Mode))
		}
	}
	if p.Owner != "" {
		d = append(d, "set owner "+p.Owner+" (resolve name on target)")
	}
	if p.Group != "" {
		d = append(d, "set group "+p.Group+" (resolve name on target)")
	}
	if p.State == "absent" {
		kind = "remove-artifact"
		d = []string{"remove owned " + p.Type + " artifact " + p.Path + " and its tracked contents; refuse replacements, mounts and unexpected entries; no download"}
	}
	d = append(d, "limits: download 1 GiB; extracted data 4 GiB; 100000 members; 4096-byte paths; 5 HTTPS redirects; Python 3.8+ on Linux")
	return []plan.Action{{Kind: kind, Artifact: p, Description: d}}, nil
}

func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	p := a.Artifact
	if err := p.Validate(); err != nil {
		return err
	}
	if p.State == "present" && a.Kind != "ensure-artifact" || p.State == "absent" && a.Kind != "remove-artifact" {
		return errors.New("invalid artifact action")
	}
	d, _ := time.ParseDuration(p.Timeout)
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	_, err := b.call(ctx, "execute", p)
	return err
}
