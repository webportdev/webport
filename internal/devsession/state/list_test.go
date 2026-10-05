package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListLiveIgnoresStaleAndRetainsValidOnCorruptFile(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	store, err := NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	live := LiveState{SessionID: "live", Worktree: store.WorktreeRoot, PIDs: []ProcessIdentity{IdentifyProcess(os.Getpid())}}
	if err := store.WriteLive(live); err != nil {
		t.Fatal(err)
	}
	stale, err := NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer stale.Release()
	if err := stale.WriteLive(LiveState{SessionID: "stale", PIDs: []ProcessIdentity{{PID: os.Getpid(), StartTime: "wrong"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Directory, "broken.live.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	instances, err := ListLive()
	if err == nil || len(instances) != 1 || instances[0].SessionID != "live" {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
	if _, err := os.Stat(stale.LivePath); err != nil {
		t.Fatal("listing removed stale state")
	}
}
