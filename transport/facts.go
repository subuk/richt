package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/subuk/richt/facts"
)

const factTimeout = 30 * time.Second
const factOutputLimit = 4 << 20

// Facts observes the target namespace without installing software or probing
// external connectivity. Every command uses the same explicit SSH/sudo inputs.
func (c *Client) Facts(ctx context.Context) (facts.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, factTimeout)
	defer cancel()
	return collectFacts(ctx, c.factRun)
}

type boundedFactOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedFactOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *boundedFactOutput) Len() int      { return b.buffer.Len() }

func (b *boundedFactOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.overflow = true
		b.cancel()
		return 0, errors.New("fact command output limit exceeded")
	}
	return b.buffer.Write(p)
}

func (c *Client) factRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := c.command(commandCtx, nil, name, args...)
	if err != nil {
		return nil, err
	}
	out := &boundedFactOutput{limit: factOutputLimit, cancel: cancel}
	diagnostic := &boundedFactOutput{limit: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err = cmd.Run()
	if out.overflow || diagnostic.overflow {
		return nil, errors.New("fact command output limit exceeded")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if message := c.failureDiagnostic(exit.ExitCode(), diagnostic.Bytes()); message != "" {
				return nil, errors.New(message)
			}
		}
		return nil, errors.New("fact command could not execute successfully")
	}
	return out.Bytes(), nil
}

type factRunner func(context.Context, string, ...string) ([]byte, error)

// The files are read as bounded bytes, never sourced. Only ENOENT permits
// fallback; permission, directory and other I/O errors remain failures.
const osReleaseCommand = `import os, sys
for path in ('/etc/os-release', '/usr/lib/os-release'):
    try:
        with open(path, 'rb') as source:
            content = source.read(1048577)
        if len(content) > 1048576:
            sys.exit(1)
        sys.stdout.buffer.write(b'present\n' + content)
        break
    except FileNotFoundError:
        if os.path.lexists(path):
            sys.exit(1)
    except OSError:
        sys.exit(1)
else:
    sys.stdout.buffer.write(b'absent\n')
`

func collectFacts(ctx context.Context, run factRunner) (facts.Snapshot, error) {
	s := facts.Snapshot{SchemaVersion: facts.SchemaVersion, Availability: map[string]facts.Availability{}}
	s.Normalize()
	read := func(label, name string, args ...string) ([]byte, error) {
		b, err := run(ctx, name, args...)
		if err != nil {
			return nil, fmt.Errorf("collect %s: %w", label, err)
		}
		return b, nil
	}
	scalar := func(label, name string, args ...string) (string, error) {
		b, err := read(label, name, args...)
		if err != nil {
			return "", err
		}
		v := strings.TrimSuffix(string(b), "\n")
		if v == "" || strings.ContainsAny(v, "\x00\r\n") {
			return "", fmt.Errorf("malformed %s output", label)
		}
		return v, nil
	}
	platform, err := scalar("platform", "uname", "-s")
	if err != nil {
		return s, err
	}
	if platform != "Linux" {
		return s, errors.New("host facts require a Linux target")
	}
	b, err := read("OS release", "python3", "-I", "-c", osReleaseCommand)
	if err != nil {
		return s, err
	}
	if bytes.HasPrefix(b, []byte("present\n")) {
		attrs, err := parseOSRelease(string(b[len("present\n"):]))
		if err != nil {
			return s, err
		}
		s.Facts.OS = facts.OS{ID: attrs["ID"], VersionID: attrs["VERSION_ID"], Name: attrs["NAME"], IDLike: strings.Fields(attrs["ID_LIKE"])}
		for key, path := range map[string]string{"ID": "os.id", "VERSION_ID": "os.version_id", "NAME": "os.name", "ID_LIKE": "os.id_like"} {
			_, ok := attrs[key]
			if ok {
				s.Availability[path] = facts.Availability{Status: "observed"}
			} else {
				s.Availability[path] = facts.Availability{Status: "unavailable", Reason: "attribute_absent"}
			}
		}
	} else if string(b) == "absent\n" {
		for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like"} {
			s.Availability[path] = facts.Availability{Status: "unavailable", Reason: "os_release_absent"}
		}
	} else {
		return s, errors.New("malformed OS release response")
	}
	if s.Facts.Kernel.Release, err = scalar("kernel release", "uname", "-r"); err != nil {
		return s, err
	}
	if s.Facts.Architecture, err = scalar("architecture", "uname", "-m"); err != nil {
		return s, err
	}
	if s.Facts.Hostname, err = scalar("hostname", "hostname"); err != nil {
		return s, err
	}
	b, err = read("online CPUs", "cat", "/sys/devices/system/cpu/online")
	if err != nil {
		return s, err
	}
	if s.Facts.CPU.LogicalCount, err = parseOnlineCPUs(string(b)); err != nil {
		return s, err
	}
	b, err = read("memory", "cat", "/proc/meminfo")
	if err != nil {
		return s, err
	}
	if s.Facts.Memory.TotalBytes, err = parseMemory(string(b)); err != nil {
		return s, err
	}
	links, err := read("network links", "ip", "-j", "link", "show")
	if err != nil {
		return s, err
	}
	addresses, err := read("network addresses", "ip", "-j", "address", "show")
	if err != nil {
		return s, err
	}
	if s.Facts.Network.Interfaces, err = parseInterfaces(links, addresses); err != nil {
		return s, err
	}
	interfaceAvailability, err := interfaceAvailability(links, addresses, s.Facts.Network.Interfaces)
	if err != nil {
		return s, err
	}
	s.Availability["network.interfaces"] = interfaceAvailability
	for _, v := range []struct {
		family, path string
		route        *facts.DefaultRoute
	}{{"-4", "network.default_ipv4", &s.Facts.Network.DefaultIPv4}, {"-6", "network.default_ipv6", &s.Facts.Network.DefaultIPv6}} {
		b, err = read("default routes", "ip", "-j", v.family, "route", "show", "table", "main", "default")
		if err != nil {
			return s, err
		}
		*v.route, err = parseDefaultRoutes(b, v.family == "-6", s.Facts.Network.Interfaces)
		if err != nil {
			return s, err
		}
		reason := ""
		if v.route.Status != "selected" {
			reason = "default_route_" + v.route.Status
		}
		s.Availability[v.path] = facts.Availability{Status: "observed", Reason: reason}
	}
	for _, path := range []string{"kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes"} {
		s.Availability[path] = facts.Availability{Status: "observed"}
	}
	s.CollectedAt = time.Now().UTC()
	s.Normalize()
	if err = s.Validate(); err != nil {
		return s, errors.New("invalid collected fact snapshot")
	}
	return s, nil
}

func parseOnlineCPUs(raw string) (int64, error) {
	var count, last int64
	last = -1
	for _, part := range strings.Split(strings.TrimSpace(raw), ",") {
		rangeParts := strings.Split(part, "-")
		first, err := strconv.ParseInt(rangeParts[0], 10, 64)
		if err != nil || first < 0 || first <= last || len(rangeParts) > 2 {
			return 0, errors.New("malformed online CPU output")
		}
		end := first
		if len(rangeParts) == 2 {
			end, err = strconv.ParseInt(rangeParts[1], 10, 64)
			if err != nil || end < first {
				return 0, errors.New("malformed online CPU output")
			}
		}
		if end > 1<<30 {
			return 0, errors.New("malformed online CPU output")
		}
		count += end - first + 1
		last = end
	}
	return count, nil
}
func parseMemory(raw string) (int64, error) {
	var result int64
	found := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "MemTotal:" {
			continue
		}
		if found || len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("malformed memory output")
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb <= 0 || kb > (1<<63-1)/1024 {
			return 0, errors.New("malformed memory output")
		}
		result = kb * 1024
		found = true
	}
	if !found {
		return 0, errors.New("malformed memory output")
	}
	return result, nil
}

func decodeFactJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(value); err != nil {
		return errors.New("malformed network JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("malformed network JSON")
	}
	return nil
}

type ipInterface struct {
	Index     int64  `json:"ifindex"`
	Name      string `json:"ifname"`
	MAC       string `json:"address"`
	MTU       int64  `json:"mtu"`
	State     string `json:"operstate"`
	Addresses []struct {
		Family       string `json:"family"`
		Local        string `json:"local"`
		PrefixLength int64  `json:"prefixlen"`
		Scope        string `json:"scope"`
	} `json:"addr_info"`
}

func parseInterfaces(links, addresses []byte) (map[string]facts.Interface, error) {
	var ls, as []ipInterface
	if err := decodeFactJSON(links, &ls); err != nil {
		return nil, err
	}
	if err := decodeFactJSON(addresses, &as); err != nil {
		return nil, err
	}
	if ls == nil || as == nil {
		return nil, errors.New("malformed interface lists")
	}
	out := map[string]facts.Interface{}
	indices := map[int64]string{}
	for _, l := range ls {
		if l.Index <= 0 || l.Name == "" || l.MTU < 0 {
			return nil, errors.New("malformed link record")
		}
		if _, ok := indices[l.Index]; ok {
			return nil, errors.New("duplicate interface index")
		}
		if _, ok := out[l.Name]; ok {
			return nil, errors.New("duplicate interface name")
		}
		indices[l.Index] = l.Name
		out[l.Name] = facts.Interface{Name: l.Name, MAC: l.MAC, MTU: l.MTU, State: strings.ToLower(l.State), Addresses: []facts.Address{}}
	}
	seen := map[int64]bool{}
	for _, a := range as {
		name, ok := indices[a.Index]
		if !ok || seen[a.Index] || a.Name != "" && a.Name != name {
			return nil, errors.New("unmatched interface address record")
		}
		seen[a.Index] = true
		v := out[name]
		for _, addr := range a.Addresses {
			if addr.Family != "inet" && addr.Family != "inet6" {
				return nil, errors.New("invalid interface address family")
			}
			v.Addresses = append(v.Addresses, facts.Address{Family: addr.Family, Address: addr.Local, PrefixLength: addr.PrefixLength, Scope: addr.Scope})
		}
		sort.Slice(v.Addresses, func(i, j int) bool {
			a, b := v.Addresses[i], v.Addresses[j]
			if a.Family != b.Family {
				return a.Family < b.Family
			}
			if a.Address != b.Address {
				return a.Address < b.Address
			}
			if a.PrefixLength != b.PrefixLength {
				return a.PrefixLength < b.PrefixLength
			}
			return a.Scope < b.Scope
		})
		out[name] = v
	}
	return out, nil
}
