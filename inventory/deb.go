package inventory

import (
	"errors"
	"regexp"
	"strings"
)

var debPackagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
var debEpochPattern = regexp.MustCompile(`^[0-9]+$`)
var debUpstreamVersionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9.+:~\-]*$`)
var debRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9+.~]+$`)

// ValidateDEBPackage accepts concrete native Debian package names. Architecture
// qualifiers, globs, paths and package-manager selectors are unsupported.
func ValidateDEBPackage(name string) error {
	if !debPackagePattern.MatchString(name) {
		return errors.New("DEB package must be a native Debian package name of at least two lowercase letters, digits, +, . or -, starting with a letter or digit")
	}
	return nil
}

// ValidateDEBVersion accepts omission or a complete Debian version. It validates
// syntax only; Debian version equality is determined by dpkg on the target.
func ValidateDEBVersion(version string) error {
	if version == "" {
		return nil
	}
	invalid := func() error {
		return errors.New("DEB version must be an exact Debian [epoch:]upstream[-revision] version starting with a digit")
	}
	rest := version
	if colon := strings.IndexByte(rest, ':'); colon >= 0 {
		if !debEpochPattern.MatchString(rest[:colon]) {
			return invalid()
		}
		rest = rest[colon+1:]
	}
	if dash := strings.LastIndexByte(rest, '-'); dash >= 0 {
		if !debRevisionPattern.MatchString(rest[dash+1:]) {
			return invalid()
		}
		rest = rest[:dash]
	}
	if !debUpstreamVersionPattern.MatchString(rest) {
		return invalid()
	}
	return nil
}
