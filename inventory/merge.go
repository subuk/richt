package inventory

import (
	"fmt"
	"reflect"
	"sort"
)

// MergeTags resolves variables without changing tags or their nested values.
// Equal-order groups are checked before any higher-order overrides.
func MergeTags(host string, names []string, tags map[string]Tag) (map[string]any, error) {
	for _, name := range names {
		if _, ok := tags[name]; !ok {
			return nil, fmt.Errorf("host %q tags: unknown tag %q", host, name)
		}
	}
	names = append([]string(nil), names...)
	sort.Slice(names, func(i, j int) bool {
		a, b := tags[names[i]], tags[names[j]]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return names[i] < names[j]
	})
	result := map[string]any{}
	for first := 0; first < len(names); {
		last := first + 1
		for last < len(names) && tags[names[last]].Order == tags[names[first]].Order {
			last++
		}
		// Pairwise comparison preserves the identity of both conflicting tags,
		// even when compatible siblings were contributed by a third tag.
		for i := first; i < last; i++ {
			for j := i + 1; j < last; j++ {
				if p, conflict := conflictPath(tags[names[i]].Vars, tags[names[j]].Vars, "vars"); conflict {
					return nil, fmt.Errorf("host %q: %s conflicts between tags %q and %q at order %d", host, p, names[i], names[j], tags[names[i]].Order)
				}
			}
		}
		for _, name := range names[first:last] {
			result = mergeMaps(result, tags[name].Vars)
		}
		first = last
	}
	return result, nil
}

func conflictPath(a, b map[string]any, prefix string) (string, bool) {
	for _, key := range sortedKeys(a) {
		bv, ok := b[key]
		if !ok {
			continue
		}
		av := a[key]
		am, aok := av.(map[string]any)
		bm, bok := bv.(map[string]any)
		if aok && bok {
			if p, ok := conflictPath(am, bm, prefix+"."+key); ok {
				return p, true
			}
			continue
		}
		if !reflect.DeepEqual(av, bv) {
			return prefix + "." + key, true
		}
	}
	return "", false
}

func mergeMaps(a, b map[string]any) map[string]any {
	result := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		result[k] = cloneValue(v)
	}
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			if am, ok := result[k].(map[string]any); ok {
				result[k] = mergeMaps(am, bm)
				continue
			}
		}
		result[k] = cloneValue(v)
	}
	return result
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return mergeMaps(nil, v)
	case []any:
		r := make([]any, len(v))
		for i, x := range v {
			r[i] = cloneValue(x)
		}
		return r
	default:
		return v
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
