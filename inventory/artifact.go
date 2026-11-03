package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var artifactAccountPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*\$?|[0-9]+)$`)

// ArtifactMetadataPath returns the protected sibling directory reserved for an
// artifact's manifest, staging files and stable execution lock.
func ArtifactMetadataPath(destination string) string {
	p := path.Clean(destination)
	digest := sha256.Sum256([]byte(p))
	return path.Join(path.Dir(p), ".richt-artifact-"+hex.EncodeToString(digest[:])[:24])
}

// ValidateArtifactURL checks source syntax without contacting the source. Error
// messages deliberately omit the source, which may contain credentials.
func ValidateArtifactURL(source string) error {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(source, "?#") || strings.IndexFunc(source, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("url: expected an absolute HTTPS URL without credentials, query or fragment")
	}
	return nil
}

// ValidateArtifactBinary validates prepared download/archive inputs without
// reading files, checking accounts, or contacting the source or target.
func ValidateArtifactBinary(b Binary) error {
	if b.Type != "download" && b.Type != "archive" {
		return fmt.Errorf("type: expected download or archive")
	}
	if b.Manager != "" || b.Package != "" || b.Version != "" || b.DisableExcludes != "" {
		return fmt.Errorf("%s binary cannot set RPM fields", b.Type)
	}
	if err := ValidateArtifactURL(b.URL); err != nil {
		return err
	}
	checksum, err := hex.DecodeString(b.SHA256)
	if err != nil || len(b.SHA256) != 64 || len(checksum) != sha256.Size {
		return fmt.Errorf("sha256: expected exactly 64 hexadecimal digits")
	}
	if !path.IsAbs(b.Path) || b.Path == "/" || path.Clean(b.Path) != b.Path || strings.IndexFunc(b.Path, unicode.IsControl) >= 0 {
		return fmt.Errorf("path: expected a clean absolute non-root path without control characters")
	}
	for _, component := range strings.Split(b.Path, "/") {
		if strings.HasPrefix(component, ".richt-artifact-") {
			return fmt.Errorf("path: artifact metadata paths are reserved")
		}
	}
	for _, account := range []struct{ field, value string }{{"owner", b.Owner}, {"group", b.Group}} {
		if account.value != "" && !artifactAccountPattern.MatchString(account.value) {
			return fmt.Errorf("%s: expected an account name or numeric ID", account.field)
		}
	}
	if b.Timeout != "" {
		d, err := time.ParseDuration(b.Timeout)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("timeout: expected a positive duration of at most 24h such as 5m")
		}
	}
	if b.Type == "download" {
		if b.Format != "" || b.StripComponents != 0 {
			return fmt.Errorf("download binary cannot set format or strip_components")
		}
		if b.Mode != "" {
			m, err := strconv.ParseUint(b.Mode, 8, 32)
			if err != nil || m > 0777 || strings.IndexFunc(b.Mode, func(r rune) bool { return r < '0' || r > '7' }) >= 0 {
				return fmt.Errorf("mode: expected an octal permission mode without special bits such as \"0755\"")
			}
		}
	} else {
		if b.Mode != "" {
			return fmt.Errorf("archive binary cannot set mode")
		}
		if b.Format != "tar.gz" {
			return fmt.Errorf("format: expected tar.gz")
		}
		if b.StripComponents < 0 || b.StripComponents > 32 {
			return fmt.Errorf("strip_components: expected an integer from 0 to 32")
		}
	}
	return nil
}

func hasArtifactFields(b Binary) bool {
	return b.URL != "" || b.SHA256 != "" || b.Path != "" || b.Owner != "" || b.Group != "" || b.Mode != "" || b.Timeout != "" || b.Format != "" || b.StripComponents != 0
}
