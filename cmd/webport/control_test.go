package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

type fakeInstance struct {
	live       state.LiveState
	operations []string
}

func startFakeInstance(t *testing.T, dir, id, worktree, profile string, routes ...string) *fakeInstance {
	t.Helper()
	instance := &fakeInstance{}
	control, err := state.StartControl(filepath.Join(dir, id+".sock"), func(_ context.Context, request state.Request) state.Response {
		instance.operations = append(instance.operations, request.Operation)
		return state.Response{OK: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	instance.live = state.LiveState{SessionID: id, Worktree: worktree, Profile: profile, ControlPath: control.Path(), ControlToken: control.Token(), Routes: map[string]state.RouteState{}}
	for _, route := range routes {
		project, branch, _ := strings.Cut(route, ":")
		instance.live.Routes[route] = state.RouteState{Project: project, Branch: branch}
	}
	return instance
}

func controlFixture(t *testing.T, instances ...*fakeInstance) {
	t.Helper()
	previous := liveInstances
	liveInstances = func() ([]state.LiveState, error) {
		var values []state.LiveState
		for _, instance := range instances {
			values = append(values, instance.live)
		}
		return values, nil
	}
	t.Cleanup(func() { liveInstances = previous })
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	// Unix socket paths are short on macOS; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "wp-ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDevControlTargetsRouteIdentity(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	wrapper := startFakeInstance(t, dir, "w", "/projects/alpha", wrapperProfile, "alpha:feature/auth")
	configured := startFakeInstance(t, dir, "c", "/projects/beta", "default", "beta:main", "beta-api:main")
	controlFixture(t, wrapper, configured)

	var out bytes.Buffer
	if err := runDevControl(context.Background(), "restart", []string{"alpha:feature/auth"}, devsession.Options{}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runDevControl(context.Background(), "stop", []string{"beta-api:main"}, devsession.Options{}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(wrapper.operations, ",") != "restart" || strings.Join(configured.operations, ",") != "stop" {
		t.Fatalf("wrapper %v configured %v", wrapper.operations, configured.operations)
	}
	if !strings.Contains(out.String(), "command wrapper restart requested for alpha:feature/auth") || !strings.Contains(out.String(), "session stop requested for beta-api:main") {
		t.Fatalf("output %q", out.String())
	}
	for _, args := range [][]string{{"alpha-feature-auth"}, {"missing:main"}, {"a:b", "c:d"}} {
		if err := runDevControl(context.Background(), "stop", args, devsession.Options{}, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := runDevControl(context.Background(), "stop", []string{"beta:main"}, devsession.Options{ConfigPath: "x.yaml"}, &out); err == nil {
		t.Fatal("accepted --config with a route")
	}

	shared := startFakeInstance(t, dir, "s", "/projects/alpha-copy", wrapperProfile, "alpha:feature/auth")
	shared.live.PIDs = []state.ProcessIdentity{{PID: 4242}}
	controlFixture(t, wrapper, configured, shared)
	err := runDevControl(context.Background(), "stop", []string{"alpha:feature/auth"}, devsession.Options{}, &out)
	if err == nil || !strings.Contains(err.Error(), "supervisor PID 4242 in /projects/alpha-copy") || len(wrapper.operations)+len(shared.operations) != 1 {
		t.Fatalf("shared route: %v, wrapper %v shared %v", err, wrapper.operations, shared.operations)
	}
}

func TestDevControlFallsBackToWorktreeWrapper(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	worktree := filepath.Join(dir, "project")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	other := startFakeInstance(t, dir, "o", "/projects/other", wrapperProfile, "other:main")
	first := startFakeInstance(t, dir, "a", worktree, wrapperProfile, "app:main")
	controlFixture(t, other, first)

	var out bytes.Buffer
	options := devsession.Options{CurrentDir: worktree}
	if err := runDevControl(context.Background(), "stop", nil, options, &out); err != nil {
		t.Fatal(err)
	}
	if len(first.operations) != 1 || len(other.operations) != 0 {
		t.Fatalf("first %v other %v", first.operations, other.operations)
	}

	second := startFakeInstance(t, dir, "b", worktree, wrapperProfile, "app-docs:main")
	controlFixture(t, other, first, second)
	err := runDevControl(context.Background(), "stop", nil, options, &out)
	if err == nil || !strings.Contains(err.Error(), "app-docs:main, app:main") {
		t.Fatalf("ambiguous worktree error: %v", err)
	}

	controlFixture(t, other)
	if err := runDevControl(context.Background(), "stop", nil, options, &out); err == nil || !strings.Contains(err.Error(), "PROJECT:BRANCH") {
		t.Fatalf("empty worktree error: %v", err)
	}
}
