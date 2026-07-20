package plans

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover_findsPlansInDir(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(tmp, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte("---\nname: "+name+"\ndescription: "+name+" plan\nstatus: draft\n---\n\nbody"), 0o644)
	}

	plans := Discover([]string{tmp})
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d: %v", len(plans), plans)
	}

	names := map[string]bool{}
	for _, p := range plans {
		names[p.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestDiscover_skipsDirsWithoutPLANmd(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "not-a-plan"), 0o755)
	os.WriteFile(filepath.Join(tmp, "not-a-plan", "README.md"), []byte("not a plan"), 0o644)

	plans := Discover([]string{tmp})
	if len(plans) != 0 {
		t.Errorf("expected 0 plans, got %d", len(plans))
	}
}

func TestDiscover_multipleDirsFirstWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for _, dir := range []string{dir1, dir2} {
		d := filepath.Join(dir, "my-plan")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "PLAN.md"), []byte("---\nname: my-plan\ndescription: from "+dir+"\nstatus: draft\n---\n\nbody"), 0o644)
	}

	plans := Discover([]string{dir1, dir2})
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].Description != "from "+dir1 {
		t.Errorf("expected first dir wins, got description %q", plans[0].Description)
	}
}

func TestDiscover_returnsEmptyOnMissingDirs(t *testing.T) {
	plans := Discover([]string{"/nonexistent/path"})
	if len(plans) != 0 {
		t.Errorf("expected 0 plans for missing dir, got %d", len(plans))
	}
}

func TestParseFrontmatter_extractsFields(t *testing.T) {
	md := `---
name: my-plan
description: A test plan
status: approved
---

# Plan body

Step 1: Do the thing.
`
	plan, err := ParseFrontmatter([]byte(md))
	if err != nil {
		t.Fatalf("ParseFrontmatter() error: %v", err)
	}
	if plan.Name != "my-plan" {
		t.Errorf("Name = %q, want %q", plan.Name, "my-plan")
	}
	if plan.Description != "A test plan" {
		t.Errorf("Description = %q", plan.Description)
	}
	if plan.Status != "approved" {
		t.Errorf("Status = %q, want %q", plan.Status, "approved")
	}
	if len(plan.Body) == 0 {
		t.Error("Body is empty")
	}
}

func TestParseFrontmatter_defaultsStatusToDraft(t *testing.T) {
	md := `---
name: no-status
description: Missing status field
---

body
`
	plan, err := ParseFrontmatter([]byte(md))
	if err != nil {
		t.Fatalf("ParseFrontmatter() error: %v", err)
	}
	if plan.Status != "draft" {
		t.Errorf("Status = %q, want %q", plan.Status, "draft")
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

func TestCreate_writesPlanFile(t *testing.T) {
	tmp := t.TempDir()
	path, err := Create(tmp, "my-plan", "A test plan", "# Instructions\n\nDo stuff.")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	plan, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error: %v", err)
	}
	if plan.Name != "my-plan" {
		t.Errorf("Name = %q, want %q", plan.Name, "my-plan")
	}
	if plan.Description != "A test plan" {
		t.Errorf("Description = %q", plan.Description)
	}
	if plan.Status != "draft" {
		t.Errorf("Status = %q, want %q", plan.Status, "draft")
	}
	if plan.Body != "# Instructions\n\nDo stuff." {
		t.Errorf("Body = %q", plan.Body)
	}
}

func TestCreate_rejectsDuplicate(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "dup", "first", "body"); err != nil {
		t.Fatalf("first Create() error: %v", err)
	}
	_, err := Create(tmp, "dup", "second", "body")
	if err == nil {
		t.Error("expected error for duplicate plan")
	}
}

func TestEdit_updatesDescription(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "edit-me", "old desc", "old body"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "edit-me")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}

	path, err := Edit(p, "new desc", "", "")
	if err != nil {
		t.Fatalf("Edit() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	plan, _ := ParseFrontmatter(data)
	if plan.Description != "new desc" {
		t.Errorf("Description = %q, want %q", plan.Description, "new desc")
	}
	if plan.Body != "old body" {
		t.Errorf("Body = %q, want %q", plan.Body, "old body")
	}
	if plan.Status != "draft" {
		t.Errorf("Status = %q, want %q", plan.Status, "draft")
	}
}

func TestEdit_updatesStatus(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "edit-status", "desc", "body"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "edit-status")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}

	path, err := Edit(p, "", "in_progress", "")
	if err != nil {
		t.Fatalf("Edit() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	plan, _ := ParseFrontmatter(data)
	if plan.Status != "in_progress" {
		t.Errorf("Status = %q, want %q", plan.Status, "in_progress")
	}
}

func TestEdit_updatesBody(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "edit-body", "desc", "old body"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "edit-body")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}

	path, err := Edit(p, "", "", "new body content")
	if err != nil {
		t.Fatalf("Edit() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	plan, _ := ParseFrontmatter(data)
	if plan.Description != "desc" {
		t.Errorf("Description = %q, want %q", plan.Description, "desc")
	}
	if plan.Body != "new body content" {
		t.Errorf("Body = %q, want %q", plan.Body, "new body content")
	}
}

func TestSetStatus(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "status-test", "desc", "body"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "status-test")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}

	path, err := SetStatus(p, "completed")
	if err != nil {
		t.Fatalf("SetStatus() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	plan, _ := ParseFrontmatter(data)
	if plan.Status != "completed" {
		t.Errorf("Status = %q, want %q", plan.Status, "completed")
	}
}

func TestDelete_removesDirectory(t *testing.T) {
	tmp := t.TempDir()
	path, err := Create(tmp, "delete-me", "desc", "body")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "delete-me")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}

	if err := Delete(p); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("plan file should have been deleted")
	}

	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Error("plan directory should have been deleted")
	}
}

func TestFindPlan(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Create(tmp, "findable", "desc", "body"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	p := FindPlan([]string{tmp}, "findable")
	if p == nil {
		t.Fatal("FindPlan() returned nil")
	}
	if p.Name != "findable" {
		t.Errorf("Name = %q, want %q", p.Name, "findable")
	}

	p = FindPlan([]string{tmp}, "nonexistent")
	if p != nil {
		t.Error("FindPlan() should return nil for unknown plan")
	}
}

func TestFormatPlanForAgent(t *testing.T) {
	p := &Plan{
		Name:        "test-plan",
		Description: "A test",
		Status:      "approved",
		Body:        "## Steps\n\n1. Do it.",
		Path:        "/tmp/test-plan/PLAN.md",
		Dir:         "/tmp/test-plan",
	}

	out := FormatPlanForAgent(p)
	if len(out) == 0 {
		t.Fatal("FormatPlanForAgent() returned empty string")
	}

	// Should contain key fields
	if !contains(out, "test-plan") {
		t.Error("missing plan name in output")
	}
	if !contains(out, "approved") {
		t.Error("missing status in output")
	}
	if !contains(out, "/tmp/test-plan") {
		t.Error("missing directory in output")
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
