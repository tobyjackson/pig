package resources

import "strings"

// ParseFrontmatter splits "---\nkey: value\n---\nbody". Values are plain
// strings; quotes around a value are removed.
func ParseFrontmatter(text string) (map[string]string, string) {
	meta := map[string]string{}
	t := strings.TrimLeft(text, "\uFEFF")
	if !strings.HasPrefix(t, "---") {
		return meta, text
	}
	rest := t[3:]
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return meta, text
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, text
	}
	block := rest[:end]
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		meta[strings.TrimSpace(k)] = v
	}
	return meta, body
}
