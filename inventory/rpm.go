package inventory

import (
	"errors"
	"regexp"
)

var rpmUpstreamVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+_.~^]*$`)
var rpmExactVersionPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9+_.~^]+-[A-Za-z0-9+_.~^]+\.[A-Za-z0-9_]+$`)
var rpmDisableExcludesPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// ValidateRPMDisableExcludes accepts omission or one DNF repository ID, including
// the reserved all and main values. Lists and glob expressions are unsupported.
func ValidateRPMDisableExcludes(value string) error {
	if value == "" || rpmDisableExcludesPattern.MatchString(value) {
		return nil
	}
	return errors.New("RPM disable_excludes must be all, main, or one repository ID using only letters, numbers, _, ., :, or -")
}

// ValidateRPMVersion accepts an omitted version, a literal upstream VERSION,
// or an exact epoch:version-release.arch request. User-supplied globs and partial
// epoch, release, or architecture selectors are not package versions.
func ValidateRPMVersion(version string) error {
	if version == "" || rpmUpstreamVersionPattern.MatchString(version) || rpmExactVersionPattern.MatchString(version) {
		return nil
	}
	return errors.New("RPM version must be an upstream version such as 1.34.3 or exact epoch:version-release.arch such as 0:1.34.3-1.noarch")
}
