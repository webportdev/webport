package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/secrets"
	"github.com/webportdev/webport/internal/devsession/state"
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

func TestRunUsesConcurrentSchedulerForMultipleServices(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles:
  default: {services: [api, worker]}
services:
  api:
    command: [sh, -c, "sleep .05"]
    completion: exit
  worker:
    command: [sh, -c, "sleep .05"]
    completion: exit
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(context.Background(), Options{ConfigPath: configPath, Out: &output, ErrOut: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "api") || !strings.Contains(output.String(), "worker") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestCleanSecretsRemovesProjectStoreAndRejectsActiveSession(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
env:
  PROJECT_SECRET:
    generate: {lifetime: project, length: 16, sets: [hex]}
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Resolve(cfg, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretStore, err := secrets.NewStore(filepathForSecretStore(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.ResolveValues(cfg.Env, secretStore, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretStore.Path); err != nil {
		t.Fatal(err)
	}
	options := Options{ConfigPath: configPath}
	if err := CleanSecrets(options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretStore.Path); !os.IsNotExist(err) {
		t.Fatalf("secret store stat error = %v", err)
	}

	if _, err := secrets.ResolveValues(cfg.Env, secretStore, nil); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = store.RemoveLive()
		_ = store.Release()
	}()
	if err := store.WriteLive(state.LiveState{SessionID: id.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err := CleanSecrets(options); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("CleanSecrets() error = %v", err)
	}
}
