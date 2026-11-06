package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
)

const fixtureLinks = `[{"ifindex":2,"ifname":"eth0.20","mtu":1500,"address":"02:00:00:00:00:02","operstate":"UP"},{"ifindex":3,"ifname":"eth1","mtu":9000,"operstate":"DOWN"}]`
const fixtureAddresses = `[{"ifindex":3,"ifname":"eth1","addr_info":[]},{"ifindex":2,"ifname":"eth0.20","addr_info":[{"family":"inet6","local":"2001:db8::2","prefixlen":64,"scope":"global"},{"family":"inet","local":"192.0.2.2","prefixlen":24,"scope":"global"}]}]`

func fixtureRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := name + " " + strings.Join(args, " ")
	values := map[string]string{
		"uname -s": "Linux\n", "uname -r": "6.8.0-target\n", "uname -m": "aarch64\n", "hostname ": "target-name\n",
		"python3 -I -c " + osReleaseCommand:  "present\nID=debian\nVERSION_ID=\"12\"\nNAME='Debian GNU/Linux'\nID_LIKE=\"debian ubuntu\"\n",
		"cat /sys/devices/system/cpu/online": "0-3,8,10-11\n", "cat /proc/meminfo": "MemTotal:       4096 kB\nMemFree: 12 kB\n",
		"ip -j link show": fixtureLinks, "ip -j address show": fixtureAddresses,
		"ip -j -4 route show table main default": `[{"dst":"default","dev":"eth0.20","gateway":"192.0.2.1"}]`,
		"ip -j -6 route show table main default": `[{"dst":"default","dev":"eth1","gateway":"2001:db8::1"}]`,
	}
	value, ok := values[key]
	if !ok {
		return nil, errors.New("unexpected command")
	}
	return []byte(value), nil
}

func TestFactsCompleteTargetSnapshot(t *testing.T) {
	s, err := collectFacts(context.Background(), fixtureRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Facts.Kernel.Release != "6.8.0-target" || s.Facts.Architecture != "aarch64" || s.Facts.Hostname != "target-name" || s.Facts.CPU.LogicalCount != 7 || s.Facts.Memory.TotalBytes != 4194304 {
		t.Fatalf("wrong target values: %+v", s.Facts)
	}
	if !reflect.DeepEqual(s.Facts.OS.IDLike, []string{"debian", "ubuntu"}) {
		t.Fatal(s.Facts.OS)
	}
	if s.Facts.Network.DefaultIPv4.Interface != "eth0.20" || s.Facts.Network.DefaultIPv6.Interface != "eth1" {
		t.Fatal(s.Facts.Network)
	}
	if s.CollectedAt.Location() != time.UTC {
		t.Fatal("timestamp not UTC")
	}
	iface := s.Facts.Network.Interfaces["eth0.20"]
	if len(iface.Addresses) != 2 || iface.Addresses[0].Family != "inet" || iface.State != "up" {
		t.Fatal(iface)
	}
	if got := s.Facts.Network.Interfaces["eth1"]; got.MAC != "" || got.Addresses == nil {
		t.Fatal("missing interface defaults", got)
	}
	availability := s.Availability["network.interfaces"].Entries
	if availability["eth1"].Fields["mac"].Status != "unavailable" || availability["eth0.20"].Fields["mac"].Status != "observed" || availability["eth1"].Fields["addresses"].Status != "observed" {
		t.Fatal("missing attribute availability", availability)
	}
	a, _ := json.Marshal(s.Facts)
	b, _ := json.Marshal(s.Facts)
	if string(a) != string(b) {
		t.Fatal("nondeterministic output")
	}
}

func TestOSReleaseDataParser(t *testing.T) {
	attrs, err := parseOSRelease("# comment\nID=alma\nNAME=\"Alma \\\"Linux\\\" \\\\ \\$HOME \\`cmd\\`\"\nVERSION_ID='10.2'\nID_LIKE=\"rhel centos fedora\"\nLITERAL='$(touch /tmp/never-execute)'\nEMPTY=\nESCAPED=two\\ words\n")
	if err != nil {
		t.Fatal(err)
	}
	if attrs["NAME"] != "Alma \"Linux\" \\ $HOME `cmd`" || attrs["LITERAL"] != "$(touch /tmp/never-execute)" || attrs["ESCAPED"] != "two words" {
		t.Fatal(attrs)
	}
	for _, bad := range []string{"broken", "NAME=\"unterminated", "NAME='a'\"b\"", "NAME=two words", "1BAD=x", "NAME=x;echo bad", "NAME=\\", "NAME=\x00", "NAME=\xff"} {
		if _, err := parseOSRelease(bad); err == nil {
			t.Fatalf("accepted malformed metadata %q", bad)
		}
	}
}

func TestOSReleaseDefaultsAndFailures(t *testing.T) {
	for _, tt := range []struct {
		value  string
		fail   bool
		status string
	}{{"absent\n", false, "unavailable"}, {"present\nID=ubuntu\n", false, "unavailable"}, {"present\nID=alma\nVERSION_ID=10\nNAME=Alma\nID_LIKE='rhel fedora'\n", false, "observed"}, {"present\nNAME=\"broken\n", true, ""}, {"unexpected", true, ""}} {
		s, err := collectFacts(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "python3" {
				return []byte(tt.value), nil
			}
			return fixtureRunner(ctx, name, args...)
		})
		if (err != nil) != tt.fail {
			t.Fatalf("%q: %v", tt.value, err)
		}
		if err == nil && s.Availability["os.name"].Status != tt.status {
			t.Fatal(s.Availability)
		}
	}
	_, err := collectFacts(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "python3" {
			return nil, errors.New("read failed")
		}
		return fixtureRunner(ctx, name, args...)
	})
	if err == nil {
		t.Fatal("read failure became defaults")
	}
}

func TestOSReleasePathPrecedence(t *testing.T) {
	// Execute the real fixed Python read helper against controlled replacement paths.
	// This tests only file selection; parsing always happens in Go.
	for _, mode := range []string{"etc", "fallback", "absent", "read-error", "broken-symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			etc := filepath.Join(dir, "etc-release")
			usr := filepath.Join(dir, "usr-release")
			if mode == "broken-symlink" {
				if err := os.Symlink(filepath.Join(dir, "missing"), etc); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "etc" || mode == "read-error" {
				if mode == "read-error" {
					if err := os.Mkdir(etc, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(etc, []byte("ID=etc"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "absent" {
				if err := os.WriteFile(usr, []byte("ID=usr\nNAME=fallback-only"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := strings.ReplaceAll(strings.ReplaceAll(osReleaseCommand, "/etc/os-release", etc), "/usr/lib/os-release", usr)
			c, _ := New(inventory.Transport{Type: "local"})
			b, err := c.factRun(context.Background(), "python3", "-I", "-c", command)
			if mode == "read-error" || mode == "broken-symlink" {
				if err == nil {
					t.Fatal("read error used fallback")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"etc": "present\nID=etc", "fallback": "present\nID=usr\nNAME=fallback-only", "absent": "absent\n"}[mode]
			if string(b) != want {
				t.Fatalf("%q want %q", b, want)
			}
		})
	}
}

func TestFactCollectionFailures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
	}{{"uname", "Darwin\n"}, {"cat", "malformed"}, {"ip", "null"}} {
		_, err := collectFacts(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == tt.name {
				return []byte(tt.value), nil
			}
			return fixtureRunner(ctx, name, args...)
		})
		if err == nil {
			t.Fatal("accepted invalid", tt.name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectFacts(ctx, fixtureRunner); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := collectFacts(ctx, func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "0-2,2-4", "4-2", "-1", "0-", "0-2,x", "0-999999999999999"} {
		if _, err := parseOnlineCPUs(bad); err == nil {
			t.Fatal("accepted online CPU", bad)
		}
	}
	for _, bad := range []string{"", "MemTotal: 1 MB", "MemTotal: -1 kB", "MemTotal: 9223372036854775807 kB", "MemTotal: 1 kB\nMemTotal: 2 kB"} {
		if _, err := parseMemory(bad); err == nil {
			t.Fatal("accepted memory", bad)
		}
	}
}

func TestFactOutputBoundAndSafeErrors(t *testing.T) {
	c, _ := New(inventory.Transport{Type: "local"})
	for _, script := range []string{`head -c 5000000 /dev/zero`, `head -c 100000 /dev/zero >&2`, `printf PRIVATE_ERROR >&2; exit 1`} {
		if _, err := c.factRun(context.Background(), "sh", "-c", script); err == nil || strings.Contains(err.Error(), "PRIVATE_ERROR") {
			t.Fatal(err)
		}
	}
	b := &boundedFactOutput{limit: 10, cancel: func() {}}
	if _, err := b.Write(make([]byte, 11)); err == nil || b.Len() > 10 || !b.overflow {
		t.Fatal("buffer exceeded bound")
	}
}

func TestFactCollectorSameLocalSSHAndExplicitSudo(t *testing.T) {
	dir := fakeCommand(t, "uname", `case "$1" in -s) printf 'Linux\n';; -r) printf 'target-release\n';; -m) printf 'target-arch\n';; *) exit 1;; esac`)
	fakeCommand(t, "hostname", `printf target-name`)
	fakeCommand(t, "python3", `printf 'present\nID=debian\n'`)
	fakeCommand(t, "cat", `case "$1" in /etc/os-release|/usr/lib/os-release) printf 'ID=debian\n';; /sys/devices/system/cpu/online) printf '0-1\n';; /proc/meminfo) printf 'MemTotal: 1024 kB\n';; *) exit 1;; esac`)
	fakeCommand(t, "ip", `printf '[]'`)
	fakeCommand(t, "sudo", `[ "$1" = -n ] && [ "$2" = -- ] || exit 90
printf called >> "$FACT_SUDO_LOG"
shift 2
exec "$@"`)
	fakeCommand(t, "ssh", `for last do :; done
exec sh -c "$last"`)
	log := filepath.Join(dir, "sudo-log")
	t.Setenv("FACT_SUDO_LOG", log)
	var baseline facts.Values
	for _, connection := range []inventory.Transport{{Type: "local"}, {Type: "local", Sudo: true}, {Type: "ssh", Host: "target"}, {Type: "ssh", Host: "target", Sudo: true}} {
		c, _ := New(connection)
		s, err := c.Facts(context.Background())
		if err != nil {
			t.Fatal(connection, err)
		}
		if baseline.Hostname == "" {
			baseline = s.Facts
		} else if !reflect.DeepEqual(baseline, s.Facts) {
			t.Fatal("transport altered facts")
		}
	}
	if b, err := os.ReadFile(log); err != nil || len(b) == 0 {
		t.Fatal("sudo not explicit", err)
	}
}

func TestInterfaceJoinAndMalformedLists(t *testing.T) {
	got, err := parseInterfaces([]byte(fixtureLinks), []byte(fixtureAddresses))
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	for _, pair := range [][2]string{{"null", "[]"}, {"[]", "null"}, {`[{"ifindex":1,"ifname":"eth0"}]`, `[{"ifindex":2,"ifname":"eth0"}]`}, {`[{"ifindex":1,"ifname":"eth0"},{"ifindex":1,"ifname":"eth1"}]`, `[]`}, {`[] trailing`, `[]`}, {`[{"ifindex":1,"ifname":"eth0"}]`, `[{"ifindex":1,"ifname":"other"}]`}} {
		if _, err := parseInterfaces([]byte(pair[0]), []byte(pair[1])); err == nil {
			t.Fatal("accepted malformed interfaces", pair)
		}
	}
}
