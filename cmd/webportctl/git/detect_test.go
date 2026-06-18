package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindGitRootSupportsWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	worktree := filepath.Join(tmp, "worktree")

	runGit(t, tmp, "init", repo)
	writeFile(t, filepath.Join(repo, "README.md"), "test\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGit(t, repo, "worktree", "add", "-b", "worktree-branch", worktree)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	}()

	got, err := findGitRoot()
	if err != nil {
		t.Fatalf("findGitRoot() error = %v", err)
	}
	if got != worktree {
		t.Errorf("findGitRoot() = %v, want %v", got, worktree)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
