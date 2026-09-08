package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectWorktreeFromNestedDirectory(t *testing.T) {
	tmp := t.TempDir()
	runGit(t, tmp, "init", "repo")
	repo := filepath.Join(tmp, "repo")
	writeFile(t, filepath.Join(repo, "README.md"), "test\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	nested := filepath.Join(repo, "nested", "dir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectWorktreeFrom(nested)
	if err != nil {
		t.Fatalf("DetectWorktreeFrom() error = %v", err)
	}
	if got.Root != repo || got.Project != "repo" || got.Branch != "master" && got.Branch != "main" {
		t.Fatalf("DetectWorktreeFrom() = %+v", got)
	}
}

func TestDetectWorktreeFromDetachedHeadRequiresOverride(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	runGit(t, tmp, "init", repo)
	writeFile(t, filepath.Join(repo, "README.md"), "test\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGit(t, repo, "checkout", "--detach", "HEAD")

	if _, err := DetectWorktreeFrom(repo); err == nil {
		t.Fatal("DetectWorktreeFrom() error = nil, want detached-head error")
	}
}
