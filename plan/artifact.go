package plan

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/subuk/richt/inventory"
)

// ArtifactChange freezes a target-fetched artifact and its ownership preconditions.
// Artifact bytes and archive contents are never stored in executable plans.
type ArtifactChange struct {
	Type            string        `json:"type"`
	State           string        `json:"state"`
	Path            string        `json:"path"`
	URL             string        `json:"url"`
	SHA256          string        `json:"sha256"`
	Owner           string        `json:"owner,omitempty"`
	Group           string        `json:"group,omitempty"`
	Mode            *uint32       `json:"mode,omitempty"`
	Timeout         string        `json:"timeout"`
	Format          string        `json:"format,omitempty"`
	StripComponents int           `json:"strip_components,omitempty"`
	Token           string        `json:"token"`
	ExpectedToken   string        `json:"expected_token,omitempty"`
	Identity        *PathIdentity `json:"identity,omitempty"`
}

func artifactHex(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == size && strings.ToLower(s) == s
}

func (p *ArtifactChange) Validate() error {
	if p == nil || p.Type != "download" && p.Type != "archive" || p.State != "present" && p.State != "absent" {
		return errors.New("invalid artifact type or state in plan")
	}
	b := inventory.Binary{Type: p.Type, Path: p.Path, URL: p.URL, SHA256: p.SHA256, Owner: p.Owner, Group: p.Group, Timeout: p.Timeout, Format: p.Format, StripComponents: p.StripComponents}
	if p.Mode != nil {
		b.Mode = strconv.FormatUint(uint64(*p.Mode), 8)
	}
	if err := inventory.ValidateArtifactBinary(b); err != nil {
		return errors.New("invalid artifact inputs in plan: " + err.Error())
	}
	if !artifactHex(p.SHA256, 32) || !artifactHex(p.Token, 16) || p.ExpectedToken != "" && !artifactHex(p.ExpectedToken, 16) {
		return errors.New("invalid artifact checksum or provenance in plan")
	}
	if p.Identity != nil && (p.Identity.Inode == 0 || p.ExpectedToken == "") || p.Identity == nil && p.ExpectedToken != "" {
		return errors.New("invalid artifact identity precondition in plan")
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil || timeout <= 0 || timeout > 24*time.Hour {
		return errors.New("artifact timeout must be positive and at most 24h")
	}
	if strings.ContainsAny(p.Owner+p.Group, "\x00\r\n") {
		return errors.New("invalid artifact ownership in plan")
	}
	if p.Type == "download" {
		if p.Format != "" || p.StripComponents != 0 || p.Mode != nil && *p.Mode > 0777 {
			return errors.New("invalid download options in plan")
		}
	} else if p.Format != "tar.gz" || p.Mode != nil || p.StripComponents < 0 || p.StripComponents > 32 {
		return errors.New("invalid archive options in plan")
	}
	return nil
}
