// Package secrets resolves local secret sources during plan preparation.
// Source contents and lookup arguments are never included in errors.
package secrets

import (
	"errors"
	"strings"

	"github.com/subuk/richt/inventory"
)

var (
	errInvalidKey        = errors.New("secret key is invalid")
	errUnknownSource     = errors.New("secret source is not declared")
	errUnsupportedSource = errors.New("secret source type is unsupported")
	errReadSource        = errors.New("secret source could not be read")
	errInvalidSource     = errors.New("secret source must contain one YAML mapping with string keys and values")
	errUnknownKey        = errors.New("secret key was not found")
)

// Source is a backend for retrieving a secret by its dot-separated key.
// Implementations must keep secret values and lookup arguments out of errors.
type Source interface {
	Lookup(key string) (string, error)
}

// Resolver owns independent source caches for one preparation. It is safe for
// concurrent lookups. Construct a new Resolver for each new diff.
type Resolver struct {
	sources map[string]Source
}

// New copies source declarations without opening any source files. Each YAML
// source is read on its first lookup and retains that result, including errors,
// for the lifetime of the Resolver.
func New(sources map[string]inventory.SecretSource) *Resolver {
	r := &Resolver{sources: make(map[string]Source, len(sources))}
	for name, declaration := range sources {
		if declaration.Type == "yaml" {
			r.sources[name] = &yamlSource{file: declaration.File}
		} else {
			r.sources[name] = unsupportedSource{}
		}
	}
	return r
}

// Lookup selects exactly the named source. Callers choose any default source;
// the resolver never searches other sources or falls back after a failure.
func (r *Resolver) Lookup(key, source string) (string, error) {
	if !validKey(key) {
		return "", errInvalidKey
	}
	if r == nil {
		return "", errUnknownSource
	}
	backend, ok := r.sources[source]
	if !ok {
		return "", errUnknownSource
	}
	return backend.Lookup(key)
}

func validKey(key string) bool {
	for part := range strings.SplitSeq(key, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

type unsupportedSource struct{}

func (unsupportedSource) Lookup(string) (string, error) { return "", errUnsupportedSource }
