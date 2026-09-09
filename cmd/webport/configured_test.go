package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

func TestConfiguredCLIHelper(t *testing.T) {
	path := os.Getenv("WEBPORT_CONFIGURED_CLI_TEST")
	if path == "" {
		return
	}
	if err := runDevWithIO([]string{"--config", path}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredCLIHandlesSIGINTAndRunsCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [resource]}}
services:
  resource:
    command: [sh, -c, 'exit 0']
    completion: exit
    shutdown: {command: [sh, -c, 'touch cleanup']}
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := os.MkdirTemp("", "webport-cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDir)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfiguredCLIHelper$")
	child.Env = append(os.Environ(), "WEBPORT_CONFIGURED_CLI_TEST="+path)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	options := devsession.Options{ConfigPath: path}
	for {
		value, _ := devsession.Status(options)
		if live, ok := value.(state.LiveState); ok && live.Services["resource"].State == "success" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("session exited early: %v: %s", err, output.String())
		case <-ctx.Done():
			t.Fatal("session startup timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := child.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SIGINT cleanup failed: %v: %s", err, output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup")); err != nil {
		t.Fatalf("cleanup did not run: %v", err)
	}
	value, err := devsession.Status(options)
	last, ok := value.(state.LastSession)
	if err != nil || !ok || last.Signal != syscall.SIGINT.String() {
		t.Fatalf("last session = %+v, %v", value, err)
	}
}

func TestConfiguredConfigShowSensitiveFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [app]}}
env: {PASSWORD: {value: cli-sensitive-marker, sensitive: true}}
services: {app: {command: [sh, -c, 'exit 0'], completion: exit}}
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, show := range []bool{false, true} {
		args := []string{"config", "--config", path, "--format", "json"}
		if show {
			args = append(args, "--show-sensitive")
		}
		var output bytes.Buffer
		if err := runDevWithIO(args, nil, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "cli-sensitive-marker") != show {
			t.Fatalf("sensitive flag %v ignored", show)
		}
	}
}

func TestConfiguredConfigFiltersInheritedEnvironmentByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [app]}}
env: {WEBPORT_CONFIGURED_TEST_MARKER: configured-value}
services: {app: {command: [sh, -c, 'exit 0'], completion: exit}}
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBPORT_INHERITED_TEST_MARKER", "inherited-value")
	for _, includeInherited := range []bool{false, true} {
		args := []string{"config", "--config", path, "--format", "json"}
		if includeInherited {
			args = append(args, "--include-inherited")
		}
		var output bytes.Buffer
		if err := runDevWithIO(args, nil, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "WEBPORT_CONFIGURED_TEST_MARKER") {
			t.Fatalf("configured value missing with includeInherited=%t: %s", includeInherited, output.String())
		}
		containsInherited := strings.Contains(output.String(), "WEBPORT_INHERITED_TEST_MARKER")
		if containsInherited != includeInherited {
			t.Fatalf("inherited value visibility = %t, want %t: %s", containsInherited, includeInherited, output.String())
		}
	}
}
