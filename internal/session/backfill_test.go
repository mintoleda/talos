package session

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTranscript(t *testing.T, projectDir, id, content string) string {
	t.Helper()
	dir := filepath.Join(SessionsDir(), ProjectHash(projectDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackfillMetasByCandidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "repos", "myproj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, proj, "aaa111", `{"role":"user","content":[{"type":"text","text":"hi"}]}`)

	report, err := BackfillMetas([]string{proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Created) != 1 || report.Created[0].ID != "aaa111" || report.Created[0].Dir != proj {
		t.Fatalf("created = %+v", report.Created)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v", report.Unresolved)
	}
	metas, err := ListAllSessionMetas()
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListAllSessionMetas = %v, %v", metas, err)
	}
}

func TestBackfillMetasByTranscriptMining(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "work", "deep", "nested", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// Transcript mentions a file inside the project; no candidate passed.
	content := `{"role":"assistant","content":[{"type":"text","text":"see ` + proj + `/src/main.go for details"}]}`
	writeTranscript(t, proj, "bbb222", content)

	report, err := BackfillMetas(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Created) != 1 || report.Created[0].Dir != proj {
		t.Fatalf("created = %+v, unresolved = %v", report.Created, report.Unresolved)
	}
}

func TestBackfillMetasUnresolved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Bucket hash matches nothing findable: no candidates, no paths in text.
	writeTranscript(t, "/nonexistent/gone", "ccc333", `{"role":"user","content":[{"type":"text","text":"just chat"}]}`)

	report, err := BackfillMetas(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Created) != 0 || len(report.Unresolved) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestBackfillMetasSkipsExistingMeta(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, proj, "ddd444", `{}`)
	if err := WriteSessionMeta(SessionMeta{ID: "ddd444", Dir: proj, ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}

	report, err := BackfillMetas([]string{proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Created) != 0 || len(report.Unresolved) != 0 {
		t.Fatalf("report = %+v", report)
	}
}
