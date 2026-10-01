package resources

import (
	"os"
	"path/filepath"
	"strings"
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
	skills := LoadSkills(t.TempDir(), false, nil, nil)
	if len(skills) != 1 || skills[0].Name != "pdf" {
		t.Fatalf("skills: %+v", skills)
	}
	if !contains(FormatSkillsForPrompt(skills, "read"), "<name>pdf</name>") {
		t.Fatal("prompt format missing skill")
	}
	prompts := LoadPromptTemplates(t.TempDir(), false, nil, nil)
	if len(prompts) != 1 || prompts[0].Name != "review" || prompts[0].Expand([]string{"x"}) != "Review x" {
		t.Fatalf("prompts: %+v", prompts)
	}
}

// A malformed frontmatter block used to be skipped silently, which made a
// template or skill vanish from the UI with no explanation.
func TestMalformedFrontmatterIsReported(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "broken.md"),
		[]byte("---\ndescription is missing its colon\n---\nBody"), 0o644)
	var warned []string
	prompts := LoadPromptTemplates(t.TempDir(), false, nil, func(msg string) { warned = append(warned, msg) })
	if len(warned) == 0 {
		t.Fatal("malformed frontmatter was not reported")
	}
	if !strings.Contains(strings.Join(warned, " "), "key: value") {
		t.Fatalf("warning is unhelpful: %v", warned)
	}
	// The body is still usable, so the template stays loaded.
	if len(prompts) != 1 || prompts[0].Name != "broken" {
		t.Fatalf("prompts: %+v", prompts)
	}

	// An unterminated block is reported too, and must not leak "---" into the
	// description.
	os.WriteFile(filepath.Join(dir, "prompts", "unterminated.md"),
		[]byte("---\ndescription: never closed\nBody"), 0o644)
	warned = nil
	prompts = LoadPromptTemplates(t.TempDir(), false, nil, func(msg string) { warned = append(warned, msg) })
	if len(warned) == 0 || !strings.Contains(strings.Join(warned, " "), "never closed") {
		t.Fatalf("unterminated frontmatter not reported: %v", warned)
	}
	for _, p := range prompts {
		if p.Name == "unterminated" && p.Description == "---" {
			t.Fatal("description leaked the frontmatter marker")
		}
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

func TestContextFilesPreferOnePerFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", filepath.Join(dir, "global"))
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents"), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude"), 0o644)
	files := LoadContextFiles(dir)
	if len(files) != 1 || files[0].Content != "agents" {
		t.Fatalf("AGENTS.md should win alone, got %+v", files)
	}

	// CLAUDE.md is used when there is no AGENTS.md.
	os.Remove(filepath.Join(dir, "AGENTS.md"))
	files = LoadContextFiles(dir)
	if len(files) != 1 || files[0].Content != "claude" {
		t.Fatalf("CLAUDE.md should be the fallback, got %+v", files)
	}

	// The override replaces both.
	os.WriteFile(filepath.Join(dir, "AGENTS.override.md"), []byte("override"), 0o644)
	files = LoadContextFiles(dir)
	if len(files) != 1 || files[0].Content != "override" {
		t.Fatalf("override should win, got %+v", files)
	}
}

func TestGlobalContextFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("global rules"), 0o644)
	files := LoadContextFiles(t.TempDir())
	if len(files) != 1 || files[0].Content != "global rules" {
		t.Fatalf("got %+v", files)
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
