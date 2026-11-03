package inventory

import (
	"strings"
	"testing"
)

func artifactBinary(typ, destination string) Binary {
	b := Binary{Type: typ, URL: "https://example.test/releases/tool", SHA256: strings.Repeat("aB", 32), Path: destination}
	if typ == "archive" {
		b.Format = "tar.gz"
	}
	return b
}

func TestValidateArtifactBinary(t *testing.T) {
	for _, typ := range []string{"download", "archive"} {
		b := artifactBinary(typ, "/opt/tool")
		b.Owner, b.Group, b.Timeout = "app-user", "123", "5m"
		if err := ValidateArtifactBinary(b); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*Binary)
	}{
		{"missing-url", func(b *Binary) { b.URL = "" }},
		{"http", func(b *Binary) { b.URL = "http://example.test/tool" }},
		{"credentials", func(b *Binary) { b.URL = "https://private-user:private-password@example.test/tool" }},
		{"query", func(b *Binary) { b.URL += "?private-token" }},
		{"empty-query", func(b *Binary) { b.URL += "?" }},
		{"fragment", func(b *Binary) { b.URL += "#private-token" }},
		{"empty-fragment", func(b *Binary) { b.URL += "#" }},
		{"relative-url", func(b *Binary) { b.URL = "/tool" }},
		{"missing-host", func(b *Binary) { b.URL = "https:///tool" }},
		{"opaque-url", func(b *Binary) { b.URL = "https:tool" }},
		{"url-control", func(b *Binary) { b.URL += "\nprivate-token" }},
		{"url-space", func(b *Binary) { b.URL += " private-token" }},
		{"url-parse-error", func(b *Binary) { b.URL = "https://example.test/%private-token" }},
		{"missing-checksum", func(b *Binary) { b.SHA256 = "" }},
		{"short-checksum", func(b *Binary) { b.SHA256 = strings.Repeat("a", 63) }},
		{"long-checksum", func(b *Binary) { b.SHA256 = strings.Repeat("a", 65) }},
		{"invalid-checksum", func(b *Binary) { b.SHA256 = strings.Repeat("g", 64) }},
		{"root-path", func(b *Binary) { b.Path = "/" }},
		{"relative-path", func(b *Binary) { b.Path = "opt/tool" }},
		{"unclean-path", func(b *Binary) { b.Path = "/opt/../tool" }},
		{"control-path", func(b *Binary) { b.Path = "/opt/tool\t" }},
		{"reserved-path", func(b *Binary) { b.Path = "/opt/.richt-artifact-user/tool" }},
		{"owner-option", func(b *Binary) { b.Owner = "--root" }},
		{"group-whitespace", func(b *Binary) { b.Group = "root wheel" }},
		{"invalid-owner", func(b *Binary) { b.Owner = "root:wheel" }},
		{"mode-special", func(b *Binary) { b.Mode = "4755" }},
		{"mode-invalid", func(b *Binary) { b.Mode = "0899" }},
		{"mode-signed", func(b *Binary) { b.Mode = "+755" }},
		{"zero-timeout", func(b *Binary) { b.Timeout = "0s" }},
		{"negative-timeout", func(b *Binary) { b.Timeout = "-5m" }},
		{"invalid-timeout", func(b *Binary) { b.Timeout = "soon" }},
		{"excessive-timeout", func(b *Binary) { b.Timeout = "24h1ns" }},
		{"rpm-manager", func(b *Binary) { b.Manager = "dnf" }},
		{"rpm-package", func(b *Binary) { b.Package = "tool" }},
		{"rpm-version", func(b *Binary) { b.Version = "1" }},
		{"rpm-excludes", func(b *Binary) { b.DisableExcludes = "all" }},
		{"download-format", func(b *Binary) { b.Format = "tar.gz" }},
		{"download-strip", func(b *Binary) { b.StripComponents = 1 }},
		{"archive-format", func(b *Binary) { b.Type, b.Format = "archive", "zip" }},
		{"archive-mode", func(b *Binary) { b.Type, b.Format, b.Mode = "archive", "tar.gz", "0755" }},
		{"archive-negative-strip", func(b *Binary) { b.Type, b.Format, b.StripComponents = "archive", "tar.gz", -1 }},
		{"archive-large-strip", func(b *Binary) { b.Type, b.Format, b.StripComponents = "archive", "tar.gz", 33 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := artifactBinary("download", "/opt/tool")
			tc.edit(&b)
			err := ValidateArtifactBinary(b)
			if err == nil {
				t.Fatal("invalid artifact accepted")
			}
			for _, secret := range []string{"private-user", "private-password", "private-token", b.URL} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic reveals source: %v", err)
				}
			}
		})
	}
}

func TestArtifactMetadataPath(t *testing.T) {
	got := ArtifactMetadataPath("/opt/tool")
	if got != ArtifactMetadataPath("/opt/./tool") || !strings.HasPrefix(got, "/opt/.richt-artifact-") || len(strings.TrimPrefix(got, "/opt/.richt-artifact-")) != 24 {
		t.Fatalf("unexpected metadata path: %s", got)
	}
	if got == ArtifactMetadataPath("/opt/other") {
		t.Fatal("distinct resource paths share metadata")
	}
}

func TestArtifactResourceOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service Service
	}{
		{"duplicate-download", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool"), artifactBinary("download", "/opt/tool")}}},
		{"archive-download-child", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool"), artifactBinary("download", "/opt/tool/bin/run")}}},
		{"archive-ancestor", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool/bin"), artifactBinary("archive", "/opt/tool")}}},
		{"download-ancestor", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool"), artifactBinary("download", "/opt/tool/other")}}},
		{"archive-config-child", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool")}, Configs: []Config{{Type: "file", Path: "/opt/tool/config"}}}},
		{"archive-directory-child", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool")}, Configs: []Config{{Type: "directory", Path: "/opt/tool/config"}}}},
		{"archive-mount-child", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool")}, Configs: []Config{{Type: "mount", Source: "/dev/data/lv", Target: "/opt/tool/data", Filesystem: "xfs"}}}},
		{"archive-mount-fstab", Service{Binaries: []Binary{artifactBinary("archive", "/etc")}, Configs: []Config{{Type: "mount", Source: "/dev/data/lv", Target: "/opt/data", Filesystem: "xfs"}}}},
		{"download-mount-fstab", Service{Binaries: []Binary{artifactBinary("download", "/etc/fstab")}, Configs: []Config{{Type: "mount", Source: "/dev/data/lv", Target: "/opt/data", Filesystem: "xfs"}}}},
		{"archive-mount-source", Service{Binaries: []Binary{artifactBinary("archive", "/dev/artifact")}, Configs: []Config{{Type: "mount", Source: "/dev/artifact/data", Target: "/opt/data", Filesystem: "xfs"}}}},
		{"download-mount-source", Service{Binaries: []Binary{artifactBinary("download", "/dev/artifact")}, Configs: []Config{{Type: "mount", Source: "/dev/artifact", Target: "/opt/data", Filesystem: "xfs"}}}},
		{"archive-mdadm-member", Service{Binaries: []Binary{artifactBinary("archive", "/dev/artifact")}, Configs: []Config{{Type: "mdadm", Array: "/dev/md/app", Level: "raid1", Metadata: "1.2", Members: []string{"/dev/artifact/data", "/dev/other"}}}}},
		{"download-mdadm-member", Service{Binaries: []Binary{artifactBinary("download", "/dev/artifact")}, Configs: []Config{{Type: "mdadm", Array: "/dev/md/app", Level: "raid1", Metadata: "1.2", Members: []string{"/dev/artifact", "/dev/other"}}}}},
		{"archive-lvm-device", Service{Binaries: []Binary{artifactBinary("archive", "/dev/artifact")}, Configs: []Config{{Type: "lvm-vg", VG: "app", Devices: []string{"/dev/artifact/data"}}}}},
		{"download-lvm-device", Service{Binaries: []Binary{artifactBinary("download", "/dev/artifact")}, Configs: []Config{{Type: "lvm-vg", VG: "app", Devices: []string{"/dev/artifact"}}}}},
		{"config-file-ancestor", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool/bin")}, Configs: []Config{{Type: "file", Path: "/opt/tool"}}}},
		{"sidecar-file", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool")}, Configs: []Config{{Type: "file", Path: ArtifactMetadataPath("/opt/tool") + "/manifest"}}}},
		{"sidecar-directory", Service{Binaries: []Binary{artifactBinary("download", "/opt/tool")}, Configs: []Config{{Type: "directory", Path: ArtifactMetadataPath("/opt/tool")}}}},
		{"archive-external-unit", Service{Binaries: []Binary{artifactBinary("archive", "/etc/systemd")}, Processes: []Process{{Type: "systemd", Unit: "app.service"}}}},
		{"archive-external-dropin", Service{Binaries: []Binary{artifactBinary("archive", "/etc/systemd/system/app.service.d")}, Processes: []Process{{Type: "systemd", Unit: "app.service"}}}},
		{"archive-external-dropin-child", Service{Binaries: []Binary{artifactBinary("archive", "/etc/systemd/system/app.service.d/child")}, Processes: []Process{{Type: "systemd", Unit: "app.service"}}}},
		{"archive-owned-unit", Service{Binaries: []Binary{artifactBinary("archive", "/etc/systemd")}, Processes: []Process{{Type: "systemd", Unit: "app.service", UnitFile: artifactUnitConfig()}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			setService(in, "app", tc.service)
			if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}}); err == nil {
				t.Fatal("overlapping artifact scope accepted")
			}
		})
	}
	for _, typ := range []string{"download", "archive"} {
		in := fixture()
		setService(in, "app", Service{Binaries: []Binary{artifactBinary(typ, "/opt/tool")}, Configs: []Config{{Type: "directory", Path: "/opt"}}})
		if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}}); err != nil {
			t.Fatalf("present ancestor directory: %v", err)
		}
		if _, err := in.Resolve(Selectors{Hosts: []string{"lanst"}}); err == nil {
			t.Fatal("absent same-service ancestor directory accepted")
		}
	}
}

func TestArtifactOwnershipBeforeServiceSelection(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Binaries: []Binary{artifactBinary("archive", "/opt/tool")}})
	setService(in, "repo", Service{Configs: []Config{{Type: "file", Path: "/opt/tool/child"}}})
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"repo"}}); err == nil {
		t.Fatal("service filtering concealed artifact ownership conflict")
	}
}

func TestRPMRejectsArtifactFields(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Binaries: []Binary{{Type: "rpm", Package: "app", Path: "/opt/tool"}}})
	if err := in.Validate(); err == nil {
		t.Fatal("RPM accepted artifact field")
	}
}

func artifactUnitConfig() *Config {
	c := inlineConfig("/etc/systemd/system/app.service")
	return &c
}
