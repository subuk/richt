package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
)

// FileState records a final directory entry without dereferencing symlinks.
// Expected states retain metadata for backup verification and future reversal;
// desired states contain only the resulting kind and content digest.
type FileState struct {
	Kind     string        `json:"kind"`
	SHA256   string        `json:"sha256,omitempty"`
	Link     string        `json:"link,omitempty"`
	Metadata *FileMetadata `json:"metadata,omitempty"`
}

type FileMetadata struct {
	UID    uint32            `json:"uid"`
	GID    uint32            `json:"gid"`
	Mode   uint32            `json:"mode"`
	Xattrs map[string]string `json:"xattrs"`
}

func (m *FileMetadata) UnmarshalJSON(data []byte) error {
	var wire struct {
		UID    *uint32                    `json:"uid"`
		GID    *uint32                    `json:"gid"`
		Mode   *uint32                    `json:"mode"`
		Xattrs map[string]json.RawMessage `json:"xattrs"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil || wire.UID == nil || wire.GID == nil || wire.Mode == nil || wire.Xattrs == nil {
		return errors.New("file state requires complete metadata")
	}
	xattrs := make(map[string]string, len(wire.Xattrs))
	for name, raw := range wire.Xattrs {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return errors.New("file extended attributes must contain base64 strings")
		}
		xattrs[name] = *value
	}
	*m = FileMetadata{UID: *wire.UID, GID: *wire.GID, Mode: *wire.Mode, Xattrs: xattrs}
	return nil
}

var fileOperationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var fileSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var fileBackupTimestampPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}(\.[0-9]+)?Z$`)

// UnmarshalJSON requires the explicitly reviewed mode and backup selection,
// including false, instead of silently treating missing fields as defaults.
func (c *FileChange) UnmarshalJSON(data []byte) error {
	type plain FileChange
	var decoded plain
	wire := struct {
		*plain
		AtomicWrites *bool `json:"atomic_writes"`
		Backup       *bool `json:"backup"`
	}{plain: &decoded}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return errors.New("invalid file operation JSON")
	}
	if wire.AtomicWrites == nil || wire.Backup == nil {
		return errors.New("file operation requires atomic_writes and backup booleans")
	}
	decoded.AtomicWrites, decoded.Backup = *wire.AtomicWrites, *wire.Backup
	*c = FileChange(decoded)
	return nil
}

// ValidatePrepared checks frozen file inputs without observing the target.
// Resource resolvers may separately handle inputs before observation.
func (c *FileChange) ValidatePrepared() error {
	if c == nil || !path.IsAbs(c.Path) || path.Clean(c.Path) != c.Path || c.Path == "/" || strings.ContainsAny(c.Path, "\x00\r\n") {
		return errors.New("file operation requires a clean absolute non-root path")
	}
	if !fileOperationIDPattern.MatchString(c.OperationID) {
		return errors.New("file operation requires a 32-character lowercase hexadecimal operation_id")
	}
	if c.State != "present" && c.State != "absent" {
		return errors.New("invalid file state")
	}
	if err := validateFileState(c.Expected, false); err != nil {
		return err
	}
	if err := validateFileState(c.Desired, true); err != nil {
		return err
	}
	if c.State == "absent" {
		if c.Write || c.Content != "" || c.Desired.Kind != "absent" {
			return errors.New("file removal has incompatible desired inputs")
		}
	} else {
		if c.Desired.Kind != "regular" {
			return errors.New("present file requires a regular desired state")
		}
		if c.Write {
			digest := sha256.Sum256([]byte(c.Content))
			if c.Desired.SHA256 != hex.EncodeToString(digest[:]) {
				return errors.New("file desired digest does not match prepared content")
			}
		} else if c.Expected.Kind != "regular" || c.Expected.SHA256 != c.Desired.SHA256 {
			return errors.New("attribute-only file operation requires unchanged regular contents")
		}
	}
	needsBackup := c.Backup && c.Expected.Kind != "absent" && (c.Write || c.State == "absent")
	if needsBackup {
		prefix, suffix := c.Path+".", "."+c.OperationID+"~"
		if !strings.HasPrefix(c.BackupPath, prefix) || !strings.HasSuffix(c.BackupPath, suffix) {
			return errors.New("file backup_path must be adjacent and contain its operation_id")
		}
		timestamp := strings.TrimSuffix(strings.TrimPrefix(c.BackupPath, prefix), suffix)
		_, timestampErr := time.Parse("20060102T150405.999999999Z", timestamp)
		if !fileBackupTimestampPattern.MatchString(timestamp) || timestampErr != nil {
			return errors.New("file backup_path requires a UTC timestamp")
		}
	} else if c.BackupPath != "" {
		return errors.New("file operation has an unnecessary backup_path")
	}
	return nil
}

func validateFileState(s *FileState, desired bool) error {
	if s == nil {
		return errors.New("file operation requires explicit expected and desired states")
	}
	switch s.Kind {
	case "absent":
		if s.SHA256 != "" || s.Link != "" || s.Metadata != nil {
			return errors.New("absent file state has incompatible fields")
		}
		return nil
	case "regular":
		if !fileSHA256Pattern.MatchString(s.SHA256) || s.Link != "" {
			return errors.New("regular file state requires a lowercase SHA-256 digest")
		}
	case "symlink":
		if desired || s.Link == "" || strings.ContainsRune(s.Link, 0) || s.SHA256 != "" {
			return errors.New("invalid symlink file state")
		}
	default:
		return errors.New("invalid file entry kind")
	}
	if desired {
		if s.Metadata != nil {
			return errors.New("desired file state cannot contain metadata")
		}
		return nil
	}
	if s.Metadata == nil || s.Metadata.Mode > 07777 || s.Metadata.Xattrs == nil {
		return errors.New("expected file state requires complete metadata")
	}
	for name, value := range s.Metadata.Xattrs {
		if name == "" || strings.ContainsRune(name, 0) {
			return errors.New("invalid file extended attribute name")
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(value); err != nil {
			return errors.New("file extended attributes must contain base64 values")
		}
	}
	return nil
}
