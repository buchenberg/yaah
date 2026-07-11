package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover_findsSkillsInDir(t *testing.T) {
	tmp := t.TempDir()
	// Create two skills
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(tmp, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+name+" skill\n---\n\nbody"), 0o644)
	}

	skills := Discover([]string{tmp})
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %v", len(skills), skills)
	}

	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestDiscover_skipsDirsWithoutSKILLmd(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "no-skill"), 0o755)
	os.WriteFile(filepath.Join(tmp, "no-skill", "README.md"), []byte("not a skill"), 0o644)

	skills := Discover([]string{tmp})
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscover_multipleDirsFirstWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Same skill name in both dirs — first dir wins
	for _, dir := range []string{dir1, dir2} {
		d := filepath.Join(dir, "my-skill")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: my-skill\ndescription: from "+dir+"\n---\n\nbody"), 0o644)
	}

	skills := Discover([]string{dir1, dir2})
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Description != "from "+dir1 {
		t.Errorf("expected first dir wins, got description %q", skills[0].Description)
	}
}

func TestDiscover_returnsEmptyOnMissingDirs(t *testing.T) {
	skills := Discover([]string{"/nonexistent/path"})
	if len(skills) != 0 {
		t.Errorf("expected 0 skills for missing dir, got %d", len(skills))
	}
}

func TestParseFrontmatter_extractsFields(t *testing.T) {
	md := `---
name: test-skill
description: A test skill for yaah
---

# Skill body

Some instructions here.
`
	skill, err := ParseFrontmatter([]byte(md))
	if err != nil {
		t.Fatalf("ParseFrontmatter() error: %v", err)
	}
	if skill.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "test-skill")
	}
	if skill.Description != "A test skill for yaah" {
		t.Errorf("Description = %q", skill.Description)
	}
	if len(skill.Body) == 0 {
		t.Error("Body is empty")
	}
}

func TestParseFrontmatter_returnsErrorOnMissingFrontmatter(t *testing.T) {
	md := `# Just a heading

No frontmatter here.
`
	_, err := ParseFrontmatter([]byte(md))
	if err == nil {
		t.Error("expected error for missing frontmatter")
	}
}
