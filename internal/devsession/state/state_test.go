package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLocksWorktreeAndRetainsLastSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	first, err := NewStore(filepath.Join(t.TempDir(), "worktree"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(first.SecretPath) != dir || filepath.Dir(first.SecretPath) == first.WorktreeRoot || filepath.Ext(first.SecretPath) != ".json" {
		t.Fatalf("secret path is not per-user runtime state: %s", first.SecretPath)
	}
	second, err := NewStore(first.WorktreeRoot, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Acquire(); err != nil {
		t.Fatal(err)
	}
	live := LiveState{SessionID: "session", Worktree: first.WorktreeRoot, Profile: "default", StartedAt: time.Now(), PIDs: []ProcessIdentity{{PID: os.Getpid()}}}
	if err := first.WriteLive(live); err != nil {
		t.Fatal(err)
	}
	if err := second.Acquire(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire() = %v", err)
	}
	read, err := first.ReadLive()
	if err != nil || read.SessionID != "session" {
		t.Fatalf("ReadLive() = %+v, %v", read, err)
	}
	if err := first.RemoveLive(); err != nil {
		t.Fatal(err)
	}
	if err := first.WriteLast(LastSession{SessionID: "session", Worktree: first.WorktreeRoot, StartedAt: live.StartedAt, StoppedAt: time.Now(), FinalStates: map[string]string{"api": "success"}}); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Acquire(); err != nil {
		t.Fatalf("second Acquire() after release = %v", err)
	}
	_ = second.Release()
	if _, err := first.ReadLast(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.LastPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("last state mode = %v, %v", info, err)
	}
}

func TestControlEndpointAuthenticatesAndUsesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := StartControl(path, func(_ context.Context, request Request) Response {
		return Response{OK: true, Payload: map[string]any{"operation": request.Operation}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := Dial(context.Background(), path, "wrong", Request{Operation: "status"})
	if err != nil || response.Status != 401 {
		t.Fatalf("unauthorized response = %+v, %v", response, err)
	}
	response, err = Dial(context.Background(), path, server.Token(), Request{Operation: "status"})
	if err != nil || !response.OK || response.Payload["operation"] != "status" {
		t.Fatalf("authorized response = %+v, %v", response, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", info, err)
	}
}

func TestStoreManagesLatestSessionLogs(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "worktree"), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	oldDirectory, err := store.SessionLogDirectory("old-session")
	if err != nil {
		t.Fatal(err)
	}
	currentDirectory, err := store.SessionLogDirectory("current-session")
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{oldDirectory, currentDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "api.log"), []byte("log\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneSessionLogs("current-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDirectory); !os.IsNotExist(err) {
		t.Fatalf("old session logs remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(currentDirectory, "api.log")); err != nil {
		t.Fatalf("current session logs missing: %v", err)
	}
	if err := store.RemoveSessionLogs("current-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(currentDirectory); !os.IsNotExist(err) {
		t.Fatalf("current session logs remain: %v", err)
	}
	if _, err := store.SessionLogDirectory("../outside"); err == nil {
		t.Fatal("unsafe session ID accepted")
	}
}

func TestAcquireRemovesStaleLiveStateWithoutSignalingRecordedPID(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "worktree"), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LivePath, []byte(`{"schema_version":1,"session_id":"stale","worktree":"stale","pids":[{"pid":999999,"start_time":"old"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	if _, err := os.Stat(store.LivePath); !os.IsNotExist(err) {
		t.Fatalf("stale live state still exists: %v", err)
	}
}

func TestProcessIdentityDoesNotTrustOnlyAReusedPID(t *testing.T) {
	identity := currentProcessIdentity(os.Getpid())
	if identity.StartTime == "" {
		t.Fatal("process identity lacks a PID-reuse guard")
	}
	if !processAlive(identity) {
		t.Fatal("current process is not alive")
	}
	identity.StartTime = "not-the-current-start-time"
	if processAlive(identity) {
		t.Fatal("processAlive accepted a mismatched start time")
	}
}
