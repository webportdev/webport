package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/plan"
	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
)

func TestInspectRouteUsesCurrentGitIdentityAndDerivedContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q", "-b", "main", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var matching atomic.Bool
	matching.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/routes" {
			http.NotFound(w, r)
			return
		}
		routes := []route.Route{{Project: "other", Branch: "main", Domain: "other-main.test", Port: 4100}}
		if matching.Load() {
			routes = append(routes, route.Route{Project: "app", Branch: "main", Domain: "app-main.test", Port: 3000})
		}
		_ = json.NewEncoder(w).Encode(route.RoutesList{Routes: routes})
	}))
	defer server.Close()

	value, err := inspectProject(context.Background(), inspectOptions{currentDir: root, api: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if value.Source != "route" || value.EnvironmentSource != "derived" || len(value.Routes) != 1 || value.Routes[0].URL != "https://app-main.test" {
		t.Fatalf("route inspection = %+v", value)
	}
	variables := value.Environment["route"]
	if variables["WEBPORT_URL"] != value.Routes[0].URL {
		t.Fatalf("route context = %+v", variables)
	}
	if _, found := variables["WEBPORT_APP_PORT"]; found {
		t.Fatal("derived context claimed an app port variable that the wrapper may not set")
	}
	if _, found := variables["WEBPORT_CLIENT_TOKEN"]; found {
		t.Fatal("inspection exposed a client token")
	}
	var human bytes.Buffer
	if err := writeProjectInspection(&human, value); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "route context (derived from active route)") {
		t.Fatalf("human output = %s", human.String())
	}
	_, err = inspectProject(context.Background(), inspectOptions{currentDir: root, api: server.URL, includeInherited: true})
	if err == nil || !strings.Contains(err.Error(), "requires a live configured session") {
		t.Fatalf("inherited route inspection error = %v", err)
	}
	matching.Store(false)
	_, err = inspectProject(context.Background(), inspectOptions{currentDir: root, api: server.URL})
	if err == nil || !strings.Contains(err.Error(), "no active route") {
		t.Fatalf("inactive route error = %v", err)
	}
}

func TestInspectConfigPathSelectsAnotherActiveWorktree(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, ".webport.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nproject: remote\nbranch: main\nworktree_root: .\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := os.MkdirTemp("", "webport-inspect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	store, err := state.NewStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	var legacy atomic.Bool
	control, err := state.StartControl(strings.TrimSuffix(store.LivePath, ".live.json")+".sock", func(_ context.Context, request state.Request) state.Response {
		if request.Operation == "config" {
			secret := "actual-secret"
			return state.Response{OK: true, Payload: map[string]any{"plan": plan.Plan{
				Profile: "default", Services: map[string]plan.Service{
					"api": {Env: map[string]config.Value{"SECRET": {Literal: &secret, Sensitive: true}}},
				},
			}}}
		}
		if request.Operation != "inspect" {
			return state.Response{Error: "unexpected operation"}
		}
		if legacy.Load() {
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
		secret := "<redacted>"
		if request.Payload["show_sensitive"] == true {
			secret = "actual-secret"
		}
		environment := map[string]string{"SECRET": secret}
		if request.Payload["include_inherited"] == true {
			environment["INHERITED"] = "yes"
		}
		return state.Response{OK: true, Payload: map[string]any{"inspection": devsession.LiveInspection{
			Project: "remote", Branch: "main", Worktree: root, Profile: "default",
			Routes:      []devsession.InspectionRoute{{Service: "api", Project: "remote", Branch: "main", URL: "https://remote-main.test"}},
			Environment: map[string]map[string]string{"api": environment},
		}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := store.WriteLive(state.LiveState{
		ControlPath: control.Path(), ControlToken: control.Token(),
		Routes: map[string]state.RouteState{
			"api": {State: "active", Project: "remote", Branch: "main", URL: "https://remote-main.test"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runCLI([]string{"inspect", "--config", configPath, "--format", "json"}, nil, &output, &output); err != nil {
		t.Fatal(err)
	}
	var value projectInspection
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Project != "remote" || value.Source != "configured" || value.Environment["api"]["SECRET"] != "actual-secret" || len(value.Routes) != 1 {
		t.Fatalf("inspection = %+v", value)
	}
	output.Reset()
	if err := runCLI([]string{"inspect", "--config", configPath}, nil, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "SECRET=\"actual-secret\"") || strings.Contains(output.String(), "<redacted>") {
		t.Fatalf("default inspection output = %s", output.String())
	}
	output.Reset()
	if err := runCLI([]string{"inspect", "--config", configPath, "--show-sensitive", "--include-inherited"}, nil, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "actual-secret") || !strings.Contains(output.String(), "INHERITED") {
		t.Fatalf("compatibility flag inspection output = %s", output.String())
	}
	legacy.Store(true)
	output.Reset()
	if err := runCLI([]string{"inspect", "--config", configPath, "--format", "json"}, nil, &output, &output); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Environment["api"]["SECRET"] != "actual-secret" || len(value.Routes) != 1 {
		t.Fatalf("older session inspection = %+v", value)
	}
}

func TestInspectInactiveConfigReportsPreviewCommand(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".webport.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nproject: inactive\nbranch: main\nworktree_root: .\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var output bytes.Buffer
	err := runCLI([]string{"inspect", "--config", path}, nil, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "no active session") || !strings.Contains(err.Error(), "webport dev config") {
		t.Fatalf("inactive error = %v", err)
	}
}
