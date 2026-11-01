//go:build !linux

package config

import (
	"errors"
	"os"
)

func validateLabel(string) error {
	return errors.New("SELinux contexts require an SELinux-enabled Linux host")
}
func readLabel(*os.File) (string, error) { return "", validateLabel("") }
func writeLabel(*os.File, string) error  { return validateLabel("") }
