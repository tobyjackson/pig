// Package docs embeds pig's documentation so `pig docs` works anywhere.
package docs

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// Names lists the available documents without the .md suffix.
func Names() []string {
	entries, _ := files.ReadDir(".")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

// Read returns one document's text.
func Read(name string) (string, bool) {
	data, err := files.ReadFile(strings.TrimSuffix(name, ".md") + ".md")
	if err != nil {
		return "", false
	}
	return string(data), true
}
