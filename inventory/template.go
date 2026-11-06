package inventory

import (
	"encoding/hex"
	"fmt"
)

// ValidateTemplate checks a template declaration without opening its source.
func ValidateTemplate(t Template) error {
	count := 0
	if t.File != "" {
		count++
	}
	if t.Inline != nil {
		count++
	}
	if t.URL != "" {
		count++
	}
	if count != 1 {
		return fmt.Errorf("specify exactly one of file, inline or url")
	}
	if t.URL == "" {
		if t.SHA256 != "" {
			return fmt.Errorf("sha256 requires url")
		}
		return nil
	}
	if err := ValidateArtifactURL(t.URL); err != nil {
		return err
	}
	return ValidateSourceSHA256(t.SHA256)
}

// ValidateSourceSHA256 checks a checksum-pinned source without fetching it.
func ValidateSourceSHA256(value string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(value) != 64 || len(decoded) != 32 {
		return fmt.Errorf("sha256: expected exactly 64 hexadecimal digits")
	}
	return nil
}

func hasTemplate(t Template) bool {
	return t.File != "" || t.Inline != nil || t.URL != "" || t.SHA256 != ""
}
