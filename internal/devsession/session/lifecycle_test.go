package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
)

type fixtureDaemon struct {
	server *httptest.Server
	mu     sync.Mutex
	next   int
	leases map[string]fixtureLease
	seen   []fixtureRouteRequest
}

type fixtureRouteRequest struct {
	ClientID string
	Project  string
	Branch   string
	Port     int
}

type fixtureLease struct {
	Project string
	Branch  string
	Port    int
}

func newFixtureDaemon() *fixtureDaemon {
	d := &fixtureDaemon{leases: make(map[string]fixtureLease)}
	d.server = httptest.NewServer(http.HandlerFunc(d.handle))
	return d
}

func (d *fixtureDaemon) Close() { d.server.Close() }

func (d *fixtureDaemon) handle(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/config":
		_ = json.NewEncoder(response).Encode(map[string]any{
			"base_domain":         "dev.example.test",
			"default_ttl_seconds": 30,
			"tls_mode":            "local-ca",
		})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/leases":
		var value struct {
			ClientID string `json:"client_id"`
			Project  string `json:"project"`
			Branch   string `json:"branch"`
			Port     int    `json:"port"`
		}
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.next++
		leaseID := fmt.Sprintf("fixture-lease-%d", d.next)
		d.leases[leaseID] = fixtureLease{Project: value.Project, Branch: value.Branch, Port: value.Port}
		d.seen = append(d.seen, fixtureRouteRequest{ClientID: value.ClientID, Project: value.Project, Branch: value.Branch, Port: value.Port})
		d.mu.Unlock()
		writeFixtureLease(response, leaseID, value.Project, value.Branch, value.Port)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/v1/leases/") && strings.HasSuffix(request.URL.Path, "/heartbeat"):
		leaseID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/leases/"), "/heartbeat")
		d.mu.Lock()
		lease, ok := d.leases[leaseID]
		d.mu.Unlock()
		if !ok {
			http.Error(response, "not found", http.StatusNotFound)
			return
		}
		writeFixtureLease(response, leaseID, lease.Project, lease.Branch, lease.Port)
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/leases/"):
		leaseID := strings.TrimPrefix(request.URL.Path, "/v1/leases/")
		d.mu.Lock()
		delete(d.leases, leaseID)
		d.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(response, request)
	}
}

func writeFixtureLease(response http.ResponseWriter, leaseID, project, branch string, port int) {
	host := project + "-" + strings.ReplaceAll(branch, "/", "-") + ".dev.example.test"
	_ = json.NewEncoder(response).Encode(map[string]any{
		"lease_id": leaseID,
		"route": map[string]any{
			"project": project, "branch": branch, "port": port,
			"domain": host, "host": host,
		},
	})
}

func TestConfiguredLifecycleFixtureIsConcurrentAndSecretSafe(t *testing.T) {
	daemon := newFixtureDaemon()
	defer daemon.Close()
	fixtures := []struct {
		name string
		port int
	}{
		{name: "alpha", port: 39200},
		{name: "beta", port: 39300},
	}
	type runningFixture struct {
		name   string
		root   string
		config string
		output *safeBuffer
		done   chan error
	}
	running := make([]runningFixture, 0, len(fixtures))
	scopes := make(map[string]string)
	ports := make(map[int]string)
	t.Cleanup(func() {
		for _, fixture := range running {
			_, _ = Control(context.Background(), Options{ConfigPath: fixture.config}, "stop", nil)
		}
		for _, fixture := range running {
			select {
			case <-fixture.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
	for _, fixture := range fixtures {
		root := t.TempDir()
		configPath := filepath.Join(root, ".webport.yaml")
		writeFixtureConfig(t, configPath, fixture.name, fixture.port)
		output := &safeBuffer{}
		done := make(chan error, 1)
		options := Options{ConfigPath: configPath, API: daemon.server.URL, Out: output, ErrOut: output}
		go func() {
			done <- Run(context.Background(), options)
			close(done)
		}()
		running = append(running, runningFixture{name: fixture.name, root: root, config: configPath, output: output, done: done})
	}

	for index := range running {
		live := waitForLiveState(t, running[index].root)
		if live.ControlToken == "" || live.ControlPath == "" {
			t.Fatalf("fixture %d live control metadata = %+v", index, live)
		}
		if len(live.Ports) != 2 || len(live.Exports) != 2 || len(live.LogPaths) != 3 {
			t.Fatalf("fixture %d live metadata = %+v", index, live)
		}
		resolved, err := InspectConfig(context.Background(), Options{ConfigPath: running[index].config, API: daemon.server.URL})
		if err != nil {
			t.Fatal(err)
		}
		if previous, exists := scopes[resolved.Identity.Scope]; exists {
			t.Fatalf("scope %q is shared by %s and fixture %d", resolved.Identity.Scope, previous, index)
		}
		scopes[resolved.Identity.Scope] = running[index].name
		for portName, value := range live.Ports {
			if previous, exists := ports[value]; exists {
				t.Fatalf("port %d is shared by %s and fixture %d/%s", value, previous, index, portName)
			}
			ports[value] = running[index].name + "/" + portName
		}
		for _, path := range live.Exports {
			assertMode0600(t, path)
		}
		for _, path := range live.LogPaths {
			assertMode0600(t, path)
		}
		status, err := Status(Options{ConfigPath: running[index].config})
		if err != nil {
			t.Fatal(err)
		}
		statusJSON, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(statusJSON), live.ControlToken) {
			t.Fatalf("status exposed control token: %s", statusJSON)
		}
	}

	if err := waitForFixtureCondition(5*time.Second, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		return len(daemon.leases) == 4
	}); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	seen := append([]fixtureRouteRequest(nil), daemon.seen...)
	daemon.mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("route registrations = %+v", seen)
	}
	for _, request := range seen {
		if request.ClientID == "" || request.Project == "" || request.Branch == "" || request.Port == 0 {
			t.Fatalf("incomplete route registration = %+v", request)
		}
	}
	for _, fixture := range running {
		options := Options{ConfigPath: fixture.config}
		if _, err := Control(context.Background(), options, "stop", nil); err != nil {
			t.Fatal(err)
		}
	}
	for index := range running {
		select {
		case err := <-running[index].done:
			if err != nil {
				t.Fatalf("fixture %d run error: %v", index, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("fixture %d did not stop", index)
		}
		backendURL := "https://" + running[index].name + "-backend-feature-e2e.dev.example.test"
		assertFixtureArtifacts(t, running[index].root, running[index].config, running[index].output, daemon.server.URL, backendURL)
	}
	daemon.mu.Lock()
	remainingLeases := len(daemon.leases)
	daemon.mu.Unlock()
	if remainingLeases != 0 {
		t.Fatalf("leases remain after stop: %d", remainingLeases)
	}
}

func TestStreamFileFollowsRotatedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &safeBuffer{}
	done := make(chan error, 1)
	go func() { done <- streamFile(ctx, path, true, output) }()
	if err := waitForFixtureCondition(time.Second, func() bool { return strings.Contains(output.String(), "before") }); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForFixtureCondition(time.Second, func() bool { return strings.Contains(output.String(), "after") }); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func writeFixtureConfig(t *testing.T, path, project string, port int) {
	t.Helper()
	content := fmt.Sprintf(`version: 1
project: %s
branch: feature/e2e
worktree_root: .
session:
  retain_last_summary: true
  exports:
    bash: .webport/session.env
    json: .webport/session.json
ports:
  backend: {first_free: %d}
  frontend: {first_free: %d}
env:
  PROJECT_SECRET:
    sensitive: true
    generate: {lifetime: project, length: 24, sets: [hex]}
profiles:
  default: {services: [frontend]}
services:
  setup:
    command: [sh, -c, 'mkdir -p .webport/persistent && printf "$PROJECT_SECRET" > .webport/persistent/secret.marker']
    completion: exit
    depends_on: []
    logs: {destination: directory, path: .webport/logs}
  backend:
    depends_on: [setup]
    command: [sh, -c, 'printf "backend online\n"; while :; do sleep 1; done']
    route: {port: backend, export: {url: BACKEND_URL}}
    logs: {destination: directory, path: .webport/logs}
  frontend:
    depends_on: [backend]
    command: [sh, -c, 'printf "$BACKEND_URL" > .webport/frontend-url; printf "frontend online\n"; while :; do sleep 1; done']
    route: {port: frontend}
    logs: {destination: directory, path: .webport/logs}
`, project, port, port)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForLiveState(t *testing.T, root string) state.LiveState {
	t.Helper()
	store, err := state.NewStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var live state.LiveState
	if err := waitForFixtureCondition(5*time.Second, func() bool {
		live, err = store.ReadLive()
		return err == nil && len(live.Ports) == 2 && len(live.LogPaths) == 3
	}); err != nil {
		t.Fatal(err)
	}
	return live
}

func waitForFixtureCondition(timeout time.Duration, condition func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out waiting for fixture condition")
}

func assertFixtureArtifacts(t *testing.T, root, config string, output *safeBuffer, api, backendURL string) {
	t.Helper()
	secretData, err := os.ReadFile(filepath.Join(root, ".webport", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var secretDocument struct {
		Values map[string]struct {
			Value string `json:"value"`
		} `json:"values"`
	}
	if err := json.Unmarshal(secretData, &secretDocument); err != nil {
		t.Fatal(err)
	}
	secret := secretDocument.Values["PROJECT_SECRET"].Value
	if secret == "" {
		t.Fatal("project secret was not persisted")
	}
	marker, err := os.ReadFile(filepath.Join(root, ".webport", "persistent", "secret.marker"))
	if err != nil || string(marker) != secret {
		t.Fatalf("persistent secret marker = %q, %v", marker, err)
	}
	frontend, err := os.ReadFile(filepath.Join(root, ".webport", "frontend-url"))
	if err != nil || string(frontend) != backendURL {
		t.Fatalf("frontend route export = %q, %v", frontend, err)
	}
	lastStore, err := state.NewStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	last, err := lastStore.ReadLast()
	if err != nil {
		t.Fatal(err)
	}
	lastJSON, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	status, err := Status(Options{ConfigPath: config})
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := InspectConfig(context.Background(), Options{ConfigPath: config, API: api})
	if err != nil {
		t.Fatal(err)
	}
	configJSON, err := resolved.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lastStore.ReadLive(); !os.IsNotExist(err) {
		t.Fatalf("live state remains: %v", err)
	}
	artifacts := []string{output.String(), string(lastJSON), string(statusJSON), string(configJSON)}
	for _, path := range []string{
		filepath.Join(root, ".webport", "logs", "setup.log"),
		filepath.Join(root, ".webport", "logs", "backend.log"),
		filepath.Join(root, ".webport", "logs", "frontend.log"),
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		artifacts = append(artifacts, string(data))
	}
	for _, artifact := range artifacts {
		if strings.Contains(artifact, secret) {
			t.Fatalf("generated secret leaked into artifact %q", artifact)
		}
		if strings.Contains(artifact, "fixture-lease-") {
			t.Fatalf("lease credential leaked into artifact %q", artifact)
		}
	}
	for _, path := range []string{
		filepath.Join(root, ".webport", "session.env"),
		filepath.Join(root, ".webport", "session.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("export %s still exists: %v", path, err)
		}
	}
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %04o", path, info.Mode().Perm())
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(value)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*safeBuffer)(nil)
