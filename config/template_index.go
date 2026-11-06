package config

import (
	"errors"
	"reflect"

	"github.com/subuk/richt/facts"
)

// templateIndex retains Go template index behavior for variables and dynamic
// interface dictionaries. Schema records additionally reject unknown fields:
// text/template's missingkey option only protects dot lookup, not built-in index.
func templateIndex(value reflect.Value, indexes ...reflect.Value) (reflect.Value, error) {
	unwrap := func(v reflect.Value) reflect.Value {
		for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		return v
	}
	if !value.IsValid() {
		return reflect.Value{}, errors.New("index of nil value")
	}
	for _, key := range indexes {
		value = unwrap(value)
		// A pointer can itself be a map key. Only unwrap interfaces here.
		for key.IsValid() && key.Kind() == reflect.Interface {
			key = key.Elem()
		}
		if !value.IsValid() {
			return reflect.Value{}, errors.New("index of nil value")
		}
		switch value.Kind() {
		case reflect.Map:
			keyType := value.Type().Key()
			if !key.IsValid() {
				switch keyType.Kind() {
				case reflect.Interface, reflect.Pointer, reflect.Chan:
					key = reflect.Zero(keyType)
				default:
					return reflect.Value{}, errors.New("invalid map index")
				}
			} else if !key.Type().AssignableTo(keyType) {
				if integerKind(key.Kind()) && integerKind(keyType.Kind()) && key.CanConvert(keyType) {
					key = key.Convert(keyType)
				} else {
					return reflect.Value{}, errors.New("invalid map index type")
				}
			}
			if !key.Comparable() {
				return reflect.Value{}, errors.New("map index is not comparable")
			}
			entry := value.MapIndex(key)
			if !entry.IsValid() {
				if _, strict := value.Interface().(facts.Record); strict {
					return reflect.Value{}, errors.New("unknown fact field")
				}
				entry = reflect.Zero(value.Type().Elem())
			}
			value = entry
		case reflect.Array, reflect.Slice, reflect.String:
			if !key.IsValid() || !integerKind(key.Kind()) {
				return reflect.Value{}, errors.New("invalid sequence index")
			}
			var n uint64
			if key.Kind() >= reflect.Int && key.Kind() <= reflect.Int64 {
				if key.Int() < 0 {
					return reflect.Value{}, errors.New("sequence index out of range")
				}
				n = uint64(key.Int())
			} else {
				n = key.Uint()
			}
			if n >= uint64(value.Len()) {
				return reflect.Value{}, errors.New("sequence index out of range")
			}
			value = value.Index(int(n))
		default:
			return reflect.Value{}, errors.New("value cannot be indexed")
		}
	}
	return value, nil
}

func integerKind(kind reflect.Kind) bool {
	return kind >= reflect.Int && kind <= reflect.Uintptr
}
