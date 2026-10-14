package socket

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func shortDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "wps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestCallAndPeerAuthorization(t *testing.T) {
	directory := shortDir(t)
	path := filepath.Join(directory, "d.sock")
	var mu sync.Mutex
	var seen []int
	allow := true
	server, err := Listen(path, func(uid int) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, uid)
		return allow
	}, map[string]Handler{
		"echo": func(_ context.Context, payload json.RawMessage) (any, error) { return json.RawMessage(payload), nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %v, %v", info.Mode(), err)
	}

	var result map[string]string
	if err := Call(context.Background(), path, "echo", map[string]string{"hello": "world"}, &result); err != nil || result["hello"] != "world" {
		t.Fatalf("echo = %v, %v", result, err)
	}
	mu.Lock()
	if len(seen) != 1 || seen[0] != os.Getuid() {
		t.Fatalf("peer UIDs = %v, want [%d]", seen, os.Getuid())
	}
	mu.Unlock()
	if err := Call(context.Background(), path, "missing", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown operation") {
		t.Fatalf("unknown operation error = %v", err)
	}
	mu.Lock()
	allow = false
	mu.Unlock()
	if err := Call(context.Background(), path, "echo", nil, nil); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unauthorized error = %v", err)
	}
}

func TestDialRefusesSharedDirectories(t *testing.T) {
	directory := shortDir(t)
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(context.Background(), filepath.Join(directory, "d.sock")); err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Fatalf("shared directory error = %v", err)
	}
}
