package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/state"
)

func TestDaemonFailureDoesNotHideLocalInstances(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	store, err := state.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	if err := store.WriteLive(state.LiveState{SessionID: "local", Worktree: store.WorktreeRoot, PIDs: []state.ProcessIdentity{state.IdentifyProcess(os.Getpid())}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	b := LocalBackend{API: server.URL}
	snapshot := b.Snapshot(context.Background())
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].SessionID != "local" || !strings.Contains(snapshot.Error, "503") {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
