package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/ports"
)

type lookupFake struct{ missing map[string]bool }

func (f lookupFake) LookPath(name string) (string, error) {
	if f.missing[name] {
		return "", os.ErrNotExist
	}
	return filepath.Join("/bin", name), nil
}

func TestBuildSelectsClosureAndDeterministicDependencyOrder(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Profiles: map[string]config.Profile{"default": {Services: []string{"frontend", "mailpit"}}},
		Services: map[string]config.Service{
			"frontend": {Command: []string{"vite"}, DependsOn: []string{"backend"}},
			"backend":  {Command: []string{"air"}, DependsOn: []string{"setup"}},
			"setup":    {Command: []string{"./setup"}, Completion: "exit"},
			"mailpit":  {Command: []string{"mailpit"}},
		},
		Requires: config.Requires{Commands: []string{"go", "air"}},
	}
	got, err := Build(cfg, identity.Identity{Project: "app", Branch: "main", WorktreeRoot: dir, ConfigDirectory: dir, Scope: "app-scope"}, BuildOptions{Lookup: lookupFake{}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "default" || strings.Join(got.Order, ",") != "mailpit,setup,backend,frontend" {
		t.Fatalf("plan = profile %q order %v", got.Profile, got.Order)
	}
	if len(got.Services) != 4 || len(got.Roots) != 2 {
		t.Fatalf("closure = %+v", got.Services)
	}
}

func TestBuildCycleSelectionAndAggregateMissingCommands(t *testing.T) {
	dir := t.TempDir()
	base := func(services map[string]config.Service) config.Config {
		return config.Config{Profiles: map[string]config.Profile{"default": {Services: []string{"a"}}}, Services: services, Requires: config.Requires{Commands: []string{"missing-b", "missing-a"}}}
	}
	_, err := Build(base(map[string]config.Service{
		"a": {Command: []string{"a"}, DependsOn: []string{"b"}},
		"b": {Command: []string{"b"}, DependsOn: []string{"a"}},
	}), identity.Identity{ConfigDirectory: dir, WorktreeRoot: dir}, BuildOptions{Lookup: lookupFake{missing: map[string]bool{"missing-a": true, "missing-b": true}}})
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Fatalf("cycle error = %v", err)
	}

	_, err = Build(config.Config{Profiles: map[string]config.Profile{"default": {Services: []string{"a"}}}, Services: map[string]config.Service{"a": {Command: []string{"a"}}}, Requires: config.Requires{Commands: []string{"missing-b", "missing-a"}}}, identity.Identity{ConfigDirectory: dir, WorktreeRoot: dir}, BuildOptions{Lookup: lookupFake{missing: map[string]bool{"missing-a": true, "missing-b": true}}})
	if err == nil || !strings.Contains(err.Error(), "missing-a, missing-b") {
		t.Fatalf("missing command error = %v", err)
	}
}

func TestBuildRejectsReservedNamesAndInvalidServiceCombinations(t *testing.T) {
	dir := t.TempDir()
	id := identity.Identity{ConfigDirectory: dir, WorktreeRoot: dir}
	for _, services := range []map[string]config.Service{
		{"status": {Command: []string{"status"}}},
		{"api": {Command: []string{"api"}, Shell: "echo api"}},
		{"api": {Command: []string{"api"}, Completion: "bogus"}},
	} {
		_, err := Build(config.Config{Profiles: map[string]config.Profile{"default": {Services: []string{firstService(services)}}}, Services: services}, id, BuildOptions{Lookup: lookupFake{}})
		if err == nil {
			t.Errorf("Build(%v) error = nil", services)
		}
	}
}

func TestBuildRendersRedactedStablePlan(t *testing.T) {
	dir := t.TempDir()
	secret := "do-not-print"
	cfg := config.Config{
		Profiles: map[string]config.Profile{"default": {Services: []string{"api"}}},
		Services: map[string]config.Service{"api": {Command: []string{"api"}, Env: map[string]config.Value{"SECRET": {Literal: &secret, Sensitive: true}}}},
	}
	got, err := Build(cfg, identity.Identity{Project: "app", Branch: "main", WorktreeRoot: dir, ConfigDirectory: dir, Scope: "scope"}, BuildOptions{Lookup: lookupFake{}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := got.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "<redacted>") {
		t.Fatalf("JSON = %s", encoded)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Human(), "development session plan") {
		t.Fatal(got.Human())
	}
}

func TestBuildSelectedServiceIncludesDependenciesOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Services: map[string]config.Service{
		"api":      {Command: []string{"api"}, DependsOn: []string{"db"}},
		"db":       {Command: []string{"db"}},
		"frontend": {Command: []string{"frontend"}},
	}}
	got, err := Build(cfg, identity.Identity{ConfigDirectory: dir, WorktreeRoot: dir}, BuildOptions{Service: "api", Lookup: lookupFake{}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "service:api" || len(got.Services) != 2 || got.Services["frontend"].Name != "" {
		t.Fatalf("selected plan = %+v", got)
	}
}

func TestBuildAllowsRouteBackedDiscoveredPortForRuntimeActivation(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Ports:    map[string]config.Port{"frontend": {Discover: true}},
		Profiles: map[string]config.Profile{"default": {Services: []string{"frontend"}}},
		Services: map[string]config.Service{"frontend": {Command: []string{"frontend"}, Route: &config.Route{Port: "frontend"}}},
	}
	got, err := Build(cfg, identity.Identity{Project: "app", Branch: "main", WorktreeRoot: dir, ConfigDirectory: dir}, BuildOptions{
		Lookup: lookupFake{}, PortValues: map[string]ports.Allocation{"frontend": {Name: "frontend", Owner: "frontend", Discovered: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ports["frontend"].Discovered {
		t.Fatalf("discovered port lost from plan: %+v", got.Ports)
	}
}

func TestJSONIncludesDetailedServiceLifecycleSettings(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Profiles: map[string]config.Profile{"default": {Services: []string{"api"}}},
		Services: map[string]config.Service{"api": {
			Command: []string{"api"}, Completion: "exit",
			Ready: &config.Ready{TCP: "127.0.0.1:8000"}, Endpoints: map[string]string{"local": "http://127.0.0.1:8000"},
			Logs: &config.Logs{Destination: "file", Path: "api.log", Mode: "append"},
			Shutdown: &config.Shutdown{Command: []string{"stop-api"}, Timeout: config.Duration(2 * time.Second)},
			Route: &config.Route{Project: "app", Branch: "main", Port: "api", Optional: true, Export: map[string]string{"url": "API_URL"}},
		}},
	}
	got, err := Build(cfg, identity.Identity{Project: "app", Branch: "main", WorktreeRoot: dir, ConfigDirectory: dir}, BuildOptions{Lookup: lookupFake{}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := got.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"ready"`, `"endpoints"`, `"logs"`, `"shutdown"`, `"route"`, `"grace_period"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("JSON omitted %s: %s", field, encoded)
		}
	}
}

func firstService(services map[string]config.Service) string {
	for name := range services {
		return name
	}
	return ""
}

var _ = ports.Allocation{}
