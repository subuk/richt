package transport

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"

	"github.com/subuk/richt/facts"
)

type ipRoute struct {
	Destination   string          `json:"dst"`
	Table         json.RawMessage `json:"table"`
	Type          string          `json:"type"`
	Interface     string          `json:"dev"`
	Gateway       string          `json:"gateway"`
	Metric        *int64          `json:"metric"`
	Priority      *int64          `json:"priority"`
	Preference    string          `json:"pref"`
	Flags         []string        `json:"flags"`
	NexthopID     json.RawMessage `json:"nhid"`
	Nexthops      []ipRoute       `json:"nexthops"`
	Via           json.RawMessage `json:"via"`
	Source        string          `json:"from"`
	Encapsulation json.RawMessage `json:"encap"`
}

func routeUnusable(r ipRoute) bool {
	if r.Type != "" && r.Type != "unicast" {
		return true
	}
	for _, flag := range r.Flags {
		if flag == "dead" || flag == "linkdown" {
			return true
		}
	}
	return false
}

func parseDefaultRoutes(data []byte, ipv6 bool, interfaces map[string]facts.Interface) (facts.DefaultRoute, error) {
	result := facts.DefaultRoute{Status: "absent"}
	var routes []*ipRoute
	if err := decodeFactJSON(data, &routes); err != nil {
		return result, err
	}
	if routes == nil {
		return result, errors.New("malformed route list")
	}
	bestMetric := int64(1<<63 - 1)
	bestPreference := 4
	paths := map[string]facts.DefaultRoute{}
	unknown := false
	found := false
	for _, record := range routes {
		if record == nil {
			return result, errors.New("malformed route record")
		}
		r := *record
		mainTable, err := routeMainTable(r.Table)
		if err != nil {
			return result, err
		}
		if !mainTable {
			continue
		}
		if r.Destination != "" && r.Destination != "default" && r.Destination != "0.0.0.0/0" && r.Destination != "::/0" {
			continue
		}
		if routeUnusable(r) {
			continue
		}
		if len(r.Nexthops) > 0 {
			usable := false
			for _, n := range r.Nexthops {
				if !routeUnusable(n) {
					usable = true
				}
			}
			if !usable {
				continue
			}
		}
		metric := int64(0)
		if r.Metric != nil {
			metric = *r.Metric
		}
		if r.Priority != nil {
			if r.Metric != nil && *r.Priority != metric {
				return result, errors.New("conflicting route priority")
			}
			metric = *r.Priority
		}
		if metric < 0 {
			return result, errors.New("invalid route metric")
		}
		preference := 1
		if ipv6 {
			switch r.Preference {
			case "high":
				preference = 0
			case "", "medium":
				preference = 1
			case "low":
				preference = 2
			default:
				return result, errors.New("invalid IPv6 route preference")
			}
		}
		if metric > bestMetric || metric == bestMetric && preference > bestPreference {
			continue
		}
		if metric < bestMetric || metric == bestMetric && preference < bestPreference {
			paths = map[string]facts.DefaultRoute{}
			unknown = false
			bestMetric = metric
			bestPreference = preference
			found = false
		}
		found = true
		next := r.Nexthops
		if len(next) == 0 {
			next = []ipRoute{r}
		}
		usable := false
		for _, n := range next {
			if routeUnusable(n) {
				continue
			}
			usable = true
			if len(r.NexthopID) > 0 && string(r.NexthopID) != "null" || len(n.NexthopID) > 0 && string(n.NexthopID) != "null" || len(r.Via) > 0 || len(n.Via) > 0 || len(r.Encapsulation) > 0 || len(n.Encapsulation) > 0 || r.Source != "" && r.Source != "all" && r.Source != "::/0" && r.Source != "0.0.0.0/0" {
				unknown = true
				continue
			}
			if _, ok := interfaces[n.Interface]; !ok || n.Interface == "" {
				unknown = true
				continue
			}
			if n.Gateway != "" {
				ip, err := netip.ParseAddr(n.Gateway)
				if err != nil || ip.Is4() == ipv6 || strings.Contains(n.Gateway, "%") {
					return result, errors.New("invalid route gateway")
				}
			}
			path := facts.DefaultRoute{Interface: n.Interface, Gateway: n.Gateway, Status: "selected"}
			paths[n.Interface+"\x00"+n.Gateway] = path
		}
		if !usable {
			unknown = true
		}
	}
	if !found {
		return result, nil
	}
	if unknown || len(paths) != 1 {
		result.Status = "ambiguous"
		return result, nil
	}
	for _, r := range paths {
		return r, nil
	}
	return result, nil
}

func routeMainTable(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return true, nil
	}
	if raw[0] == '"' {
		var name string
		if json.Unmarshal(raw, &name) != nil || name == "" {
			return false, errors.New("invalid route table")
		}
		return name == "main" || name == "254", nil
	}
	number, err := strconv.ParseUint(string(raw), 10, 32)
	if err != nil || number == 0 {
		return false, errors.New("invalid route table")
	}
	return number == 254, nil
}
