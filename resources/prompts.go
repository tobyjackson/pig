package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PromptTemplate is a markdown file run with /name [args].
type PromptTemplate struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string
	Path         string
	Source       string
}

// LoadPromptTemplates gathers templates from ~/.pig/prompts and .pig/prompts.
// A template whose frontmatter is malformed is reported through warn and still
// loaded, since the body is usually still usable.
func LoadPromptTemplates(cwd string, trusted bool, extra []string, warn func(string)) []PromptTemplate {
	var out []PromptTemplate
	seen := map[string]bool{}
	add := func(t PromptTemplate, ok bool) {
		if ok && !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	for _, t := range templatesInDir(filepath.Join(GlobalDir(), "prompts"), "user", warn) {
		add(t, true)
	}
	if trusted {
		for _, t := range templatesInDir(filepath.Join(ProjectDir(cwd), "prompts"), "project", warn) {
			add(t, true)
		}
	}
	for _, p := range extra {
		p = expandHome(p)
		if isDir(p) {
			for _, t := range templatesInDir(p, "path", warn) {
				add(t, true)
			}
		} else {
			add(loadTemplate(p, "path", warn))
		}
	}
	return out
}

func templatesInDir(dir, source string, warn func(string)) []PromptTemplate {
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(files)
	var out []PromptTemplate
	for _, f := range files {
		if t, ok := loadTemplate(f, source, warn); ok {
			out = append(out, t)
		}
	}
	return out
}

func loadTemplate(path, source string, warn func(string)) (PromptTemplate, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptTemplate{}, false
	}
	text := string(data)
	meta, body, ferr := ParseFrontmatterChecked(text)
	if ferr != nil && warn != nil {
		warn(fmt.Sprintf("%s: %v", path, ferr))
	}
	t := PromptTemplate{
		Name: strings.TrimSuffix(filepath.Base(path), ".md"), Body: body, Path: path, Source: source,
		Description: meta["description"], ArgumentHint: meta["argument-hint"],
	}
	if t.Description == "" {
		for _, line := range strings.Split(body, "\n") {
			s := strings.TrimSpace(line)
			// Skip blank lines and a bare frontmatter marker, which is what a
			// body looks like when the block above it was never closed.
			if s == "" || s == "---" {
				continue
			}
			t.Description = s
			break
		}
	}
	return t, true
}

var argPattern = regexp.MustCompile(`\$\{@:(\d+)(?::(\d+))?\}|\$\{(\d+):-([^}]*)\}|\$ARGUMENTS|\$@|\$(\d+)`)

// Expand fills $1, $2, $@, $ARGUMENTS, ${1:-default}, ${@:N}, ${@:N:L}.
func (t PromptTemplate) Expand(args []string) string {
	return argPattern.ReplaceAllStringFunc(t.Body, func(m string) string {
		sub := argPattern.FindStringSubmatch(m)
		switch {
		case sub[1] != "": // ${@:N} or ${@:N:L}
			n, _ := strconv.Atoi(sub[1])
			if n < 1 {
				n = 1
			}
			if n > len(args) {
				return ""
			}
			rest := args[n-1:]
			if sub[2] != "" {
				l, _ := strconv.Atoi(sub[2])
				if l < len(rest) {
					rest = rest[:l]
				}
			}
			return strings.Join(rest, " ")
		case sub[3] != "": // ${N:-default}
			n, _ := strconv.Atoi(sub[3])
			if n >= 1 && n <= len(args) && args[n-1] != "" {
				return args[n-1]
			}
			return sub[4]
		case m == "$ARGUMENTS" || m == "$@":
			return strings.Join(args, " ")
		case sub[5] != "":
			n, _ := strconv.Atoi(sub[5])
			if n >= 1 && n <= len(args) {
				return args[n-1]
			}
			return ""
		}
		return m
	})
}

// SplitArgs splits on spaces, honouring double and single quotes.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	has := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			has = true
		case c == ' ' || c == '\t' || c == '\n':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
