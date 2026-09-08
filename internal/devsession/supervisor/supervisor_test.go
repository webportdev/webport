package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/routes"
)

func TestRunDiamondAndSharedExitDependency(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	services := map[string]plan.Service{
		"setup":    {Name: "setup", Command: []string{"sh", "-c", "printf setup > \"$MARK\""}, Completion: "exit"},
		"backend":  {Name: "backend", Command: []string{"sh", "-c", "test -f \"$MARK\" && printf backend >> \"$MARK\""}, Completion: "exit", DependsOn: []string{"setup"}},
		"frontend": {Name: "frontend", Command: []string{"sh", "-c", "test -f \"$MARK\" && printf frontend >> \"$MARK\""}, Completion: "exit", DependsOn: []string{"backend"}},
		"mailpit":  {Name: "mailpit", Command: []string{"sh", "-c", "test -f \"$MARK\""}, Completion: "exit", DependsOn: []string{"setup"}},
	}
	values := mustValues(t, marker)
	result, err := Run(context.Background(), plan.Plan{Order: []string{"setup", "backend", "frontend", "mailpit"}, Services: services}, Options{Environments: allEnvironments(values, services), Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	if result.States["frontend"] != StateSuccess || result.States["mailpit"] != StateSuccess {
		t.Fatalf("states = %+v", result.States)
	}
	data, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(data), "setup") || !strings.Contains(string(data), "backend") || !strings.Contains(string(data), "frontend") {
		t.Fatalf("marker = %q, %v", data, err)
	}
}

func TestRunStartsIndependentBranchesConcurrently(t *testing.T) {
	services := map[string]plan.Service{
		"a": {Name: "a", Command: []string{"sh", "-c", "sleep .15"}, Completion: "exit"},
		"b": {Name: "b", Command: []string{"sh", "-c", "sleep .15"}, Completion: "exit"},
	}
	values := mustValues(t, "")
	started := time.Now()
	result, err := Run(context.Background(), plan.Plan{Order: []string{"a", "b"}, Services: services}, Options{Environments: allEnvironments(values, services)})
	if err != nil || result.States["a"] != StateSuccess || result.States["b"] != StateSuccess {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if time.Since(started) > 280*time.Millisecond {
		t.Fatalf("branches did not run concurrently: %v", time.Since(started))
	}
}

func TestRunBlocksDependentAfterFailureAndCancelsRunningProcesses(t *testing.T) {
	services := map[string]plan.Service{
		"failed":    {Name: "failed", Command: []string{"sh", "-c", "exit 3"}, Completion: "exit"},
		"dependent": {Name: "dependent", Command: []string{"sh", "-c", "exit 0"}, Completion: "exit", DependsOn: []string{"failed"}},
	}
	values := mustValues(t, "")
	result, err := Run(context.Background(), plan.Plan{Order: []string{"failed", "dependent"}, Services: services}, Options{Environments: allEnvironments(values, services)})
	if err == nil || result.States["failed"] != StateFailed || result.States["dependent"] != StateBlocked {
		t.Fatalf("failure result = %+v, %v", result, err)
	}

	long := map[string]plan.Service{"server": {Name: "server", Command: []string{"sh", "-c", "while true; do sleep 1; done"}, Completion: "process"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err = Run(ctx, plan.Plan{Order: []string{"server"}, Services: long}, Options{Environments: allEnvironments(values, long)})
	if err != nil || result.States["server"] != StateStopped {
		t.Fatalf("cancel result = %+v, %v", result, err)
	}
}

func TestRunExecutesRegisteredShutdownCommandsInReverseOrder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cleanup")
	services := map[string]plan.Service{
		"resource": {
			Name: "resource", Command: []string{"sh", "-c", "exit 0"}, Completion: "exit",
			Shutdown: &config.Shutdown{Command: []string{"sh", "-c", "printf resource >> \"$CLEANUP\""}, Timeout: config.Duration(time.Second)},
		},
		"dependent": {
			Name: "dependent", Command: []string{"sh", "-c", "exit 0"}, Completion: "exit", DependsOn: []string{"resource"},
			Shutdown: &config.Shutdown{Command: []string{"sh", "-c", "printf dependent >> \"$CLEANUP\""}, Timeout: config.Duration(time.Second)},
		},
	}
	values := mustValuesWithCleanup(t, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	result, err := Run(ctx, plan.Plan{Order: []string{"resource", "dependent"}, Services: services}, Options{Environments: allEnvironments(values, services)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "dependentresource" {
		t.Fatalf("cleanup order = %q, %v; states=%+v", data, err, result.States)
	}
}

func TestRunActivatesRouteAfterReadinessAndReleasesBeforeReturn(t *testing.T) {
	client := &routeClientFake{}
	routeManager, err := routes.NewManager(client, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	service := plan.Service{Name: "frontend", Command: []string{"sh", "-c", "exit 0"}, Completion: "exit"}
	item := plan.Route{Service: "frontend", Project: "app", Branch: "main", Port: 3000, Available: true}
	values := mustValues(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Run(ctx, plan.Plan{Order: []string{"frontend"}, Services: map[string]plan.Service{"frontend": service}, Routes: plan.Routes{Routes: []plan.Route{item}}}, Options{
		Environments: map[string]env.Values{"frontend": values}, RouteManager: routeManager, RoutePlans: []plan.Route{item},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.registers) != 1 || len(client.releases) != 1 {
		t.Fatalf("route lifecycle registers=%d releases=%d", len(client.registers), len(client.releases))
	}
}

func TestReleaseRoutesUsesBoundedContext(t *testing.T) {
	client := &routeClientFake{blockRelease: true}
	routeManager, err := routes.NewManager(client, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	item := plan.Route{Service: "frontend", Project: "app", Branch: "main", Port: 3000, Available: true}
	if err := routeManager.Activate(context.Background(), item); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	err = releaseRoutesAndShutdown(nil, plan.Plan{}, Options{RouteManager: routeManager, RouteReleaseTimeout: 20 * time.Millisecond})
	if err == nil {
		t.Fatal("releaseRoutesAndShutdown() error = nil")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("route release took %v", elapsed)
	}
}

type routeClientFake struct {
	registers    []daemon.RouteRequest
	releases     []string
	blockRelease bool
}

func (f *routeClientFake) Config(context.Context) (daemon.Config, error) {
	return daemon.Config{BaseDomain: "test"}, nil
}
func (f *routeClientFake) Register(_ context.Context, request daemon.RouteRequest) (daemon.Lease, error) {
	f.registers = append(f.registers, request)
	return daemon.Lease{ID: "lease", Route: daemon.Route{Project: request.Project, Branch: request.Branch, Port: request.Port}}, nil
}
func (f *routeClientFake) Heartbeat(context.Context, string, time.Duration) (daemon.Lease, error) {
	return daemon.Lease{}, nil
}
func (f *routeClientFake) Release(ctx context.Context, lease string) error {
	if f.blockRelease {
		<-ctx.Done()
		return ctx.Err()
	}
	f.releases = append(f.releases, lease)
	return nil
}

func mustValuesWithCleanup(t *testing.T, path string) env.Values {
	t.Helper()
	values, err := env.Resolve(env.Input{Inherited: map[string]string{"CLEANUP": path}})
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func mustValues(t *testing.T, marker string) env.Values {
	t.Helper()
	inherited := map[string]string{}
	if marker != "" {
		inherited["MARK"] = marker
	}
	values, err := env.Resolve(env.Input{Inherited: inherited})
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func allEnvironments(values env.Values, services map[string]plan.Service) map[string]env.Values {
	result := make(map[string]env.Values, len(services))
	for name := range services {
		result[name] = values
	}
	return result
}
