package transport

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// parseOSRelease implements assignments as data. Expansion, command execution,
// concatenated quoted strings, and shell statements are never evaluated.
func parseOSRelease(raw string) (map[string]string, error) {
	bad := errors.New("malformed OS release metadata")
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') {
		return nil, bad
	}
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, bad
		}
		for i, c := range key {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
				return nil, bad
			}
		}
		var result strings.Builder
		quote := byte(0)
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote = value[0]
			value = value[1:]
		}
		closed := quote == 0
		for i := 0; i < len(value); i++ {
			c := value[i]
			if quote != 0 && c == quote {
				if strings.TrimSpace(value[i+1:]) != "" {
					return nil, bad
				}
				closed = true
				break
			}
			if c == '\\' && quote != '\'' {
				if i+1 == len(value) {
					return nil, bad
				}
				i++
				next := value[i]
				if quote == '"' && !strings.ContainsRune("$`\"\\", rune(next)) {
					result.WriteByte('\\')
				}
				result.WriteByte(next)
				continue
			}
			if quote == 0 && (c == '\'' || c == '"' || c == ' ' || c == '\t' || c == '\r' || c == ';') {
				return nil, bad
			}
			result.WriteByte(c)
		}
		if !closed {
			return nil, bad
		}
		out[key] = result.String()
	}
	return out, nil
}
