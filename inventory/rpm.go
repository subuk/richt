package inventory

import (
	"errors"
	"regexp"
)

var rpmUpstreamVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+_.~^]*$`)
var rpmExactVersionPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9+_.~^]+-[A-Za-z0-9+_.~^]+\.[A-Za-z0-9_]+$`)

// ValidateRPMVersion accepts an omitted version, a literal upstream VERSION,
// or an exact epoch:version-release.arch request. User-supplied globs and partial
// epoch, release, or architecture selectors are not package versions.
func ValidateRPMVersion(version string) error {
	if version == "" || rpmUpstreamVersionPattern.MatchString(version) || rpmExactVersionPattern.MatchString(version) {
		return nil
	}
	return errors.New("RPM version must be an upstream version such as 1.34.3 or exact epoch:version-release.arch such as 0:1.34.3-1.noarch")
}
