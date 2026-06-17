// Package git provides utilities for detecting git repository information.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectProject detects the project name from the git repository basename.
// It searches upward from the current directory to find the git root.
func DetectProject() (string, error) {
	root, err := findGitRoot()
	if err != nil {
		return "", fmt.Errorf("failed to find git root: %w", err)
	}
	return filepath.Base(root), nil
}

// DetectBranch detects the current git branch name.
func DetectBranch() (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found in PATH: %w", err)
	}

	// Try to get the branch name using git symbolic-ref (for normal branches)
	// If that fails, fall back to git rev-parse (for detached HEAD)
	var out bytes.Buffer
	cmd := exec.Command(gitPath, "symbolic-ref", "--short", "HEAD")
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}

	if err := cmd.Run(); err != nil {
		// Likely in detached HEAD state, try rev-parse
		cmd = exec.Command(gitPath, "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Stdout = &out
		cmd.Stderr = &bytes.Buffer{}
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to detect branch: %w", err)
		}
	}

	branch := strings.TrimSpace(out.String())
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("not on any branch (detached HEAD)")
	}

	return branch, nil
}

// findGitRoot finds the git repository root directory by searching upward.
func findGitRoot() (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found in PATH: %w", err)
	}

	var out bytes.Buffer
	cmd := exec.Command(gitPath, "rev-parse", "--show-toplevel")
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	root := strings.TrimSpace(out.String())
	if root == "" {
		return "", fmt.Errorf("git root is empty")
	}

	// Verify the .git directory exists
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", fmt.Errorf("git root .git directory not found: %w", err)
	}

	return root, nil
}
