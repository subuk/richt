package inventory

import (
	"strings"
	"testing"
)

func TestValidateDEBPackage(t *testing.T) {
	for _, name := range []string{"app", "libc6", "g++", "libstdc++6", "0x", "example-app", "pkg+", "pkg-", "pkg.name"} {
		if err := ValidateDEBPackage(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "a", "App", "app:amd64", "app:any", "app*", "app?", "app=1.0", "app/stable", "./app.deb", "-app", "app_1", "app\n", "app other", "épp"} {
		if ValidateDEBPackage(name) == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestValidateDEBVersion(t *testing.T) {
	for _, version := range []string{"", "1", "1.0", "0:1.0", "1.0-0", "2:1.0-1ubuntu1", "1.0~rc1-1", "1.0+git20261106-2+b1", "1.0-beta-1", "1:1.0:vendor-1", "1.0-1.2~b"} {
		if err := ValidateDEBVersion(version); err != nil {
			t.Errorf("%q: %v", version, err)
		}
	}
	for _, version := range []string{" ", " 1.0", "1.0\n", "1.0*", "v1.0", "x:1.0", ":1.0", "1:", "1.0-", "1.0-1-", "1.0-1:2", "1.0_1", "1/2", "$(id)", "1.0=2", "1.0 --force", "1.0\x00"} {
		if ValidateDEBVersion(version) == nil {
			t.Errorf("accepted %q", version)
		}
	}
}

func TestDEBBinaryValidation(t *testing.T) {
	for _, change := range []func(*Binary){
		func(b *Binary) { b.Manager = "dnf" },
		func(b *Binary) { b.Package = "app:amd64" },
		func(b *Binary) { b.Version = "v1.0" },
		func(b *Binary) { b.DisableExcludes = "all" },
		func(b *Binary) { b.URL = "https://example.test/app" },
		func(b *Binary) { b.Timeout = "1m" },
		func(b *Binary) { b.StripComponents = 1 },
	} {
		binary := Binary{Type: "deb", Package: "app"}
		change(&binary)
		if validateService(Service{Binaries: []Binary{binary}}) == nil {
			t.Errorf("accepted invalid DEB binary: %+v", binary)
		}
	}
}

func TestDEBOwnershipAndNormalization(t *testing.T) {
	in := fixture()
	setService(in, "app", Service{Binaries: []Binary{{Type: "deb", Package: "same"}}})
	setService(in, "repo", Service{Binaries: []Binary{{Type: "rpm", Package: "same"}}})
	out, err := in.Resolve(Selectors{Hosts: []string{"gena"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Hosts[0].Services[1].Definition.Binaries[0].Manager; got != "apt" {
		t.Fatalf("manager = %q", got)
	}
	if in.Services["app"].Binaries[0].Manager != "" {
		t.Fatal("normalization mutated original")
	}
	setService(in, "repo", Service{Binaries: []Binary{{Type: "deb", Manager: "apt", Package: "same"}}})
	// Filtering out one owner must not hide a conflict on the selected host.
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}, Services: []string{"app"}}); err == nil || !strings.Contains(err.Error(), "package dpkg:same") {
		t.Fatalf("expected dpkg ownership conflict, got %v", err)
	}
	setService(in, "repo", Service{Binaries: []Binary{{Type: "deb", Package: "other"}}})
	setService(in, "app", Service{Binaries: []Binary{{Type: "deb", Package: "same"}, {Type: "deb", Package: "same"}}})
	if _, err := in.Resolve(Selectors{Hosts: []string{"gena"}}); err == nil || !strings.Contains(err.Error(), "package dpkg:same") {
		t.Fatalf("expected within-service dpkg ownership conflict, got %v", err)
	}
}
