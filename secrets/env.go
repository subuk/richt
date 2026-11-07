package secrets

import (
	"os"
	"sync"
	"unicode/utf8"
)

type envSource struct {
	env    map[string]string
	once   sync.Once
	values map[string]string
	err    error
}

func (s *envSource) Lookup(key string) (string, error) {
	s.once.Do(s.load)
	if s.err != nil {
		return "", s.err
	}
	value, ok := s.values[key]
	if !ok {
		return "", errUnknownKey
	}
	return value, nil
}

func (s *envSource) load() {
	values := make(map[string]string, len(s.env))
	variables := make(map[string]string, len(s.env))
	for key, name := range s.env {
		value, ok := variables[name]
		if !ok {
			value, ok = os.LookupEnv(name)
			if !ok {
				s.err = errReadSource
				return
			}
			if !utf8.ValidString(value) {
				s.err = errInvalidEnvValue
				return
			}
			variables[name] = value
		}
		values[key] = value
	}
	s.values = values
}
