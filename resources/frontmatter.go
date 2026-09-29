package resources

import (
	"fmt"
	"strings"
)

// ParseFrontmatter splits "---\nkey: value\n---\nbody". Values are plain
// strings; quotes around a value are removed.
//
// A file with no frontmatter is not an error: the whole text is the body. A file
// that opens frontmatter and then breaks it is. Use ParseFrontmatterChecked to
// tell those apart.
func ParseFrontmatter(text string) (map[string]string, string) {
	meta, body, _ := ParseFrontmatterChecked(text)
	return meta, body
}

// ParseFrontmatterChecked is ParseFrontmatter with the malformed cases
// reported. It returns an error when the text opens a frontmatter block with
// "---" and then never closes it, and a warning-shaped error for a block that
// closes but whose lines are not "key: value".
//
// The body is still returned in both cases, because the rest of the file is
// usually usable, so callers should report the error and carry on.
func ParseFrontmatterChecked(text string) (map[string]string, string, error) {
	meta := map[string]string{}
	t := strings.TrimLeft(text, "\uFEFF")
	if !strings.HasPrefix(t, "---") {
		return meta, text, nil
	}
	rest := t[3:]
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return meta, text, nil
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, text, fmt.Errorf("frontmatter opens with --- but is never closed")
	}
	block := rest[:end]
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	var malformed []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			malformed = append(malformed, trimmed)
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		meta[strings.TrimSpace(k)] = v
	}
	if len(malformed) > 0 {
		return meta, body, fmt.Errorf("frontmatter line is not \"key: value\": %s", strings.Join(malformed, "; "))
	}
	return meta, body, nil
}
