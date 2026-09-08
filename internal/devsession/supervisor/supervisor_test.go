package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/plan"
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
