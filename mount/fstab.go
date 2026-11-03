package mount

import (
	"errors"
	"fmt"
	"strings"

	"github.com/subuk/richt/plan"
)

func escape(s string) string {
	return strings.NewReplacer("\\", `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`).Replace(s)
}
func unescape(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\").Replace(s)
}

// editFstab only replaces entries for one target, retaining every unrelated byte.
// Compatible duplicate entries collapse into one; conflicting entries fail.
func editFstab(before string, c *plan.MountChange, compatible func(string) bool, canonicalize ...func(string) (string, error)) (string, error) {
	lines := strings.SplitAfter(before, "\n")
	result := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		body := strings.TrimSuffix(line, "\n")
		content, comment, hasComment := splitComment(body)
		fields := strings.Fields(content)
		if len(fields) == 0 {
			result = append(result, line)
			continue
		}
		if len(fields) < 4 {
			return "", errors.New("invalid fstab entry")
		}
		observedTarget := unescape(fields[1])
		matches := observedTarget == c.Target
		if !matches && strings.HasPrefix(observedTarget, "/") && len(canonicalize) > 0 {
			canonical, err := canonicalize[0](observedTarget)
			if err != nil {
				return "", err
			}
			matches = canonical == c.Target
		}
		if !matches {
			result = append(result, line)
			continue
		}
		if unescape(fields[2]) != c.Filesystem || !compatible(unescape(fields[0])) {
			return "", fmt.Errorf("fstab target %s has an incompatible source or filesystem", c.Target)
		}
		if c.State == "absent" {
			if hasComment {
				result = append(result, "#"+comment+"\n")
			}
			continue
		}
		if found {
			if hasComment {
				result = append(result, "#"+comment+"\n")
			}
			continue
		}
		found = true
		// Keep a compatible entry byte-for-byte when its effective options match.
		if observedTarget == c.Target && unescape(fields[0]) == "UUID="+c.Identity.UUID && sameOptions(fields[3], c.Options) {
			result = append(result, line)
			continue
		}
		entry := fstabEntry(c)
		if hasComment {
			entry += " #" + comment
		}
		result = append(result, entry+"\n")
	}
	after := strings.Join(result, "")
	if c.State == "present" && !found {
		if after != "" && !strings.HasSuffix(after, "\n") {
			after += "\n"
		}
		after += fstabEntry(c) + "\n"
	}
	return after, nil
}
func fstabEntry(c *plan.MountChange) string {
	options := strings.Join(c.Options, ",")
	if options == "" {
		options = "defaults"
	}
	// Filesystem UUIDs persist across device aliases and boot-time renumbering.
	return "UUID=" + escape(c.Identity.UUID) + "\t" + escape(c.Target) + "\t" + c.Filesystem + "\t" + options + "\t0\t0"
}
func sameOptions(observed string, desired []string) bool {
	have := map[string]bool{}
	for _, o := range strings.Split(observed, ",") {
		have[o] = true
	}
	want := map[string]bool{}
	for _, o := range desired {
		want[o] = true
	}
	if len(want) == 0 {
		want["defaults"] = true
	}
	if len(have) != len(want) {
		return false
	}
	for o := range want {
		if !have[o] {
			return false
		}
	}
	return true
}

// A hash embedded in a field (for example a directory name) is not a comment.
func splitComment(line string) (string, string, bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i], line[i+1:], true
		}
	}
	return line, "", false
}
