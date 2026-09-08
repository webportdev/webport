package identity

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
)

func TestResolveGitIdentityAndConfigRelativePath(t *testing.T) {
	repo := newRepo(t, "project")
	configDir := filepath.Join(repo, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, ".webport.yaml")
	writeFile(t, path, "version: 1\n")
	cfg := config.Config{Source: config.Source{PrimaryPath: path}}

	got, err := Resolve(cfg, configDir, bytes.NewReader(bytes.Repeat([]byte{1}, 18)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "project" || got.WorktreeRoot != repo || got.ConfigDirectory != configDir {
		t.Fatalf("identity = %+v", got)
	}
	// The exact base64 representation is less important than stable length;
	// this guard catches accidental use of a filesystem path as the ID.
	if len(got.SessionID) != 24 {
		t.Fatalf("session ID = %q", got.SessionID)
	}
	resolved, err := got.ResolvePath(".webport/api.log")
	if err != nil || resolved != filepath.Join(configDir, ".webport/api.log") {
		t.Fatalf("ResolvePath() = %q, %v", resolved, err)
	}
}

func TestResolveStableScopeDiffersForSameNamedDirectories(t *testing.T) {
	parent := t.TempDir()
	first := newRepoAt(t, filepath.Join(parent, "one", "app"), "app")
	second := newRepoAt(t, filepath.Join(parent, "two", "app"), "app")

	firstCfg := config.Config{Source: config.Source{PrimaryPath: filepath.Join(first, ".webport.yaml")}}
	secondCfg := config.Config{Source: config.Source{PrimaryPath: filepath.Join(second, ".webport.yaml")}}
	firstID, err := Resolve(firstCfg, first, bytes.NewReader(bytes.Repeat([]byte{2}, 18)))
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := Resolve(secondCfg, second, bytes.NewReader(bytes.Repeat([]byte{2}, 18)))
	if err != nil {
		t.Fatal(err)
	}
	if firstID.Scope == secondID.Scope {
		t.Fatalf("same scope for different roots: %q", firstID.Scope)
	}
	if firstID.Scope != stableScope("app", firstID.WorktreeRoot) {
		t.Fatal("scope is not stable for one worktree")
	}
}

func TestResolveNonGitOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".webport.yaml")
	writeFile(t, configPath, "version: 1\n")
	cfg := config.Config{
		Project:      "manual",
		Branch:       "local",
		WorktreeRoot: dir,
		Source:       config.Source{PrimaryPath: configPath},
	}
	got, err := Resolve(cfg, dir, bytes.NewReader(bytes.Repeat([]byte{3}, 18)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "manual" || got.Branch != "local" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestResolveCanonicalizesSymlinkedInvocation(t *testing.T) {
	repo := newRepo(t, "linked")
	linkParent := filepath.Join(t.TempDir(), "link-parent")
	if err := os.Symlink(repo, linkParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".webport.yaml")
	writeFile(t, path, "version: 1\n")
	cfg := config.Config{Source: config.Source{PrimaryPath: path}}
	got, err := Resolve(cfg, filepath.Join(linkParent, "nested"), bytes.NewReader(bytes.Repeat([]byte{4}, 18)))
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreeRoot != repo {
		t.Fatalf("worktree root = %q, want %q", got.WorktreeRoot, repo)
	}
}

func newRepo(t *testing.T, name string) string {
	return newRepoAt(t, filepath.Join(t.TempDir(), name), name)
}

func newRepoAt(t *testing.T, path, name string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(path), "init", path)
	writeFile(t, filepath.Join(path, "README.md"), "test\n")
	runGit(t, path, "add", "README.md")
	runGit(t, path, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	return path
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
