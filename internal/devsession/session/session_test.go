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
	var streamed bytes.Buffer
	if err := StreamLogs(context.Background(), Options{ConfigPath: configPath}, "setup", false, &streamed); err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "configured\n" {
		t.Fatalf("streamed configured log = %q", streamed.String())
	}
}

func TestRunDefaultLogsAreReadableFromRetainedSession(t *testing.T) {
	dir := t.TempDir()
	runtimeDirectory, err := os.MkdirTemp("", "webport-test-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [setup]}}
services:
  setup:
    command: [sh, -c, "printf 'managed output\\n'"]
    completion: exit
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), Options{ConfigPath: configPath, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.ReadLast()
	if err != nil {
		t.Fatal(err)
	}
	path := last.LogPaths["setup"]
	if path == "" || strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatalf("managed log path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "managed output\n" {
		t.Fatalf("managed log = %q, %v", data, err)
	}
	var streamed bytes.Buffer
	if err := StreamLogs(context.Background(), Options{ConfigPath: configPath}, "setup", false, &streamed); err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "managed output\n" {
		t.Fatalf("streamed log = %q", streamed.String())
	}
	oldDirectory := filepath.Dir(path)
	withoutRetention := strings.Replace(content, "worktree_root: .\n", "worktree_root: .\nsession: {retain_last_summary: false}\n", 1)
	if err := os.WriteFile(configPath, []byte(withoutRetention), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), Options{ConfigPath: configPath, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDirectory); !os.IsNotExist(err) {
		t.Fatalf("previous managed log directory remains: %v", err)
	}
	if _, err := store.ReadLast(); !os.IsNotExist(err) {
		t.Fatalf("last session state retained with retention disabled: %v", err)
	}
	if entries, err := os.ReadDir(store.LogRoot); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("managed log directories retained: %v", entries)
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

func TestDefaultLogsAreReadableWhileSessionIsActive(t *testing.T) {
	dir := t.TempDir()
	runtimeDirectory, err := os.MkdirTemp("", "webport-test-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [server]}}
services:
  server:
    command: [sh, -c, "printf 'server online\\n'; while true; do sleep 1; done"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	stopped := false
	go func() {
		done <- Run(context.Background(), Options{ConfigPath: configPath, Out: &safeBuffer{}, ErrOut: &safeBuffer{}})
	}()
	t.Cleanup(func() {
		if stopped {
			return
		}
		_, _ = Control(context.Background(), Options{ConfigPath: configPath}, "stop", nil)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	if err := waitForFixtureCondition(2*time.Second, func() bool {
		var output bytes.Buffer
		return StreamLogs(context.Background(), Options{ConfigPath: configPath}, "server", false, &output) == nil && strings.Contains(output.String(), "server online")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Control(context.Background(), Options{ConfigPath: configPath}, "stop", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		stopped = true
	case <-time.After(2 * time.Second):
		t.Fatal("development session did not stop")
	}
}

func TestExplicitNoneDisablesLogRetrieval(t *testing.T) {
	dir := t.TempDir()
	runtimeDirectory, err := os.MkdirTemp("", "webport-test-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	configPath := filepath.Join(dir, ".webport.yaml")
	content := `version: 1
project: sample
branch: main
worktree_root: .
profiles: {default: {services: [setup]}}
services:
  setup:
    command: [sh, -c, "printf 'terminal only\\n'"]
    completion: exit
    logs: {destination: none}
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), Options{ConfigPath: configPath, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	_, err = LogFiles(Options{ConfigPath: configPath}, "setup")
	if err == nil || !strings.Contains(err.Error(), "logging is disabled") {
		t.Fatalf("LogFiles() error = %v", err)
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
