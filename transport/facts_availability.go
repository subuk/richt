package transport

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/subuk/richt/facts"
)

func attributeAvailability(raw map[string]json.RawMessage, key string) (facts.Availability, error) {
	value, ok := raw[key]
	if !ok {
		return facts.Availability{Status: "unavailable", Reason: "attribute_absent"}, nil
	}
	if string(value) == "null" {
		return facts.Availability{}, errors.New("null network attribute")
	}
	return facts.Availability{Status: "observed"}, nil
}

func interfaceAvailability(links, addresses []byte, interfaces map[string]facts.Interface) (facts.Availability, error) {
	result := facts.Availability{Status: "observed", Entries: map[string]facts.Availability{}}
	var ls, as []map[string]json.RawMessage
	if err := decodeFactJSON(links, &ls); err != nil {
		return result, err
	}
	if err := decodeFactJSON(addresses, &as); err != nil {
		return result, err
	}
	addressRecords := map[int64]map[string]json.RawMessage{}
	for _, raw := range as {
		var index int64
		if err := json.Unmarshal(raw["ifindex"], &index); err != nil {
			return result, errors.New("invalid address index")
		}
		addressRecords[index] = raw
	}
	for _, raw := range ls {
		var index int64
		var name string
		if json.Unmarshal(raw["ifindex"], &index) != nil || json.Unmarshal(raw["ifname"], &name) != nil {
			return result, errors.New("invalid link identity")
		}
		entry := facts.Availability{Status: "observed", Fields: map[string]facts.Availability{}}
		for attr, field := range map[string]string{"ifname": "name", "address": "mac", "mtu": "mtu", "operstate": "state"} {
			availability, err := attributeAvailability(raw, attr)
			if err != nil {
				return result, err
			}
			entry.Fields[field] = availability
		}
		addressRaw, ok := addressRecords[index]
		if !ok {
			entry.Fields["addresses"] = facts.Availability{Status: "unavailable", Reason: "address_record_absent"}
		} else {
			availability, err := attributeAvailability(addressRaw, "addr_info")
			if err != nil {
				return result, err
			}
			var raws []map[string]json.RawMessage
			if value, ok := addressRaw["addr_info"]; ok {
				if json.Unmarshal(value, &raws) != nil {
					return result, errors.New("invalid address attributes")
				}
			}
			byValue := map[string]facts.Availability{}
			for _, addrRaw := range raws {
				var addr struct {
					Family string `json:"family"`
					Local  string `json:"local"`
					Prefix int64  `json:"prefixlen"`
					Scope  string `json:"scope"`
				}
				encoded, _ := json.Marshal(addrRaw)
				if json.Unmarshal(encoded, &addr) != nil {
					return result, errors.New("invalid address fields")
				}
				key, _ := json.Marshal(facts.Address{Family: addr.Family, Address: addr.Local, PrefixLength: addr.Prefix, Scope: addr.Scope})
				item := facts.Availability{Status: "observed", Fields: map[string]facts.Availability{}}
				for attr, field := range map[string]string{"family": "family", "local": "address", "prefixlen": "prefix_length", "scope": "scope"} {
					a, err := attributeAvailability(addrRaw, attr)
					if err != nil {
						return result, err
					}
					item.Fields[field] = a
				}
				byValue[string(key)] = item
			}
			if len(interfaces[name].Addresses) > 0 {
				availability.Entries = map[string]facts.Availability{}
			}
			for i, addr := range interfaces[name].Addresses {
				key, _ := json.Marshal(addr)
				availability.Entries[strconv.Itoa(i)] = byValue[string(key)]
			}
			entry.Fields["addresses"] = availability
		}
		result.Entries[name] = entry
	}
	return result, nil
}
