package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/session"
)

func TestWorkspaceRPCs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := session.SessionMeta{ID: "workspace-test", Dir: repo, ProjectDir: repo, Isolation: "none", CreatedAt: time.Now(), LastActive: time.Now()}
	if err := session.WriteSessionMeta(meta); err != nil {
		t.Fatal(err)
	}

	d := NewDaemon(&config.Config{BaseDir: filepath.Join(home, ".talos")}, 0)
	projects, err := d.listProjects()
	if err != nil || len(projects.Projects) != 1 {
		t.Fatalf("projects = %+v, err=%v", projects, err)
	}
	if projects.Projects[0].SessionCount != 1 || projects.Projects[0].DefaultBranch != "main" {
		t.Fatalf("project metadata = %+v", projects.Projects[0])
	}
	status, err := d.gitStatus(rpc.GitStatusParams{SessionID: meta.ID})
	if err != nil || len(status.Files) != 1 || status.Files[0].Path != "README.md" {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	tree, err := fsTree(rpc.FSTreeParams{Dir: repo, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tree.Entries {
		if entry.Path == "ignored.txt" {
			t.Fatal("ignored file appeared in tree")
		}
	}
	read, err := fsRead(rpc.FSReadParams{Path: filepath.Join(repo, "README.md")})
	if err != nil || read.Content != "hello\nchanged\n" || read.Truncated {
		t.Fatalf("read = %+v, err=%v", read, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
