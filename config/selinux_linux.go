//go:build linux

package config

import (
	"errors"
	"os"
	"strings"

	selinux "github.com/opencontainers/selinux/go-selinux"
	"golang.org/x/sys/unix"
)

func validateLabel(label string) error {
	if !selinux.GetEnabled() {
		return errors.New("SELinux context requested but SELinux is not enabled")
	}
	if _, err := selinux.NewContext(label); err != nil {
		return errors.New("invalid SELinux context")
	}
	return nil
}
func readLabel(f *os.File) (string, error) {
	n, err := unix.Fgetxattr(int(f.Fd()), "security.selinux", nil)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	n, err = unix.Fgetxattr(int(f.Fd()), "security.selinux", b)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b[:n]), "\x00"), err
}
func writeLabel(f *os.File, label string) error {
	return unix.Fsetxattr(int(f.Fd()), "security.selinux", append([]byte(label), 0), 0)
}
