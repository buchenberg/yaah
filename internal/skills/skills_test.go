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

func TestCreate_writesSkillFile(t *testing.T) {
	tmp := t.TempDir()
	path, err := Create(tmp, "my-skill", "A test skill", "# Instructions\n\nDo stuff.")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	skill, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error: %v", err)
	}
	if skill.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-skill")
	}
	if skill.Description != "A test skill" {
		t.Errorf("Description = %q", skill.Description)
	}
	if skill.Body != "# Instructions\n\nDo stuff." {
		t.Errorf("Body = %q", skill.Body)
	}
}

func TestCreate_rejectsDuplicate(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "dup", "first", "body"); err != nil {
		t.Fatalf("first Create() error: %v", err)
	}
	_, err := Create(tmp, "dup", "second", "body")
	if err == nil {
		t.Error("expected error for duplicate skill")
	}
}

func TestEdit_updatesDescription(t *testing.T) {
	tmp := t.TempDir()
	path, err := Create(tmp, "edit-me", "old desc", "old body")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	s := FindSkill([]string{tmp}, "edit-me")
	if s == nil {
		t.Fatal("FindSkill() returned nil")
	}

	path, err = Edit(s, "new desc", "")
	if err != nil {
		t.Fatalf("Edit() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	skill, _ := ParseFrontmatter(data)
	if skill.Description != "new desc" {
		t.Errorf("Description = %q, want %q", skill.Description, "new desc")
	}
	if skill.Body != "old body" {
		t.Errorf("Body = %q, want %q", skill.Body, "old body")
	}
}

func TestEdit_updatesBody(t *testing.T) {
	tmp := t.TempDir()
	path, err := Create(tmp, "edit-body", "desc", "old body")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	s := FindSkill([]string{tmp}, "edit-body")
	if s == nil {
		t.Fatal("FindSkill() returned nil")
	}

	path, err = Edit(s, "", "new body content")
	if err != nil {
		t.Fatalf("Edit() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	skill, _ := ParseFrontmatter(data)
	if skill.Description != "desc" {
		t.Errorf("Description = %q, want %q", skill.Description, "desc")
	}
	if skill.Body != "new body content" {
		t.Errorf("Body = %q, want %q", skill.Body, "new body content")
	}
}
