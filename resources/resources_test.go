package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrontmatter(t *testing.T) {
	meta, body := ParseFrontmatter("---\nname: my-skill\ndescription: \"Does things\"\n---\n# Title\nbody")
	if meta["name"] != "my-skill" || meta["description"] != "Does things" || body != "# Title\nbody" {
		t.Fatalf("got %v %q", meta, body)
	}
	meta, body = ParseFrontmatter("no frontmatter")
	if len(meta) != 0 || body != "no frontmatter" {
		t.Fatal("plain text should pass through")
	}
}

func TestPromptExpand(t *testing.T) {
	p := PromptTemplate{Body: "a=$1 b=${2:-def} all=$@ rest=${@:2} two=${@:1:2} args=$ARGUMENTS"}
	got := p.Expand([]string{"x", "y", "z"})
	want := "a=x b=y all=x y z rest=y z two=x y args=x y z"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if p.Expand([]string{"x"}) != "a=x b=def all=x rest= two=x args=x" {
		t.Fatalf("defaults: %q", p.Expand([]string{"x"}))
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`one "two words" 'three'`)
	if len(got) != 3 || got[1] != "two words" || got[2] != "three" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadSkillsAndPrompts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "skills", "pdf"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "pdf", "SKILL.md"), []byte("---\nname: pdf\ndescription: Work with PDFs\n---\nSteps"), 0o644)
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "review.md"), []byte("---\ndescription: Review code\n---\nReview $1"), 0o644)
	skills := LoadSkills(t.TempDir(), false, nil)
	if len(skills) != 1 || skills[0].Name != "pdf" {
		t.Fatalf("skills: %+v", skills)
	}
	if !contains(FormatSkillsForPrompt(skills, "read"), "<name>pdf</name>") {
		t.Fatal("prompt format missing skill")
	}
	prompts := LoadPromptTemplates(t.TempDir(), false, nil)
	if len(prompts) != 1 || prompts[0].Name != "review" || prompts[0].Expand([]string{"x"}) != "Review x" {
		t.Fatalf("prompts: %+v", prompts)
	}
}

func TestContextFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", filepath.Join(dir, "global"))
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("root rules"), 0o644)
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "AGENTS.override.md"), []byte("override"), 0o644)
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("ignored"), 0o644)
	files := LoadContextFiles(sub)
	var contents []string
	for _, f := range files {
		contents = append(contents, f.Content)
	}
	if len(files) != 2 || contents[0] != "root rules" || contents[1] != "override" {
		t.Fatalf("got %v", contents)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool { return indexOf(s, sub) >= 0 })()
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
