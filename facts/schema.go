// Package facts defines versioned observations of Linux hosts.
package facts

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = "1"
const CollectorVersion = "1"

type Snapshot struct {
	SchemaVersion string                  `json:"schema_version"`
	CollectedAt   time.Time               `json:"collected_at"`
	Facts         Values                  `json:"facts"`
	Availability  map[string]Availability `json:"availability"`
}

type Availability struct {
	Status  string                  `json:"status"`
	Reason  string                  `json:"reason"`
	Fields  map[string]Availability `json:"fields,omitempty"`
	Entries map[string]Availability `json:"entries,omitempty"`
}

// Record identifies a schema-defined object in template values. Dynamic
// dictionaries use ordinary maps, so absent interface names remain optional.
type Record map[string]any

type Values struct {
	OS           OS      `json:"os"`
	Kernel       Kernel  `json:"kernel"`
	Architecture string  `json:"architecture"`
	Hostname     string  `json:"hostname"`
	CPU          CPU     `json:"cpu"`
	Memory       Memory  `json:"memory"`
	Network      Network `json:"network"`
}
type OS struct {
	ID        string   `json:"id"`
	VersionID string   `json:"version_id"`
	Name      string   `json:"name"`
	IDLike    []string `json:"id_like"`
}
type Kernel struct {
	Release string `json:"release"`
}
type CPU struct {
	LogicalCount int64 `json:"logical_count"`
}
type Memory struct {
	TotalBytes int64 `json:"total_bytes"`
}
type Network struct {
	Interfaces  map[string]Interface `json:"interfaces"`
	DefaultIPv4 DefaultRoute         `json:"default_ipv4"`
	DefaultIPv6 DefaultRoute         `json:"default_ipv6"`
}
type Interface struct {
	Name      string    `json:"name"`
	MAC       string    `json:"mac"`
	MTU       int64     `json:"mtu"`
	State     string    `json:"state"`
	Addresses []Address `json:"addresses"`
}
type Address struct {
	Family       string `json:"family"`
	Address      string `json:"address"`
	PrefixLength int64  `json:"prefix_length"`
	Scope        string `json:"scope"`
}
type DefaultRoute struct {
	Interface string `json:"interface"`
	Gateway   string `json:"gateway"`
	Status    string `json:"status"`
}

// Normalize materializes schema defaults; it does not turn a failed observation
// into a valid snapshot or invent collection metadata.
func (s *Snapshot) Normalize() {
	if s.Availability == nil {
		s.Availability = map[string]Availability{}
	}
	if s.Facts.OS.IDLike == nil {
		s.Facts.OS.IDLike = []string{}
	}
	if s.Facts.Network.Interfaces == nil {
		s.Facts.Network.Interfaces = map[string]Interface{}
	}
	for k, v := range s.Facts.Network.Interfaces {
		if v.Addresses == nil {
			v.Addresses = []Address{}
		}
		s.Facts.Network.Interfaces[k] = v
	}
	if s.Facts.Network.DefaultIPv4.Status == "" {
		s.Facts.Network.DefaultIPv4.Status = "absent"
	}
	if s.Facts.Network.DefaultIPv6.Status == "" {
		s.Facts.Network.DefaultIPv6.Status = "absent"
	}
}

func (s Snapshot) Validate() error {
	if s.SchemaVersion != SchemaVersion || s.CollectedAt.IsZero() {
		return errors.New("invalid fact snapshot version or collection time")
	}
	if s.Availability == nil || s.Facts.OS.IDLike == nil || s.Facts.Network.Interfaces == nil {
		return errors.New("fact collections must be materialized")
	}
	if s.Facts.CPU.LogicalCount < 0 || s.Facts.Memory.TotalBytes < 0 {
		return errors.New("invalid fact quantity")
	}
	for _, path := range []string{"os.id", "os.version_id", "os.name", "os.id_like", "kernel.release", "architecture", "hostname", "cpu.logical_count", "memory.total_bytes", "network.interfaces", "network.default_ipv4", "network.default_ipv6"} {
		a, ok := s.Availability[path]
		if !ok || (a.Status != "observed" && a.Status != "unavailable") || a.Status == "unavailable" && a.Reason == "" {
			return errors.New("missing or invalid fact availability")
		}
		if !strings.HasPrefix(path, "os.") && a.Status != "observed" {
			return errors.New("required host observation is unavailable")
		}
	}
	if s.Facts.Kernel.Release == "" || s.Facts.Architecture == "" || s.Facts.Hostname == "" || s.Facts.CPU.LogicalCount == 0 || s.Facts.Memory.TotalBytes == 0 {
		return errors.New("required host observation is empty")
	}
	for name, v := range s.Facts.Network.Interfaces {
		if name == "" || name != v.Name || v.MTU < 0 || v.Addresses == nil {
			return errors.New("invalid interface facts")
		}
		for _, a := range v.Addresses {
			ip, err := netip.ParseAddr(a.Address)
			bits := int64(128)
			if a.Family == "inet" {
				bits = 32
			}
			if err != nil || (a.Family != "inet" && a.Family != "inet6") || (a.Family == "inet") != ip.Is4() || a.PrefixLength < 0 || a.PrefixLength > bits {
				return errors.New("invalid interface address facts")
			}
		}
	}
	for i, r := range []DefaultRoute{s.Facts.Network.DefaultIPv4, s.Facts.Network.DefaultIPv6} {
		switch r.Status {
		case "selected":
			if _, ok := s.Facts.Network.Interfaces[r.Interface]; !ok {
				return errors.New("default route interface is missing")
			}
			if r.Gateway != "" {
				if ip, err := netip.ParseAddr(r.Gateway); err != nil || ip.Is4() != (i == 0) {
					return errors.New("invalid default route gateway")
				}
			}
		case "absent", "ambiguous":
			if r.Interface != "" || r.Gateway != "" {
				return errors.New("unselected default route has a path")
			}
		default:
			return errors.New("invalid default route status")
		}
	}
	for path, a := range s.Availability {
		value, err := s.Lookup(path)
		if err != nil || validateAvailability(a, value) != nil {
			return errors.New("invalid fact availability")
		}
	}
	interfacesAvailability := s.Availability["network.interfaces"]
	if len(interfacesAvailability.Entries) != len(s.Facts.Network.Interfaces) {
		return errors.New("missing interface availability")
	}
	for name, iface := range s.Facts.Network.Interfaces {
		entry, ok := interfacesAvailability.Entries[name]
		if !ok || len(entry.Fields) != 5 {
			return errors.New("incomplete interface availability")
		}
		for _, field := range []string{"name", "mac", "mtu", "state", "addresses"} {
			if _, ok := entry.Fields[field]; !ok {
				return errors.New("missing interface field availability")
			}
		}
		addresses := entry.Fields["addresses"]
		if len(addresses.Entries) != len(iface.Addresses) {
			return errors.New("missing address availability")
		}
		for i := range iface.Addresses {
			a, ok := addresses.Entries[strconv.Itoa(i)]
			if !ok || len(a.Fields) != 4 {
				return errors.New("incomplete address availability")
			}
		}
	}
	return nil
}

func validateAvailability(a Availability, value any) error {
	bad := errors.New("invalid availability record")
	if a.Status != "observed" && a.Status != "unavailable" || a.Status == "unavailable" && a.Reason == "" {
		return bad
	}
	if a.Status == "unavailable" {
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.String:
			if v.Len() != 0 {
				return bad
			}
		case reflect.Int64:
			if v.Int() != 0 {
				return bad
			}
		case reflect.Slice, reflect.Map:
			if v.Len() != 0 {
				return bad
			}
		}
	}
	for field, child := range a.Fields {
		record, ok := value.(Record)
		if !ok {
			return bad
		}
		fieldValue, ok := record[field]
		if !ok {
			return bad
		}
		if err := validateAvailability(child, fieldValue); err != nil {
			return err
		}
	}
	for key, child := range a.Entries {
		var childValue any
		switch collection := value.(type) {
		case map[string]any:
			var ok bool
			childValue, ok = collection[key]
			if !ok {
				return bad
			}
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(collection) {
				return bad
			}
			childValue = collection[index]
		case []string:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(collection) {
				return bad
			}
			childValue = collection[index]
		default:
			return bad
		}
		if err := validateAvailability(child, childValue); err != nil {
			return err
		}
	}
	return nil
}

// Lookup validates schema paths independently of observed values. Dynamic
// interface names are accessed with template index, never as dotted paths.
func (s Snapshot) Lookup(path string) (any, error) {
	if s.Facts.Network.DefaultIPv4.Status == "" {
		s.Facts.Network.DefaultIPv4.Status = "absent"
	}
	if s.Facts.Network.DefaultIPv6.Status == "" {
		s.Facts.Network.DefaultIPv6.Status = "absent"
	}
	if path == "" {
		return nil, errors.New("unknown fact path")
	}
	v := reflect.ValueOf(s.Facts)
	for _, part := range strings.Split(path, ".") {
		if v.Kind() != reflect.Struct {
			return nil, fmt.Errorf("unknown fact path %q", path)
		}
		found := false
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("json") == part {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown fact path %q", path)
		}
	}
	return templateValue(v), nil
}

// ValidatePath rejects unknown schema paths without reading host observations.
func ValidatePath(path string) error {
	_, err := (Snapshot{}).Lookup(path)
	return err
}

func templateValue(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Struct:
		out := Record{}
		for i := 0; i < v.NumField(); i++ {
			out[v.Type().Field(i).Tag.Get("json")] = templateValue(v.Field(i))
		}
		return out
	case reflect.Map:
		out := map[string]any{}
		for _, key := range v.MapKeys() {
			out[key.String()] = templateValue(v.MapIndex(key))
		}
		return out
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			out := make([]string, v.Len())
			for i := range out {
				out[i] = v.Index(i).String()
			}
			return out
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = templateValue(v.Index(i))
		}
		return out
	default:
		return v.Interface()
	}
}
