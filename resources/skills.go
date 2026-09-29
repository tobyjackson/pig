package resources

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill is a SKILL.md file: a name, a short description, and the path the
// model reads when the task matches.
type Skill struct {
	Name        string
	Description string
	Path        string
	Dir         string
	Source      string // user | project | path
	// True when the skill is hidden from the model and only used via /skill:name.
	DisableModelInvocation bool
}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// LoadSkills gathers skills from the standard folders plus any extra paths.
// Project folders are only read when trusted.
func LoadSkills(cwd string, trusted bool, extra []string) []Skill {
	home, _ := os.UserHomeDir()
	var dirs []struct{ path, source string }
	dirs = append(dirs,
		struct{ path, source string }{filepath.Join(GlobalDir(), "skills"), "user"},
		struct{ path, source string }{filepath.Join(home, ".agents", "skills"), "user"},
	)
	if trusted {
		dirs = append(dirs,
			struct{ path, source string }{filepath.Join(ProjectDir(cwd), "skills"), "project"},
			struct{ path, source string }{filepath.Join(cwd, ".agents", "skills"), "project"},
		)
	}
	seen := map[string]bool{}
	var out []Skill
	add := func(s Skill, ok bool) {
		if ok && !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	for _, d := range dirs {
		for _, s := range skillsInDir(d.path, d.source) {
			add(s, true)
		}
	}
	for _, p := range extra {
		p = expandHome(p)
		if isDir(p) {
			if s, ok := loadSkillFile(filepath.Join(p, "SKILL.md"), "path"); ok {
				add(s, true)
			} else {
				for _, s := range skillsInDir(p, "path") {
					add(s, true)
				}
			}
		} else if isFile(p) {
			add(loadSkillFile(p, "path"))
		}
	}
	return out
}

func skillsInDir(dir, source string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if s, ok := loadSkillFile(filepath.Join(p, "SKILL.md"), source); ok {
				out = append(out, s)
			}
		} else if strings.HasSuffix(e.Name(), ".md") {
			if s, ok := loadSkillFile(p, source); ok {
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadSkillFile(path, source string) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	meta, _ := ParseFrontmatter(string(data))
	desc := meta["description"]
	if desc == "" {
		return Skill{}, false
	}
	name := meta["name"]
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
		if filepath.Base(path) != "SKILL.md" {
			name = strings.TrimSuffix(filepath.Base(path), ".md")
		}
	}
	name = strings.ToLower(name)
	if !skillName.MatchString(name) {
		return Skill{}, false
	}
	if len(desc) > 1024 {
		desc = desc[:1024]
	}
	return Skill{
		Name: name, Description: desc, Path: path, Dir: filepath.Dir(path), Source: source,
		DisableModelInvocation: meta["disable-model-invocation"] == "true",
	}, true
}

// FormatSkillsForPrompt renders the skills list the model sees.
func FormatSkillsForPrompt(skills []Skill, readTool string) string {
	var visible []Skill
	for _, s := range skills {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
	if readTool == "bash" {
		sb.WriteString("Use bash to load a skill's file when the task matches its description.\n")
	} else {
		sb.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	}
	sb.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md) and use that absolute path in tool commands.\n\n<available_skills>\n")
	for _, s := range visible {
		sb.WriteString("  <skill>\n")
		sb.WriteString("    <name>" + escapeXML(s.Name) + "</name>\n")
		sb.WriteString("    <description>" + escapeXML(s.Description) + "</description>\n")
		sb.WriteString("    <location>" + escapeXML(s.Path) + "</location>\n")
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// SkillBody returns the text of a skill file without its frontmatter.
func SkillBody(s Skill) string {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return ""
	}
	_, body := ParseFrontmatter(string(data))
	return body
}
