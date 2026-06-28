package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	dir := t.TempDir()

	writeSkill := func(name, content string) {
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill("deploy-docker", "# Deploy Docker — Deploy the current project to Docker\n\n## Usage\n\nRun `docker build -t project .`\n")
	writeSkill("run-tests", "Run tests with coverage and formatting.\nFull instructions here.")
	// hidden dirs and files should be ignored
	os.WriteFile(filepath.Join(dir, "_private.md"), []byte("should be ignored"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("should be ignored"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a skill"), 0644)
	// dir without SKILL.md should be ignored
	os.MkdirAll(filepath.Join(dir, "empty-dir"), 0755)

	skills, err := Scan([]Dir{{Path: dir}})
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	if skills[0].Name != "deploy-docker" {
		t.Errorf("expected deploy-docker, got %s", skills[0].Name)
	}
	if skills[0].Description != "Deploy the current project to Docker" {
		t.Errorf("expected 'Deploy the current project to Docker', got %q", skills[0].Description)
	}
	if skills[0].Path != filepath.Join(dir, "deploy-docker", "SKILL.md") {
		t.Errorf("unexpected path: %s", skills[0].Path)
	}

	if skills[1].Name != "run-tests" {
		t.Errorf("expected run-tests, got %s", skills[1].Name)
	}
	if skills[1].Description != "Run tests with coverage and formatting." {
		t.Errorf("expected 'Run tests with coverage and formatting.', got %q", skills[1].Description)
	}
}

func TestScanOverride(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	os.MkdirAll(filepath.Join(global, "deploy"), 0755)
	os.WriteFile(filepath.Join(global, "deploy", "SKILL.md"), []byte("Global deploy skill"), 0644)
	os.MkdirAll(filepath.Join(project, "deploy"), 0755)
	os.WriteFile(filepath.Join(project, "deploy", "SKILL.md"), []byte("Project-specific deploy"), 0644)

	// Later dirs override earlier ones.
	skills, err := Scan([]Dir{{Path: global, Label: "global"}, {Path: project, Label: "project"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (project overrides), got %d", len(skills))
	}
	if skills[0].Description != "Project-specific deploy" {
		t.Errorf("expected project override, got %q", skills[0].Description)
	}
	if skills[0].Path != filepath.Join(project, "deploy", "SKILL.md") {
		t.Errorf("expected project path, got %s", skills[0].Path)
	}
}

func TestScanNonexistentDir(t *testing.T) {
	skills, err := Scan([]Dir{{Path: "/nonexistent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0, got %d", len(skills))
	}
}

func TestRenderListing(t *testing.T) {
	skills := []Skill{
		{Name: "a", Description: "First skill", Path: "/x/a/SKILL.md"},
		{Name: "b", Description: "Second skill", Path: "/x/b/SKILL.md"},
	}
	out := RenderListing(skills)
	if !contains(out, "- `a` — First skill") {
		t.Errorf("missing skill a in listing")
	}
	if !contains(out, "- `b` — Second skill") {
		t.Errorf("missing skill b in listing")
	}
	if !contains(out, "skill: a") {
		t.Errorf("missing invocation for skill a")
	}
	if !contains(out, "skill: b") {
		t.Errorf("missing invocation for skill b")
	}
}

func TestRenderEmpty(t *testing.T) {
	if out := RenderListing(nil); out != "" {
		t.Errorf("expected empty, got %q", out)
	}
}

func TestExtractDescriptionSkipsFrontmatter(t *testing.T) {
	content := "---\ndescription: some yaml\n---\n\nReal description here."
	desc, err := extractDescription(writeTemp(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Real description here." {
		t.Errorf("expected 'Real description here.', got %q", desc)
	}
}

func TestExtractDescriptionSkipsBareHeading(t *testing.T) {
	content := "# Title\n\nActual description."
	desc, err := extractDescription(writeTemp(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Actual description." {
		t.Errorf("expected 'Actual description.', got %q", desc)
	}
}

func TestExtractDescriptionWithDashSep(t *testing.T) {
	content := "# Some Title - This is the description"
	desc, err := extractDescription(writeTemp(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "This is the description" {
		t.Errorf("expected 'This is the description', got %q", desc)
	}
}

func TestExtractDescriptionWithEmDashSep(t *testing.T) {
	content := "# Some Title \u2014 This is the description"
	desc, err := extractDescription(writeTemp(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "This is the description" {
		t.Errorf("expected 'This is the description', got %q", desc)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
