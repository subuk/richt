package inventory

import "testing"

func TestValidateRPMVersion(t *testing.T) {
	for _, version := range []string{"", "1.34.3", "1", "v2.0", "1.2~rc1", "1.2^git20261101", "1.2+vendor_1", "0:1.34.3-1.noarch", "2:1.34.3-4.el9.x86_64"} {
		t.Run("valid/"+version, func(t *testing.T) {
			if err := ValidateRPMVersion(version); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, version := range []string{" ", " 1.34.3", "1.34.3\n", "1.34.*", "1.34.3?", "[1].34.3", "0:1.34.3", "1.34.3-1", "1.34.3-1.noarch", "0:1.34.3-1", "0:1.34.3-1.", "x:1.34.3-1.noarch", "--allowerasing", "1.34.3 --allowerasing", "https://example.test/demo.rpm", "$(id)", "1/2"} {
		t.Run("invalid/"+version, func(t *testing.T) {
			if ValidateRPMVersion(version) == nil {
				t.Fatal("accepted malformed version")
			}
		})
	}
}
