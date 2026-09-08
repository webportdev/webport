package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunSingleConfiguredExitServiceWithLogs(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: feature/test
worktree_root: .
ports: {}
env:
  MESSAGE: configured
profiles:
  default:
    services: [setup]
services:
  setup:
    command: [sh, -c, "printf '%s\\n' \"$MESSAGE\""]
    completion: exit
    logs:
      destination: file
      path: .webport/setup.log
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := Run(context.Background(), Options{ConfigPath: configPath, Out: &output, ErrOut: &output})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "development session plan") || !strings.Contains(output.String(), "[setup] configured") {
		t.Fatalf("output = %q", output.String())
	}
	logData, err := os.ReadFile(filepath.Join(dir, ".webport", "setup.log"))
	if err != nil || string(logData) != "configured\n" {
		t.Fatalf("log = %q, %v", logData, err)
	}
}

func TestRunConfiguredProcessStopsOnContextCancellation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [server]}}
services:
  server:
    command: [sh, -c, "while true; do sleep 1; done"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	if err := Run(ctx, Options{ConfigPath: configPath, Out: &output, ErrOut: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "server") {
		t.Fatalf("output = %q", output.String())
	}
}
