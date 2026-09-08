// Package identity resolves the immutable worktree and launch identity used
// by a development session.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/route"
)

type Identity struct {
	Project         string
	Branch          string
	RepositoryRoot  string
	WorktreeRoot    string
	ConfigDirectory string
	Scope           string
	SessionID       string
}

// Resolve infers or validates all identity values without changing process
// state. randomSource is injected so tests can make launch IDs deterministic.
func Resolve(cfg config.Config, currentDir string, randomSource io.Reader) (Identity, error) {
	if cfg.Source.PrimaryPath == "" {
		return Identity{}, errors.New("configuration source path is required")
	}
	configPath, err := filepath.Abs(cfg.Source.PrimaryPath)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	configDirectory, err := canonicalExisting(filepath.Dir(configPath))
	if err != nil {
		return Identity{}, fmt.Errorf("canonicalize configuration directory: %w", err)
	}
	if currentDir == "" {
		currentDir = configDirectory
	}

	var detected gitdetect.Worktree
	gitInfo, gitErr := gitdetect.DetectWorktreeFrom(currentDir)
	if gitErr == nil {
		detected = gitInfo
	}

	worktreeRoot := cfg.WorktreeRoot
	if worktreeRoot == "" && gitErr == nil {
		worktreeRoot = detected.Root
	}
	if worktreeRoot == "" {
		return Identity{}, fmt.Errorf("resolve worktree: %w; set worktree_root, project, and branch for a non-Git directory", gitErr)
	}
	if !filepath.IsAbs(worktreeRoot) {
		worktreeRoot = filepath.Join(configDirectory, worktreeRoot)
	}
	worktreeRoot, err = canonicalExisting(worktreeRoot)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve worktree root: %w", err)
	}

	project := cfg.Project
	if project == "" && gitErr == nil {
		project = detected.Project
	}
	branch := cfg.Branch
	if branch == "" && gitErr == nil {
		branch = detected.Branch
	}
	if project == "" {
		return Identity{}, errors.New("project is required when Git project detection is unavailable")
	}
	if branch == "" {
		return Identity{}, errors.New("branch is required when Git branch detection is unavailable or detached")
	}
	if !route.ValidProjectName(project) {
		return Identity{}, fmt.Errorf("invalid project name %q", project)
	}
	if !route.ValidBranchName(branch) {
		return Identity{}, fmt.Errorf("invalid branch name %q", branch)
	}

	if randomSource == nil {
		randomSource = rand.Reader
	}
	var idBytes [18]byte
	if _, err := io.ReadFull(randomSource, idBytes[:]); err != nil {
		return Identity{}, fmt.Errorf("generate session ID: %w", err)
	}
	return Identity{
		Project:         project,
		Branch:          branch,
		RepositoryRoot:  worktreeRoot,
		WorktreeRoot:    worktreeRoot,
		ConfigDirectory: configDirectory,
		Scope:           stableScope(project, worktreeRoot),
		SessionID:       base64.RawURLEncoding.EncodeToString(idBytes[:]),
	}, nil
}

func (i Identity) ResolvePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(filepath.Join(i.ConfigDirectory, path))
}

var unsafeScopeChars = regexp.MustCompile(`[^a-z0-9_-]+`)

func stableScope(project, worktreeRoot string) string {
	prefix := strings.ToLower(project + "-" + filepath.Base(worktreeRoot))
	prefix = unsafeScopeChars.ReplaceAllString(prefix, "-")
	prefix = strings.Trim(prefix, "-_")
	if prefix == "" {
		prefix = "worktree"
	}
	digest := sha256.Sum256([]byte(project + "\x00" + worktreeRoot))
	return fmt.Sprintf("%s-%x", prefix, digest[:8])
}

func canonicalExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path does not exist: %s", absolute)
		}
		return "", err
	}
	return filepath.Clean(resolved), nil
}
