package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
)

func sessionFixture(t *testing.T, body string) Options {
	t.Helper()
	directory := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "webport-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	path := filepath.Join(directory, ".webport.yaml")
	header := "version: 1\nproject: sample\nbranch: feature/test\nworktree_root: .\n"
	if err := os.WriteFile(path, []byte(header+body), 0600); err != nil {
		t.Fatal(err)
	}
	return Options{ConfigPath: path, Out: io.Discard, ErrOut: io.Discard}
}

func TestInspectValidatesRuntimeInputsWithoutStartingOrGenerating(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"missing-env", "env: {BROKEN: '${env.UNDEFINED_WEBPORT_TEST_VALUE}'}\nservices: {app: {command: ['true']}}\n", "not defined"},
		{"cycle", "env: {ONE: '${env.TWO}', TWO: '${env.ONE}'}\nservices: {app: {command: ['true']}}\n", "cycle"},
		{"sensitive-argv", "env: {PASSWORD: {value: secret-value, sensitive: true}}\nservices: {app: {command: [echo, '${env.PASSWORD}']}}\n", "sensitive"},
		{"invalid-generator", "env: {PASSWORD: {generate: {length: -1}}}\nservices: {app: {command: ['true']}}\n", "length"},
		{"missing-dotenv", "session: {env_files: [missing.env]}\nservices: {app: {command: ['true']}}\n", "missing.env"},
		{"missing-command", "services: {app: {command: [webport-no-such-command-test]}}\n", "executable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := sessionFixture(t, "profiles: {default: {services: [app]}}\n"+test.body)
			_, err := InspectConfig(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("InspectConfig error = %v, want %s", err, test.want)
			}
			store, storeErr := state.NewStore(filepath.Dir(options.ConfigPath), "")
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if _, err := os.Stat(store.SecretPath); !os.IsNotExist(err) {
				t.Fatalf("inspection created secret state: %v", err)
			}
		})
	}
}

func TestInspectEnvironmentIncludesEverySelectedService(t *testing.T) {
	t.Setenv("WEBPORT_INHERITED_ENV_TEST", "inherited")
	options := sessionFixture(t, `profiles: {default: {services: [frontend, backend]}}
services:
  frontend: {command: ['true'], env: {FRONTEND_ONLY: frontend}}
  backend: {command: ['true'], env: {BACKEND_ONLY: backend}}
`)
	plan, err := InspectConfig(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Environment["FRONTEND_ONLY"].Literal == nil || plan.Environment["BACKEND_ONLY"].Literal == nil {
		t.Fatalf("session environment omitted a service: %+v", plan.Environment)
	}
	if _, ok := plan.Environment["WEBPORT_INHERITED_ENV_TEST"]; ok {
		t.Fatalf("default inspection included inherited environment: %+v", plan.Environment)
	}
	options.IncludeInherited = true
	plan, err = InspectConfig(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Environment["WEBPORT_INHERITED_ENV_TEST"].Literal == nil {
		t.Fatalf("include-inherited inspection omitted inherited environment: %+v", plan.Environment)
	}
}

func TestSelectedServiceIgnoresUnrelatedRoutesAndOccupiedPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	options := sessionFixture(t, fmt.Sprintf(`ports: {unused: {fixed: %d}}
profiles: {default: {services: [other]}}
services:
  app: {command: ['true'], completion: exit}
  other:
    command: [no-such-unselected-command]
    route: {port: unused}
`, port))
	options.Service = "app"
	options.API = "http://127.0.0.1:1"
	p, err := InspectConfig(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Services) != 1 || len(p.Ports) != 0 || len(p.Routes.Routes) != 0 {
		t.Fatalf("unselected resources in plan: %+v", p)
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
}

func TestSingleExitResourcePersistsUntilStopAndRecordsState(t *testing.T) {
	t.Setenv("WEBPORT_INHERITED_ENV_TEST", "inherited")
	options := sessionFixture(t, `profiles: {default: {services: [resource]}}
env:
  PASSWORD: {value: never-in-status, sensitive: true}
  GENERATED: {generate: {length: 16, sets: [hex]}}
services:
  resource:
    command: [sh, -c, 'printf "%s" "$WEBPORT_ROUTE" > identity']
    completion: exit
    endpoints: {local: 'http://127.0.0.1:9000'}
    shutdown: {command: [sh, -c, 'printf stopped > cleanup'], timeout: 1s}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	var live state.LiveState
	for {
		value, err := Status(options)
		if candidate, ok := value.(state.LiveState); err == nil && ok && candidate.Services["resource"].State == "success" {
			live = candidate
			break
		}
		select {
		case err := <-done:
			t.Fatalf("resource session exited before stop: %v", err)
		case <-ctx.Done():
			t.Fatal("resource never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if live.ControlToken != "" || len(live.PIDs) < 1 || live.Services["resource"].PID == 0 || !live.Services["resource"].Ready || live.Endpoints["resource.local"] == "" {
		t.Fatalf("incomplete live state: %+v", live)
	}
	encoded, _ := json.Marshal(live)
	if strings.Contains(string(encoded), "never-in-status") {
		t.Fatal("status leaked sensitive value")
	}
	defaultEnv, err := Control(ctx, options, "env", map[string]any{})
	if err != nil || !defaultEnv.OK {
		t.Fatalf("default env inspection failed: %+v, %v", defaultEnv, err)
	}
	defaultValues, ok := defaultEnv.Payload["values"].(map[string]any)
	if !ok {
		t.Fatalf("default env values = %#v", defaultEnv.Payload["values"])
	}
	if _, ok := defaultValues["WEBPORT_INHERITED_ENV_TEST"]; ok {
		t.Fatalf("default env inspection included inherited environment: %v", defaultValues)
	}
	allEnv, err := Control(ctx, options, "env", map[string]any{"include_inherited": true})
	if err != nil || !allEnv.OK {
		t.Fatalf("include-inherited env inspection failed: %+v, %v", allEnv, err)
	}
	allValues, ok := allEnv.Payload["values"].(map[string]any)
	if !ok || allValues["WEBPORT_INHERITED_ENV_TEST"] != "<redacted>" {
		t.Fatalf("include-inherited env values = %#v", allEnv.Payload["values"])
	}
	options.PreferLive = true
	hiddenPlan, err := InspectConfig(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	options.ShowSensitive = true
	visiblePlan, err := InspectConfig(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := hiddenPlan.JSON(true)
	visible, _ := visiblePlan.JSON(true)
	if strings.Contains(string(hidden), "never-in-status") || !strings.Contains(string(visible), "never-in-status") {
		t.Fatal("live config inspection did not honor sensitivity")
	}
	generated := visiblePlan.Services["resource"].Env["GENERATED"]
	if generated.Literal == nil || len(*generated.Literal) != 16 {
		t.Fatal("live config did not return the active generated value")
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(options.ConfigPath), "identity")); err != nil || string(data) != "sample:feature/test" {
		t.Fatalf("session identity = %q, %v", data, err)
	}
	if _, err := Control(ctx, options, "stop", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(options.ConfigPath), "cleanup")); err != nil {
		t.Fatalf("shutdown did not run: %v", err)
	}
	value, err := Status(options)
	last, ok := value.(state.LastSession)
	if err != nil || !ok || last.FinalStates["resource"] != "success" || last.Endpoints["resource.local"] == "" {
		t.Fatalf("last session = %+v, %v", value, err)
	}
	encoded, _ = json.Marshal(last)
	for _, forbidden := range []string{"never-in-status", "control_token", "lease_id", "\"pid\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("retained forbidden data %q", forbidden)
		}
	}
}

func TestFailedExitStartNeverRunsShutdownAndRetainsFailure(t *testing.T) {
	options := sessionFixture(t, `profiles: {default: {services: [resource]}}
services:
  resource:
    command: [sh, -c, 'exit 7']
    completion: exit
    shutdown: {command: [sh, -c, 'touch wrong-cleanup']}
`)
	if err := Run(context.Background(), options); err == nil {
		t.Fatal("failed start succeeded")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(options.ConfigPath), "wrong-cleanup")); !os.IsNotExist(err) {
		t.Fatalf("failed start registered cleanup: %v", err)
	}
	value, err := Status(options)
	last, ok := value.(state.LastSession)
	if err != nil || !ok || last.ExitCode != 7 || last.FinalStates["resource"] != "failed" || !strings.Contains(last.Initiating, "resource") {
		t.Fatalf("last session = %+v, %v", value, err)
	}
}

func TestInspectSensitiveValuesAreOptIn(t *testing.T) {
	options := sessionFixture(t, `profiles: {default: {services: [app]}}
env: {PASSWORD: {value: unique-sensitive-test-value, sensitive: true}}
services: {app: {command: ['true'], completion: exit}}
`)
	p, err := InspectConfig(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := p.JSON()
	visible, _ := p.JSON(true)
	if strings.Contains(string(hidden), "unique-sensitive-test-value") || !strings.Contains(string(visible), "unique-sensitive-test-value") {
		t.Fatal("JSON sensitivity flag ignored")
	}
	if strings.Contains(p.Human(), "unique-sensitive-test-value") || !strings.Contains(p.Human(true), "unique-sensitive-test-value") {
		t.Fatal("human sensitivity flag ignored")
	}
}

func TestMultipleServicesReceiveGenericSessionEnvironment(t *testing.T) {
	options := sessionFixture(t, `profiles: {default: {services: [api, worker]}}
services:
  api:
    command: [sh, -c, 'printf "%s|%s" "$WEBPORT_ROUTE" "$WEBPORT_SESSION_MANAGED" > api.env']
    completion: exit
  worker:
    command: [sh, -c, 'printf "%s|%s" "$WEBPORT_ROUTE" "$WEBPORT_SESSION_MANAGED" > worker.env']
    completion: exit
`)
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(options.ConfigPath), name+".env"))
		if err != nil || string(data) != "sample:feature/test|1" {
			t.Fatalf("%s identity = %q, %v", name, data, err)
		}
	}
}
