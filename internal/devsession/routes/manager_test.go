package routes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/plan"
)

type fakeClient struct {
	registers  []daemon.RouteRequest
	releases   []string
	err        error
	heartbeats int
}

func (f *fakeClient) Config(context.Context) (daemon.Config, error) {
	return daemon.Config{BaseDomain: "test"}, nil
}
func (f *fakeClient) Register(_ context.Context, request daemon.RouteRequest) (daemon.Lease, error) {
	f.registers = append(f.registers, request)
	if f.err != nil {
		return daemon.Lease{}, f.err
	}
	return daemon.Lease{ID: request.ClientID + "-lease", Route: daemon.Route{Project: request.Project, Branch: request.Branch, Port: request.Port}}, nil
}
func (f *fakeClient) Heartbeat(context.Context, string, time.Duration) (daemon.Lease, error) {
	f.heartbeats++
	if f.heartbeats == 1 {
		return daemon.Lease{}, daemon.ErrNotFound
	}
	return daemon.Lease{ID: "recovered", Route: daemon.Route{Port: 3000}}, nil
}
func (f *fakeClient) Release(_ context.Context, lease string) error {
	f.releases = append(f.releases, lease)
	return nil
}

func TestManagerActivatesIndependentRoutesAndReleasesThem(t *testing.T) {
	client := &fakeClient{}
	manager, err := NewManager(client, time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []plan.Route{{Service: "frontend", Project: "app", Branch: "main", Port: 3000}, {Service: "mailpit", Project: "app-mailpit", Branch: "main", Port: 8025}} {
		if err := manager.Activate(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.registers) != 2 || client.registers[0].ClientID == client.registers[1].ClientID {
		t.Fatalf("registers = %+v", client.registers)
	}
	if err := manager.ReleaseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.releases) != 2 {
		t.Fatalf("releases = %v", client.releases)
	}
	for name, entry := range manager.Snapshot() {
		if entry.State != Released {
			t.Fatalf("%s state = %s", name, entry.State)
		}
	}
}

func TestManagerOptionalFailureDoesNotBlockAndRequiredFailureReturns(t *testing.T) {
	client := &fakeClient{err: errors.New("daemon offline")}
	manager, err := NewManager(client, time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(context.Background(), plan.Route{Service: "optional", Optional: true, Project: "app", Branch: "main", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	if manager.Snapshot()["optional"].State != Unavailable {
		t.Fatalf("optional state = %+v", manager.Snapshot()["optional"])
	}
	if err := manager.Activate(context.Background(), plan.Route{Service: "required", Project: "app", Branch: "main", Port: 3001}); err == nil {
		t.Fatal("required activation error = nil")
	}
}

func TestManagerHeartbeatRecoversAfterDaemonRouteLoss(t *testing.T) {
	client := &fakeClient{}
	manager, err := NewManager(client, 100*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(context.Background(), plan.Route{Service: "api", Project: "app", Branch: "main", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(client.registers) < 2 {
		time.Sleep(time.Millisecond)
	}
	if len(client.registers) != 2 || manager.Snapshot()["api"].State != Active {
		t.Fatalf("recovery registers=%d state=%+v", len(client.registers), manager.Snapshot()["api"])
	}
	_ = manager.ReleaseAll(context.Background())
}
